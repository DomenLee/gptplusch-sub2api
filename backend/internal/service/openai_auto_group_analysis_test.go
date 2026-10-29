package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func groupSample(id int, cohort string, ms int) OpenAIAutoGroupSample {
	return OpenAIAutoGroupSample{ID: fmt.Sprint(id), Cohort: cohort, FirstTokenMS: ms, Success: true}
}

func TestOpenAIAutoGroupSlowRequestBoundaryAndIsolation(t *testing.T) {
	p := DefaultOpenAIAutoGroupSettings()
	p.LatencyEnabled = true
	h := &OpenAIAutoGroupHistory{}
	values := []int{10000, 11000, 1000, 11000, 11000}
	for i, ms := range values {
		sample := groupSample(i, "astra-long", ms)
		UpdateOpenAIAutoGroupHistory(h, sample, p, false)
		d := EvaluateOpenAIAutoGroup(OpenAIAutoGroupState{}, h, sample, p, true, time.Now())
		require.Equal(t, i == 4, d.Action == "down")
	}
	fast := groupSample(99, "sol-short", 500)
	UpdateOpenAIAutoGroupHistory(h, fast, p, false)
	require.Equal(t, "down", EvaluateOpenAIAutoGroup(OpenAIAutoGroupState{}, h, fast, p, true, time.Now()).Action, "a fast different model must not hide the existing slow category")
	for i := 100; i < 110; i++ {
		UpdateOpenAIAutoGroupHistory(h, groupSample(i, "astra-long", 1000), p, false)
	}
	require.Empty(t, EvaluateOpenAIAutoGroup(OpenAIAutoGroupState{}, h, groupSample(110, "astra-long", 1000), p, true, time.Now()).Action)
}

func TestOpenAIAutoGroupRecoveryNeedsMatchingFreshRequests(t *testing.T) {
	p := DefaultOpenAIAutoGroupSettings()
	p.RecoveryEnabled = true
	now := time.Now()
	state := OpenAIAutoGroupState{Status: "downgraded", RestoreBindings: []OpenAIAutoGroupBinding{{ID: 7, Priority: 2}}, RecoveryCohorts: []string{"astra-long"}, ProtectedUntil: now.Add(time.Minute).Format(time.RFC3339Nano)}
	h := &OpenAIAutoGroupHistory{}
	for i := 0; i < 30; i++ {
		UpdateOpenAIAutoGroupHistory(h, groupSample(i, "sol-short", 100), p, true)
	}
	require.Empty(t, EvaluateOpenAIAutoGroup(state, h, groupSample(29, "sol-short", 100), p, true, now.Add(time.Hour)).Action, "a different model cannot restore the slow one")
	for i := 30; i < 60; i++ {
		UpdateOpenAIAutoGroupHistory(h, groupSample(i, "astra-long", 1000), p, true)
	}
	sample := groupSample(59, "astra-long", 1000)
	require.Empty(t, EvaluateOpenAIAutoGroup(state, h, sample, p, true, now).Action, "protection blocks even with enough requests")
	require.Empty(t, EvaluateOpenAIAutoGroup(state, h, sample, p, false, now.Add(time.Hour)).Action, "scheduler cooldown blocks recovery")
	require.Equal(t, "up", EvaluateOpenAIAutoGroup(state, h, sample, p, true, now.Add(time.Hour)).Action)
	errSample := OpenAIAutoGroupSample{ID: "429", Overload: true, Failed: true, StatusCode: 429}
	require.True(t, UpdateOpenAIAutoGroupHistory(h, errSample, p, true))
	require.Empty(t, h.Cohorts)
	require.True(t, UpdateOpenAIAutoGroupHistory(h, OpenAIAutoGroupSample{ID: "429", Success: true, Cohort: "astra-long", FirstTokenMS: 0}, p, true))
	require.Empty(t, h.Cohorts, "successful retry must not become recovery evidence")
	for i := 60; i < 89; i++ {
		UpdateOpenAIAutoGroupHistory(h, groupSample(i, "astra-long", 1000), p, true)
	}
	require.Empty(t, EvaluateOpenAIAutoGroup(state, h, sample, p, true, now.Add(time.Hour)).Action)
	UpdateOpenAIAutoGroupHistory(h, groupSample(89, "astra-long", 1000), p, true)
	require.Equal(t, "up", EvaluateOpenAIAutoGroup(state, h, sample, p, true, now.Add(time.Hour)).Action)
}

