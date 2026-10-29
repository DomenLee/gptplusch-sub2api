package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"time"

	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	dbaccountgroup "github.com/Wei-Shaw/sub2api/ent/accountgroup"
	dbgroup "github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/lib/pq"
)

// Serialize sampling, decisions and binding changes across all application
// instances. Redis contains bounded samples; the durable recovery snapshot and
// generation are committed together with group changes.
func (r *accountRepository) ProcessOpenAIAutoGroup(ctx context.Context, accountID int64, generation string, decide func(*service.Account) (*service.OpenAIAutoGroupDecision, error)) (bool, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	entity, err := tx.Account.Query().Where(dbaccount.IDEQ(accountID), dbaccount.DeletedAtIsNil(), dbaccount.PlatformEQ(service.PlatformOpenAI)).ForUpdate().Only(ctx)
	if err != nil {
		return false, err
	}
	account := accountEntityToService(entity)
	old := service.ReadOpenAIAutoGroupState(account.Extra)
	if old.Generation != generation || old.Paused || account.IsShadow() {
		return false, nil
	}
	bindings, err := tx.AccountGroup.Query().Where(dbaccountgroup.AccountIDEQ(accountID)).WithGroup(func(q *dbent.GroupQuery) { q.Order(dbent.Asc(dbgroup.FieldID)).ForShare() }).All(ctx)
	if err != nil {
		return false, err
	}
	current := make([]service.OpenAIAutoGroupBinding, 0, len(bindings))
	highIDs := make([]int64, 0)
	for _, binding := range bindings {
		current = append(current, service.OpenAIAutoGroupBinding{ID: binding.GroupID, Priority: binding.Priority})
		account.GroupIDs = append(account.GroupIDs, binding.GroupID)
		if g := binding.Edges.Group; g != nil && g.Platform == service.PlatformOpenAI && g.RateMultiplier > service.OpenAIAutoGroupRateBoundary {
			highIDs = append(highIDs, g.ID)
		}
	}
	if len(old.ExpectedBindings) > 0 && !autoGroupBindingsEqual(current, old.ExpectedBindings) {
		old.Paused, old.Status, old.Generation, old.Message = true, "paused", uuid.NewString(), "Group bindings changed; automatic grouping paused for manual review"
		if err := saveAutoGroupState(ctx, tx.Client(), accountID, old); err != nil {
			return false, err
		}
		return false, tx.Commit()
	}
	decision, err := decide(account)
	if err != nil || decision == nil {
		return false, err
	}
	// Recheck against the locked bindings immediately before mutation.
	for _, binding := range current {
		if slices.Contains(decision.SkipGroupIDs, binding.ID) {
			return false, nil
		}
	}
	if decision.Action == "up" {
		for _, binding := range old.RestoreBindings {
			if slices.Contains(decision.SkipGroupIDs, binding.ID) {
				return false, nil
			}
		}
	}
	state := decision.State
	changed := false
	if decision.Action != "" {
		until, _ := time.Parse(time.RFC3339Nano, old.ProtectedUntil)
		if time.Now().Before(until) {
			decision.Action = ""
		}
	}
	if decision.Action == "down" && len(highIDs) > 0 && len(old.RestoreBindings) == 0 {
		query := tx.Group.Query().Where(dbgroup.DeletedAtIsNil(), dbgroup.StatusEQ(service.StatusActive), dbgroup.PlatformEQ(service.PlatformOpenAI), dbgroup.RateMultiplierGTE(0), dbgroup.RateMultiplierLTE(service.OpenAIAutoGroupRateBoundary), func(s *entsql.Selector) { s.Where(entsql.EQ(s.C("auto_route_enabled"), false)) })
		if account.Type == service.AccountTypeAPIKey {
			query.Where(dbgroup.RequireOauthOnlyEQ(false))
		}
		groups, err := query.Order(dbent.Asc(dbgroup.FieldID)).ForShare().All(ctx)
		if err != nil {
			return false, err
		}
		groups = slices.DeleteFunc(groups, func(g *dbent.Group) bool { return slices.Contains(decision.SkipGroupIDs, g.ID) })
		if len(groups) == 0 {
			return false, fmt.Errorf("no compatible active GPT groups at or below 2x; create or enable a low-rate group not listed in skip_group_ids, then retry")
		}
		high := make(map[int64]bool, len(highIDs))
		for _, id := range highIDs {
			high[id] = true
		}
		target := make([]service.OpenAIAutoGroupBinding, 0)
		bound := make(map[int64]bool)
		priority := 0
		for _, b := range current {
			priority = max(priority, b.Priority)
			if !high[b.ID] {
				target = append(target, b)
				bound[b.ID] = true
			}
		}
		for _, g := range groups {
			if !bound[g.ID] {
				priority++
				target = append(target, service.OpenAIAutoGroupBinding{ID: g.ID, Priority: priority})
			}
		}
		if err := replaceAutoGroupBindings(ctx, tx, accountID, current, target); err != nil {
			return false, err
		}
		state.RestoreBindings, state.ExpectedBindings = current, target
		state.Status, state.Message = "downgraded", "Moved to compatible groups at or below 2x"
		changed = true
	} else if decision.Action == "up" && len(old.RestoreBindings) > 0 && account.IsSchedulable() {
		// Every original high group must still be active and compatible. Never add
		// an unrelated high group just because an account is responding quickly.
		restoreIDs := make([]int64, 0)
		for _, b := range old.RestoreBindings {
			restoreIDs = append(restoreIDs, b.ID)
		}
		groups, err := tx.Group.Query().Where(dbgroup.IDIn(restoreIDs...), dbgroup.DeletedAtIsNil(), func(s *entsql.Selector) { s.Where(entsql.EQ(s.C("auto_route_enabled"), false)) }).Order(dbent.Asc(dbgroup.FieldID)).ForShare().All(ctx)
		if err != nil {
			return false, err
		}
		currentIDs := make(map[int64]bool)
		for _, b := range current {
			currentIDs[b.ID] = true
		}
		byID := make(map[int64]*dbent.Group)
		for _, g := range groups {
			byID[g.ID] = g
		}
		restoredHigh := 0
		for _, b := range old.RestoreBindings {
			if currentIDs[b.ID] {
				continue
			}
			g := byID[b.ID]
			if g == nil || g.Platform != service.PlatformOpenAI || g.Status != service.StatusActive || g.RateMultiplier <= service.OpenAIAutoGroupRateBoundary || (account.Type == service.AccountTypeAPIKey && g.RequireOauthOnly) {
				return false, fmt.Errorf("original high-rate group #%d is unavailable or incompatible; review account groups", b.ID)
			}
			restoredHigh++
		}
		if restoredHigh == 0 {
			return false, fmt.Errorf("no original high-rate groups can be restored; review account groups")
		}
		if err := replaceAutoGroupBindings(ctx, tx, accountID, current, old.RestoreBindings); err != nil {
			return false, err
		}
		state.ExpectedBindings = old.RestoreBindings
		state.RestoreBindings = nil
		state.RecoveryCohorts = nil
		state.Status, state.Message = "upgraded", "Restored original high-rate groups and priorities"
		changed = true
	}
	if changed {
		state.FromGroupIDs, state.ToGroupIDs = nil, nil
		for _, b := range current {
			state.FromGroupIDs = append(state.FromGroupIDs, b.ID)
		}
		for _, b := range state.ExpectedBindings {
			state.ToGroupIDs = append(state.ToGroupIDs, b.ID)
		}
		state.Generation = uuid.NewString()
		state.ProtectedUntil = time.Now().Add(time.Duration(decision.ProtectionMinutes) * time.Minute).UTC().Format(time.RFC3339Nano)
		state.RecoveryCount = 0
	}
	if err := saveAutoGroupState(ctx, tx.Client(), accountID, state); err != nil {
		return false, err
	}
	if changed {
		if err := enqueueSchedulerOutbox(ctx, tx.Client(), service.SchedulerOutboxEventAccountGroupsChanged, &accountID, nil, buildSchedulerGroupPayload(mergeGroupIDs(state.FromGroupIDs, state.ToGroupIDs))); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	if changed {
		r.syncSchedulerAccountSnapshotDetached(ctx, accountID)
	}
	return changed, nil
}

func saveAutoGroupState(ctx context.Context, client *dbent.Client, id int64, state service.OpenAIAutoGroupState) error {
	payload, err := json.Marshal(map[string]any{service.OpenAIAutoGroupStateExtraKey: state})
	if err != nil {
		return err
	}
	_, err = client.ExecContext(ctx, "UPDATE accounts SET extra = COALESCE(extra, '{}'::jsonb) || $1::jsonb, updated_at = NOW() WHERE id = $2", string(payload), id)
	return err
}

func autoGroupBindingsEqual(a, b []service.OpenAIAutoGroupBinding) bool {
	if len(a) != len(b) {
		return false
	}
	left, right := append([]service.OpenAIAutoGroupBinding(nil), a...), append([]service.OpenAIAutoGroupBinding(nil), b...)
	sort.Slice(left, func(i, j int) bool { return left[i].ID < left[j].ID })
	sort.Slice(right, func(i, j int) bool { return right[i].ID < right[j].ID })
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func replaceAutoGroupBindings(ctx context.Context, tx *dbent.Tx, id int64, from, to []service.OpenAIAutoGroupBinding) error {
	target := make(map[int64]int)
	existing := make(map[int64]int)
	for _, b := range to {
		target[b.ID] = b.Priority
	}
	for _, b := range from {
		existing[b.ID] = b.Priority
	}
	remove := make([]int64, 0)
	for _, b := range from {
		if _, ok := target[b.ID]; !ok {
			remove = append(remove, b.ID)
		}
	}
	if len(remove) > 0 {
		if _, err := tx.AccountGroup.Delete().Where(dbaccountgroup.AccountIDEQ(id), dbaccountgroup.GroupIDIn(remove...)).Exec(ctx); err != nil {
			return err
		}
	}
	for _, b := range to {
		if priority, ok := existing[b.ID]; ok {
			if priority != b.Priority {
				if _, err := tx.AccountGroup.Update().Where(dbaccountgroup.AccountIDEQ(id), dbaccountgroup.GroupIDEQ(b.ID)).SetPriority(b.Priority).Save(ctx); err != nil {
					return err
				}
			}
			continue
		}
		if _, err := tx.AccountGroup.Create().SetAccountID(id).SetGroupID(b.ID).SetPriority(b.Priority).Save(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Called inside explicit binding transactions, including clear-all and bulk
// edits. Preserve recovery evidence but stop automatic actions after an edit.
func pauseAutoGroupForManualBindings(ctx context.Context, exec sqlExecutor, ids []int64, skipGroupID ...int64) error {
	if len(ids) == 0 {
		return nil
	}
	args := []any{pq.Array(ids)}
	filter := ""
	if len(skipGroupID) > 0 {
		args = append(args, skipGroupID[0])
		filter = " AND NOT EXISTS (SELECT 1 FROM account_groups WHERE account_id=accounts.id AND group_id=$2)"
	}
	_, err := exec.ExecContext(ctx, `UPDATE accounts SET extra = jsonb_set(extra, '{openai_auto_group_state}',
 (extra->'openai_auto_group_state') || jsonb_build_object('paused', true, 'status', 'paused', 'generation', gen_random_uuid()::text, 'message', 'Groups edited manually; automatic grouping paused'))
 WHERE id = ANY($1) AND platform = 'openai' AND extra ? 'openai_auto_group_state'`+filter, args...)
	return err
}

// Resume changes only the managed JSON key while holding the account lock;
// quota, proxy, billing and other configuration are preserved atomically.
func (r *accountRepository) ResumeOpenAIAutoGroup(ctx context.Context, id int64) error {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	entity, err := tx.Account.Query().Where(dbaccount.IDEQ(id), dbaccount.DeletedAtIsNil(), dbaccount.PlatformEQ(service.PlatformOpenAI)).ForUpdate().Only(ctx)
	if err != nil {
		return err
	}
	account := accountEntityToService(entity)
	if account.IsShadow() {
		return fmt.Errorf("shadow accounts cannot use automatic grouping")
	}
	previous := service.ReadOpenAIAutoGroupState(account.Extra)
	if !previous.Paused {
		return nil
	}
	state := service.OpenAIAutoGroupState{Status: "tracking", Generation: uuid.NewString(), ProtectedUntil: previous.ProtectedUntil, At: time.Now().UTC().Format(time.RFC3339), Message: "Automatic grouping resumed from current manual bindings"}
	if err := saveAutoGroupState(ctx, tx.Client(), id, state); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	r.syncSchedulerAccountSnapshotDetached(ctx, id)
	return nil
}
