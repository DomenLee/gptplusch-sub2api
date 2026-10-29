package service

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
)

type openAIAutoGroupAttempt struct {
	id         string
	generation string
	revision   string
	prior      time.Duration
	groupIDs   []int64
	mu         sync.Mutex
	sampled    bool
	overloaded bool
	failed     bool
	completed  bool
	depth      int
	wsPrior    time.Duration
	wsEnd      time.Time
}

func (s *OpenAIGatewayService) openAIAutoGroupAttempt(ctx context.Context, account *Account) *openAIAutoGroupAttempt {
	if s == nil || s.settingService == nil || s.accountRepo == nil || s.rateLimitService == nil || account == nil || account.Platform != PlatformOpenAI || account.IsShadow() {
		return nil
	}
	request, _ := ctx.Value(openAIAutoGroupContextKey{}).(*openAIAutoGroupRequest)
	if request == nil {
		return nil
	}
	group := request.group.Load()
	if group == nil || group.Platform != PlatformOpenAI || group.AutoRouteEnabled {
		return nil
	}
	p, err := s.settingService.openAIAutoGroupRuntimeSettings(ctx)
	if err != nil {
		slog.Warn("openai_auto_group_settings_failed", "account_id", account.ID, "error", err)
		return nil
	}
	if !p.Active() {
		return nil
	}
	request.mu.Lock()
	defer request.mu.Unlock()
	if request.attemptTurn != request.turn.Load() {
		request.attempts = nil
		request.lastAccount, request.lastEnd = 0, time.Time{}
		request.attemptTurn = request.turn.Load()
	}
	key := fmt.Sprintf("%d:%d", account.ID, request.turn.Load())
	if old := request.attempts[key]; old != nil {
		return old
	}
	current, err := s.accountRepo.GetByID(ctx, account.ID)
	if err != nil || current == nil {
		slog.Warn("openai_auto_group_sample_unavailable", "account_id", account.ID, "error", err)
		return nil
	}
	state := ReadOpenAIAutoGroupState(current.Extra)
	if !slices.Contains(current.GroupIDs, group.ID) {
		return nil
	}
	if state.Paused || p.SkipsGroups(current.GroupIDs) || openAIAutoGroupSkipsRestore(p, state) || (group.RateMultiplier <= OpenAIAutoGroupRateBoundary && len(state.RestoreBindings) == 0) {
		return nil
	}
	if request.attempts == nil {
		request.attempts = make(map[string]*openAIAutoGroupAttempt)
	}
	attempt := &openAIAutoGroupAttempt{id: request.id + ":" + strconv.FormatInt(request.turn.Load(), 10), generation: state.Generation, revision: p.Revision, groupIDs: append([]int64(nil), current.GroupIDs...)}
	request.attempts[key] = attempt
	return attempt
}

func (s *OpenAIGatewayService) beginOpenAIAutoGroup(ctx context.Context, account *Account) func(*OpenAIForwardResult, error) {
	attempt := s.openAIAutoGroupAttempt(ctx, account)
	if attempt == nil {
		return func(*OpenAIForwardResult, error) {}
	}
	request := ctx.Value(openAIAutoGroupContextKey{}).(*openAIAutoGroupRequest)
	start := time.Now()
	request.mu.Lock()
	if request.lastAccount == account.ID && !request.lastEnd.IsZero() {
		attempt.prior += start.Sub(request.lastEnd)
	}
	attempt.depth++
	prior := attempt.prior
	if attempt.depth > 1 {
		request.mu.Unlock()
		var once sync.Once
		return func(*OpenAIForwardResult, error) {
			once.Do(func() { request.mu.Lock(); attempt.depth--; request.mu.Unlock() })
		}
	}
	attempt.wsPrior, attempt.wsEnd = 0, time.Time{}
	request.lastAccount, request.lastEnd = account.ID, time.Time{}
	request.mu.Unlock()
	var once sync.Once
	return func(result *OpenAIForwardResult, err error) {
		once.Do(func() {
			request.mu.Lock()
			attempt.depth--
			attempt.prior = prior + time.Since(start)
			request.lastAccount, request.lastEnd = account.ID, time.Now()
			request.mu.Unlock()
			if result == nil {
				return
			}
			sample := OpenAIAutoGroupSample{ID: attempt.id, FirstTokenMS: -1, Cohort: openAIAutoGroupCohort(result)}
			sample.Success = err == nil && !result.ClientDisconnect && result.SucceededForScheduling()
			sample.Failed = !sample.Success
			if result.Stream && result.FirstTokenMs != nil && *result.FirstTokenMs >= 0 {
				firstToken := *result.FirstTokenMs
				if result.autoGroupFirstTokenMS != nil {
					firstToken = *result.autoGroupFirstTokenMS
				}
				sample.FirstTokenMS = int(prior.Milliseconds()) + firstToken
			} else {
				sample.Success = false
			}
			s.recordOpenAIAutoGroupSample(ctx, account, attempt, sample)
		})
	}
}