func TestOpenAIAutoGroupRecoveryFastStreakMedianAndObservation(t *testing.T) {
	p := DefaultOpenAIAutoGroupSettings()
	p.RecoveryEnabled = true
	state := OpenAIAutoGroupState{RestoreBindings: []OpenAIAutoGroupBinding{{ID: 7}}, RecoveryCohorts: []string{"a"}}
	h := &OpenAIAutoGroupHistory{}
	for i := 0; i < 30; i++ {
		UpdateOpenAIAutoGroupHistory(h, groupSample(i, "a", 9000), p, true)
	}
	require.False(t, openAIAutoGroupRecovered(h.Cohorts["a"], p), "90 percent alone cannot hide a slow median")
	for i := 30; i < 60; i++ {
		UpdateOpenAIAutoGroupHistory(h, groupSample(i, "a", 1000), p, true)
	}
	UpdateOpenAIAutoGroupHistory(h, groupSample(60, "a", 10000), p, true)
	require.False(t, openAIAutoGroupRecovered(h.Cohorts["a"], p), "exactly 10s is not a fast request")
	for i := 61; i < 71; i++ {
		UpdateOpenAIAutoGroupHistory(h, groupSample(i, "a", 1000), p, true)
	}
	require.True(t, openAIAutoGroupRecovered(h.Cohorts["a"], p))
	p.ObserveOnly = true
	d := EvaluateOpenAIAutoGroup(state, h, groupSample(70, "a", 1000), p, true, time.Now())
	require.Empty(t, d.Action)
	require.Equal(t, "observing", d.State.Status)
}

func TestOpenAIAutoGroupCohortUsesActualModelAndTotalInput(t *testing.T) {
	effort, tier := "high", "priority"
	a := &OpenAIForwardResult{Model: "alias", UpstreamModel: "actual", ReasoningEffort: &effort, ServiceTier: &tier, Usage: OpenAIUsage{InputTokens: 200000, CacheReadInputTokens: 190000}}
	key := openAIAutoGroupCohort(a)
	require.Contains(t, key, "actual")
	var parts []string
	require.NoError(t, json.Unmarshal([]byte(key), &parts))
	require.Equal(t, ">128k", parts[2])
	a.Model = "another-alias"
	require.Equal(t, key, openAIAutoGroupCohort(a))
	a.Usage.InputTokens = 1000
	require.NotEqual(t, key, openAIAutoGroupCohort(a))
}

func TestOpenAIAutoGroupObserverRetriesAndMissingTokens(t *testing.T) {
	svc, repo, counter := autoGroupTestService(t)
	p, _ := svc.settingService.GetOpenAIAutoGroupSettings(context.Background())
	p.LatencyEnabled = true
	require.NoError(t, svc.settingService.SetOpenAIAutoGroupSettings(context.Background(), p))
	account := &Account{ID: 20, Platform: PlatformOpenAI}
	ctx, _ := autoGroupTestRequest(3)
	attempt := svc.openAIAutoGroupAttempt(ctx, account)
	attempt.prior = 11 * time.Second
	finish := svc.beginOpenAIAutoGroup(ctx, account)
	ms := 100
	result := &OpenAIForwardResult{Model: "model", Stream: true, FirstTokenMs: &ms, Usage: OpenAIUsage{InputTokens: 2000}}
	finish(result, nil)
	finish(result, nil)
	require.Equal(t, 1, counter.calls)
	require.Greater(t, counter.history.Requests[0].FirstTokenMS, 10000)
	ctx, _ = autoGroupTestRequest(3)
	svc.beginOpenAIAutoGroup(ctx, account)(&OpenAIForwardResult{Model: "model", Stream: true}, nil)
	require.Equal(t, 2, counter.calls)
	require.False(t, counter.history.Requests[1].Success)
	ctx, _ = autoGroupTestRequest(3)
	svc.beginOpenAIAutoGroup(ctx, account)(result, errors.New("stream failed"))
	require.False(t, counter.history.Requests[2].Success)
	require.Zero(t, repo.calls)
}

func TestOpenAIAutoGroupSettingsLegacyDefaultsAndValidation(t *testing.T) {
	repo := &autoGroupSettingRepo{values: map[string]string{SettingKeyOpenAIAutoGroup: `{"enabled":true,"window_minutes":5,"failure_threshold":3}`}}
	settings := NewSettingService(repo, nil)
	p, err := settings.GetOpenAIAutoGroupSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, 20, p.FailureWindow)
	require.Equal(t, 10, p.LatencyWindow)
	require.False(t, p.LatencyEnabled)
	require.False(t, p.RecoveryEnabled)
	p.FailureThreshold = 21
	require.Error(t, validateOpenAIAutoGroupSettings(p))
	p.FailureThreshold = 3
	p.RecoveryFastStreak = 31
	require.Error(t, validateOpenAIAutoGroupSettings(p))
	p.RecoveryFastStreak = 10
	p.ProtectionMinutes = 0
	require.NoError(t, validateOpenAIAutoGroupSettings(p))
}

