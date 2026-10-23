package repository

import (
	"context"
	"encoding/json"
	"fmt"

	entsql "entgo.io/ent/dialect/sql"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	dbaccount "github.com/Wei-Shaw/sub2api/ent/account"
	dbaccountgroup "github.com/Wei-Shaw/sub2api/ent/accountgroup"
	dbgroup "github.com/Wei-Shaw/sub2api/ent/group"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *accountRepository) DowngradeOpenAIAccountGroups(ctx context.Context, accountID, triggerGroupID int64, state service.OpenAIAutoGroupState) (bool, error) {
	tx, err := r.client.Tx(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	account, err := tx.Account.Query().Where(dbaccount.IDEQ(accountID), dbaccount.DeletedAtIsNil(), dbaccount.PlatformEQ(service.PlatformOpenAI)).ForUpdate().Only(ctx)
	if err != nil {
		return false, err
	}
	bindings, err := tx.AccountGroup.Query().Where(dbaccountgroup.AccountIDEQ(accountID)).WithGroup(func(query *dbent.GroupQuery) {
		query.Order(dbent.Asc(dbgroup.FieldID)).ForShare()
	}).All(ctx)
	if err != nil {
		return false, err
	}
	highIDs := make([]int64, 0)
	bound := make(map[int64]bool, len(bindings))
	triggerBound := false
	priority := 0
	for _, binding := range bindings {
		bound[binding.GroupID] = true
		state.FromGroupIDs = append(state.FromGroupIDs, binding.GroupID)
		if binding.Priority > priority {
			priority = binding.Priority
		}
		group := binding.Edges.Group
		if group != nil && group.Platform == service.PlatformOpenAI && group.RateMultiplier > service.OpenAIAutoGroupRateBoundary {
			highIDs = append(highIDs, group.ID)
			triggerBound = triggerBound || group.ID == triggerGroupID
			continue
		}
		state.ToGroupIDs = append(state.ToGroupIDs, binding.GroupID)
	}
	if !triggerBound {
		return false, nil
	}
	query := tx.Group.Query().Where(dbgroup.DeletedAtIsNil(), dbgroup.StatusEQ(service.StatusActive), dbgroup.PlatformEQ(service.PlatformOpenAI), dbgroup.RateMultiplierGTE(0), dbgroup.RateMultiplierLTE(service.OpenAIAutoGroupRateBoundary), func(selector *entsql.Selector) {
		selector.Where(entsql.EQ(selector.C("auto_route_enabled"), false))
	})
	if account.Type == service.AccountTypeAPIKey {
		query.Where(dbgroup.RequireOauthOnlyEQ(false))
	}
	lowGroups, err := query.Order(dbent.Asc(dbgroup.FieldID)).ForShare().All(ctx)
	if err != nil {
		return false, err
	}
	if len(lowGroups) == 0 {
		return false, fmt.Errorf("no compatible active GPT groups at or below 2x; create or enable a low-rate group, then retry")
	}
	if _, err := tx.AccountGroup.Delete().Where(dbaccountgroup.AccountIDEQ(accountID), dbaccountgroup.GroupIDIn(highIDs...)).Exec(ctx); err != nil {
		return false, err
	}
	for _, group := range lowGroups {
		if bound[group.ID] {
			continue
		}
		priority++
		if _, err := tx.AccountGroup.Create().SetAccountID(accountID).SetGroupID(group.ID).SetPriority(priority).Save(ctx); err != nil {
			return false, err
		}
		state.ToGroupIDs = append(state.ToGroupIDs, group.ID)
	}
	payload, err := json.Marshal(map[string]any{service.OpenAIAutoGroupStateExtraKey: state})
	if err != nil {
		return false, err
	}
	if _, err := tx.Client().ExecContext(ctx, "UPDATE accounts SET extra = COALESCE(extra, '{}'::jsonb) || $1::jsonb, updated_at = NOW() WHERE id = $2", string(payload), accountID); err != nil {
		return false, err
	}
	if err := enqueueSchedulerOutbox(ctx, tx.Client(), service.SchedulerOutboxEventAccountGroupsChanged, &accountID, nil, buildSchedulerGroupPayload(mergeGroupIDs(state.FromGroupIDs, state.ToGroupIDs))); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	r.syncSchedulerAccountSnapshotDetached(ctx, accountID)
	return true, nil
}