func (s *OpenAIGatewayService) recordOpenAIAutoGroupSample(ctx context.Context, account *Account, attempt *openAIAutoGroupAttempt, sample OpenAIAutoGroupSample) {
	attempt.mu.Lock()
	defer attempt.mu.Unlock()
	if attempt.sampled && !(sample.Success && !attempt.completed) && !(sample.Overload && !attempt.overloaded) && !(sample.Failed && !attempt.failed) {
		return
	}
	// Preserve errors already seen for this request while attaching its final
	// successful latency; a later success can never count toward recovery.
	sample.Failed = sample.Failed || attempt.failed
	sample.Overload = sample.Overload || attempt.overloaded
	repo, ok := s.accountRepo.(OpenAIAutoGroupRepository)
	counter, counterOK := s.rateLimitService.tempUnschedCache.(OpenAIAutoGroupCounter)
	if !ok || !counterOK {
		return
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	changed, err := repo.ProcessOpenAIAutoGroup(stateCtx, account.ID, attempt.generation, func(current *Account) (*OpenAIAutoGroupDecision, error) {
		// Recheck the revision under the account lock so old in-flight requests
		// cannot reset statistics after a settings save.
		policy, err := s.settingService.GetOpenAIAutoGroupSettings(stateCtx)
		if err != nil {
			return nil, err
		}
		if !policy.Active() || policy.Revision != attempt.revision {
			return nil, nil
		}
		state := ReadOpenAIAutoGroupState(current.Extra)
		if policy.SkipsGroups(current.GroupIDs) || openAIAutoGroupSkipsRestore(policy, state) {
			return nil, nil
		}
		expected, actual := append([]int64(nil), attempt.groupIDs...), append([]int64(nil), current.GroupIDs...)
		slices.Sort(expected)
		slices.Sort(actual)
		if !slices.Equal(expected, actual) {
			state.Paused, state.Status, state.Generation, state.Message = true, "paused", uuid.NewString(), "Groups changed while the request was running; review current groups before resuming automation"
			return &OpenAIAutoGroupDecision{State: state}, nil
		}
		history, added, err := counter.RecordOpenAIAutoGroupSample(stateCtx, account.ID, policy.Revision+":"+state.Generation, sample, policy, len(state.RestoreBindings) > 0)
		if err != nil {
			return nil, fmt.Errorf("request counting unavailable; check Redis and retry: %w", err)
		}
		if !added {
			return nil, nil
		}
		return EvaluateOpenAIAutoGroup(state, history, sample, policy, current.IsSchedulable(), time.Now()), nil
	})
	if err != nil {
		slog.Warn("openai_auto_group_failed", "account_id", account.ID, "error", err)
		_, saveErr := repo.ProcessOpenAIAutoGroup(stateCtx, account.ID, attempt.generation, func(current *Account) (*OpenAIAutoGroupDecision, error) {
			state := ReadOpenAIAutoGroupState(current.Extra)
			state.Status, state.Message, state.At = "failed", err.Error(), time.Now().UTC().Format(time.RFC3339)
			return &OpenAIAutoGroupDecision{State: state}, nil
		})
		if saveErr != nil {
			slog.Error("openai_auto_group_status_save_failed", "account_id", account.ID, "error", saveErr)
		}
		return
	}
	attempt.sampled = true
	attempt.completed = attempt.completed || sample.Success
	attempt.overloaded = attempt.overloaded || sample.Overload
	attempt.failed = attempt.failed || sample.Failed
	if changed {
		if request, ok := ctx.Value(openAIAutoGroupContextKey{}).(*openAIAutoGroupRequest); ok {
			current, loadErr := s.accountRepo.GetByID(stateCtx, account.ID)
			group := request.group.Load()
			if loadErr != nil || current == nil || group == nil || !slices.Contains(current.GroupIDs, group.ID) {
				request.removed.Store(account.ID, true)
			}
		}
		slog.Info("openai_auto_group_changed", "account_id", account.ID)
	}
}

func openAIAutoGroupSkipsRestore(policy *OpenAIAutoGroupSettings, state OpenAIAutoGroupState) bool {
	for _, binding := range state.RestoreBindings {
		if policy.SkipsGroups([]int64{binding.ID}) {
			return true
		}
	}
	return false
}

func (s *OpenAIGatewayService) beginOpenAIAutoGroupWSTiming(ctx context.Context, account *Account, start time.Time) func() {
	attempt := s.openAIAutoGroupAttempt(ctx, account)
	if attempt == nil {
		return func() {}
	}
	request := ctx.Value(openAIAutoGroupContextKey{}).(*openAIAutoGroupRequest)
	request.mu.Lock()
	if !attempt.wsEnd.IsZero() {
		attempt.wsPrior += start.Sub(attempt.wsEnd)
	}
	request.mu.Unlock()
	return func() {
		request.mu.Lock()
		attempt.wsPrior += time.Since(start)
		attempt.wsEnd = time.Now()
		request.mu.Unlock()
	}
}

func (s *OpenAIGatewayService) openAIAutoGroupWSFirstToken(ctx context.Context, account *Account, ms int) int {
	attempt := s.openAIAutoGroupAttempt(ctx, account)
	if attempt == nil {
		return ms
	}
	request := ctx.Value(openAIAutoGroupContextKey{}).(*openAIAutoGroupRequest)
	request.mu.Lock()
	defer request.mu.Unlock()
	return int(attempt.wsPrior.Milliseconds()) + ms
}