func TestOpenAIAutoGroupLateRetryCannotBypassRequestDedup(t *testing.T) {
	svc, _, counter := autoGroupTestService(t)
	account := &Account{ID: 20, Platform: PlatformOpenAI}
	ctx, _ := autoGroupTestRequest(3)
	svc.observeOpenAIAutoGroupFailure(ctx, account, 500, nil)
	require.Equal(t, 1, counter.calls)
	svc.observeOpenAIAutoGroupFailure(ctx, account, 429, nil)
	require.True(t, counter.history.Requests[0].Overload)
	// Even after the bounded shared dedup history has advanced, the live
	// request retains its observation and cannot become a fresh fast success.
	counter.history.Seen = nil
	before := counter.calls
	ms := 1
	svc.beginOpenAIAutoGroup(ctx, account)(&OpenAIForwardResult{Model: "model", Stream: true, FirstTokenMs: &ms}, nil)
	require.Equal(t, before+1, counter.calls)
	require.True(t, counter.history.Requests[0].Failed)
	require.True(t, counter.history.Requests[0].Overload)
	require.True(t, counter.history.Requests[0].Success)
}

func TestOpenAIAutoGroupSuccessfulRetryRetainsErrorsAndCumulativeLatency(t *testing.T) {
	svc, _, counter := autoGroupTestService(t)
	account := &Account{ID: 20, Platform: PlatformOpenAI}
	ctx, _ := autoGroupTestRequest(3)
	svc.observeOpenAIAutoGroupFailure(ctx, account, 429, nil)
	attempt := svc.openAIAutoGroupAttempt(ctx, account)
	attempt.prior = 11 * time.Second
	ms := 100
	svc.beginOpenAIAutoGroup(ctx, account)(&OpenAIForwardResult{Model: "model", Stream: true, FirstTokenMs: &ms}, nil)
	require.Len(t, counter.history.Requests, 1)
	sample := counter.history.Requests[0]
	require.True(t, sample.Overload)
	require.True(t, sample.Success)
	require.Greater(t, sample.FirstTokenMS, 10000)
	require.Len(t, counter.history.Cohorts[sample.Cohort], 1)
}

func TestOpenAIAutoGroupAllSlowCategoriesMustRecover(t *testing.T) {
	p := DefaultOpenAIAutoGroupSettings()
	p.LatencyEnabled = true
	p.RecoveryEnabled = true
	h := &OpenAIAutoGroupHistory{}
	for i := 0; i < 3; i++ {
		UpdateOpenAIAutoGroupHistory(h, groupSample(i, "a", 12000), p, false)
		UpdateOpenAIAutoGroupHistory(h, groupSample(i+3, "b", 12000), p, false)
	}
	now := time.Now()
	d := EvaluateOpenAIAutoGroup(OpenAIAutoGroupState{ProtectedUntil: now.Add(time.Minute).Format(time.RFC3339Nano)}, h, groupSample(10, "a", 1000), p, true, now)
	require.Empty(t, d.Action)
	d = EvaluateOpenAIAutoGroup(d.State, h, groupSample(11, "fast-other", 1000), p, true, now.Add(time.Hour))
	require.Equal(t, "down", d.Action)
	require.Equal(t, []string{"a", "b"}, d.State.RecoveryCohorts)
	low := d.State
	low.RestoreBindings = []OpenAIAutoGroupBinding{{ID: 7}}
	h = &OpenAIAutoGroupHistory{}
	for i := 0; i < 30; i++ {
		UpdateOpenAIAutoGroupHistory(h, groupSample(i, "a", 1000), p, true)
	}
	require.Empty(t, EvaluateOpenAIAutoGroup(low, h, groupSample(29, "a", 1000), p, true, now.Add(time.Hour)).Action)
	for i := 30; i < 60; i++ {
		UpdateOpenAIAutoGroupHistory(h, groupSample(i, "b", 1000), p, true)
	}
	require.Equal(t, "up", EvaluateOpenAIAutoGroup(low, h, groupSample(59, "b", 1000), p, true, now.Add(time.Hour)).Action)
	require.Empty(t, EvaluateOpenAIAutoGroup(low, h, OpenAIAutoGroupSample{ID: "missing", FirstTokenMS: -1}, p, true, now.Add(time.Hour)).Action)
}

func TestOpenAIAutoGroupCategoryOverflowRemainsBlocked(t *testing.T) {
	p := DefaultOpenAIAutoGroupSettings()
	p.RecoveryEnabled = true
	h := &OpenAIAutoGroupHistory{}
	for i := 0; i < 32; i++ {
		UpdateOpenAIAutoGroupHistory(h, groupSample(i, fmt.Sprint(i), 1000), p, true)
	}
	UpdateOpenAIAutoGroupHistory(h, groupSample(32, "omitted-slow", 30000), p, true)
	require.True(t, h.Overflow)
	for i := 33; i < 63; i++ {
		UpdateOpenAIAutoGroupHistory(h, groupSample(i, "0", 1000), p, true)
	}
	d := EvaluateOpenAIAutoGroup(OpenAIAutoGroupState{RestoreBindings: []OpenAIAutoGroupBinding{{ID: 7}}, RecoveryCohorts: []string{"0"}}, h, groupSample(62, "0", 1000), p, true, time.Now())
	require.Empty(t, d.Action)
	require.Equal(t, "failed", d.State.Status)
}

func TestOpenAIAutoGroupNestedForwardAndWSRetries(t *testing.T) {
	svc, _, counter := autoGroupTestService(t)
	account := &Account{ID: 20, Platform: PlatformOpenAI}
	ctx, _ := autoGroupTestRequest(3)
	outer := svc.beginOpenAIAutoGroup(ctx, account)
	inner := svc.beginOpenAIAutoGroup(ctx, account)
	ms := 100
	result := &OpenAIForwardResult{Model: "model", Stream: true, FirstTokenMs: &ms}
	inner(result, nil)
	require.Zero(t, counter.calls, "inner protocol conversion must not finalize the logical request")
	attempt := svc.openAIAutoGroupAttempt(ctx, account)
	attempt.wsPrior = 11 * time.Second
	cumulative := svc.openAIAutoGroupWSFirstToken(ctx, account, ms)
	result.autoGroupFirstTokenMS = &cumulative
	outer(result, nil)
	require.Equal(t, 1, counter.calls)
	require.Greater(t, counter.history.Requests[0].FirstTokenMS, 10000)
	require.Equal(t, 100, *result.FirstTokenMs, "display metric remains unchanged")
}

type autoGroupResumeRepo struct {
	AccountRepository
	current *Account
	calls   int
}

func (r *autoGroupResumeRepo) ResumeOpenAIAutoGroup(_ context.Context, _ int64) error {
	r.calls++
	return nil
}
func (r *autoGroupResumeRepo) GetByID(context.Context, int64) (*Account, error) {
	return r.current, nil
}
func TestOpenAIAutoGroupResumeBypassesGenericAccountEdit(t *testing.T) {
	repo := &autoGroupResumeRepo{current: &Account{ID: 7, Platform: PlatformOpenAI, Extra: map[string]any{"quota_daily_limit": 50, "custom": "keep"}}}
	admin := &adminServiceImpl{accountRepo: repo}
	updated, err := admin.UpdateAccount(context.Background(), 7, &UpdateAccountInput{Extra: map[string]any{"openai_auto_group_resume": true}})
	require.NoError(t, err)
	require.Equal(t, 1, repo.calls)
	require.Equal(t, 50, updated.Extra["quota_daily_limit"])
	require.Equal(t, "keep", updated.Extra["custom"])
	_, err = admin.UpdateAccount(context.Background(), 7, &UpdateAccountInput{Name: "other edit", Extra: map[string]any{"openai_auto_group_resume": true}})
	require.Error(t, err)
	require.Equal(t, 1, repo.calls)
}

func TestOpenAIAutoGroupTransportFailuresResetRecovery(t *testing.T) {
	for _, timeout := range []bool{false, true} {
		t.Run(fmt.Sprint(timeout), func(t *testing.T) {
			svc, repo, counter := autoGroupTestService(t)
			p, _ := svc.settingService.GetOpenAIAutoGroupSettings(context.Background())
			p.RecoveryEnabled = true
			require.NoError(t, svc.settingService.SetOpenAIAutoGroupSettings(context.Background(), p))
			state := OpenAIAutoGroupState{Generation: "low", RestoreBindings: []OpenAIAutoGroupBinding{{ID: 9}}, RecoveryCohorts: []string{"model"}}
			repo.current = &Account{ID: 20, Platform: PlatformOpenAI, GroupIDs: []int64{7}, Extra: map[string]any{OpenAIAutoGroupStateExtraKey: state}}
			counter.history = OpenAIAutoGroupHistory{Version: p.Revision + ":low", Cohorts: map[string][]OpenAIAutoGroupSample{"model": {groupSample(1, "model", 1000)}}}
			ctx, ginCtx := autoGroupTestRequest(2)
			if timeout {
				_ = svc.newOpenAIFirstOutputTimeoutError(ctx, ginCtx, repo.current, nil, "", time.Now(), "model", "", time.Second, "headers", nil)
			} else {
				_ = svc.handleOpenAIUpstreamTransportError(ctx, ginCtx, repo.current, errors.New("connection reset by peer"), false)
			}
			require.Empty(t, counter.history.Cohorts)
			require.Equal(t, 1, counter.calls)
			require.True(t, counter.history.Requests[0].Failed)
			require.False(t, counter.history.Requests[0].Overload)
		})
	}
}
