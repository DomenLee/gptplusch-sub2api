package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type autoGroupSettingRepo struct {
	SettingRepository
	values  map[string]string
	readErr error
}

func (repo *autoGroupSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	if repo.readErr != nil {
		return "", repo.readErr
	}
	value, exists := repo.values[key]
	if !exists {
		return "", ErrSettingNotFound
	}
	return value, nil
}

func (repo *autoGroupSettingRepo) Set(_ context.Context, key, value string) error {
	if repo.values == nil {
		repo.values = make(map[string]string)
	}
	repo.values[key] = value
	return nil
}

type autoGroupRepo struct {
	AccountRepository
	calls   int
	state   OpenAIAutoGroupState
	err     error
	current *Account
	created *Account
	bound   []int64
}

func (repo *autoGroupRepo) DowngradeOpenAIAccountGroups(_ context.Context, _, _ int64, state OpenAIAutoGroupState) (bool, error) {
	repo.calls++
	repo.state = state
	return repo.err == nil, repo.err
}

func (repo *autoGroupRepo) UpdateExtra(_ context.Context, _ int64, updates map[string]any) error {
	state, ok := updates[OpenAIAutoGroupStateExtraKey].(OpenAIAutoGroupState)
	if !ok {
		return fmt.Errorf("invalid GPT auto-group state")
	}
	repo.state = state
	return nil
}

func (repo *autoGroupRepo) GetByID(context.Context, int64) (*Account, error) {
	return repo.current, nil
}

func (repo *autoGroupRepo) Create(_ context.Context, account *Account) error {
	account.ID = 99
	repo.created = account
	return nil
}

func (repo *autoGroupRepo) BindGroups(_ context.Context, _ int64, ids []int64) error {
	repo.bound = append([]int64(nil), ids...)
	return nil
}

type autoGroupGroups struct {
	GroupRepository
	groups []Group
}

func (repo *autoGroupGroups) ListActiveByPlatform(context.Context, string) ([]Group, error) {
	return repo.groups, nil
}

type autoGroupCounter struct {
	TempUnschedCache
	seen  map[string]bool
	calls int
	err   error
}

func (counter *autoGroupCounter) RecordOpenAIAutoGroupFailure(_ context.Context, accountID int64, revision, requestID string, _, threshold int) (int64, bool, error) {
	if counter.err != nil {
		return 0, false, counter.err
	}
	key := fmt.Sprintf("%d:%s:%s", accountID, revision, requestID)
	if counter.seen[key] {
		return int64(counter.calls), false, nil
	}
	counter.seen[key] = true
	counter.calls++
	return int64(counter.calls), counter.calls >= threshold, nil
}

func autoGroupTestService(t *testing.T) (*OpenAIGatewayService, *autoGroupRepo, *autoGroupCounter) {
	t.Helper()
	settings := NewSettingService(&autoGroupSettingRepo{}, nil)
	require.NoError(t, settings.SetOpenAIAutoGroupSettings(context.Background(), &OpenAIAutoGroupSettings{Enabled: true, WindowMinutes: 5, FailureThreshold: 3}))
	repo := &autoGroupRepo{}
	counter := &autoGroupCounter{seen: make(map[string]bool)}
	return &OpenAIGatewayService{accountRepo: repo, settingService: settings, rateLimitService: &RateLimitService{tempUnschedCache: counter}}, repo, counter
}

func autoGroupTestRequest(rate float64) (context.Context, *gin.Context) {
	ctx := context.WithValue(context.Background(), ctxkey.Group, &Group{ID: 7, Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: rate})
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
	return withOpenAIAutoGroupRequest(ctx, ginCtx), ginCtx
}

func TestOpenAIAutoGroupSettings(t *testing.T) {
	repo := &autoGroupSettingRepo{}
	settings := NewSettingService(repo, nil)
	policy, err := settings.GetOpenAIAutoGroupSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, DefaultOpenAIAutoGroupSettings(), policy)
	policy.Enabled = true
	require.NoError(t, settings.SetOpenAIAutoGroupSettings(context.Background(), policy))
	firstRevision := policy.Revision
	require.NotEmpty(t, firstRevision)
	loaded, err := settings.openAIAutoGroupRuntimeSettings(context.Background())
	require.NoError(t, err)
	require.Equal(t, *policy, *loaded)
	require.NoError(t, settings.SetOpenAIAutoGroupSettings(context.Background(), policy))
	require.NotEqual(t, firstRevision, policy.Revision)
	for _, invalid := range []OpenAIAutoGroupSettings{{WindowMinutes: 0, FailureThreshold: 3}, {WindowMinutes: 61, FailureThreshold: 3}, {WindowMinutes: 5, FailureThreshold: 0}, {WindowMinutes: 5, FailureThreshold: 1001}} {
		require.Error(t, settings.SetOpenAIAutoGroupSettings(context.Background(), &invalid))
	}
	repo.readErr = errors.New("settings unavailable")
	_, err = settings.GetOpenAIAutoGroupSettings(context.Background())
	require.Error(t, err)
}

func TestOpenAIAutoGroupCreateDefaultsByMultiplier(t *testing.T) {
	settings := NewSettingService(&autoGroupSettingRepo{}, nil)
	require.NoError(t, settings.SetOpenAIAutoGroupSettings(context.Background(), &OpenAIAutoGroupSettings{DefaultHighRate: true, WindowMinutes: 5, FailureThreshold: 3}))
	groups := &autoGroupGroups{groups: []Group{
		{ID: 1, Name: "自用", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 3},
		{ID: 2, Name: "不降智", Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 1.5},
		{ID: 3, Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 2},
		{ID: 4, Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 2.5, AutoRouteEnabled: true},
		{ID: 5, Platform: PlatformOpenAI, Status: StatusActive, RateMultiplier: 2.5, RequireOAuthOnly: true},
		{ID: 6, Platform: PlatformOpenAI, Status: "inactive", RateMultiplier: 3},
		{ID: 7, Platform: PlatformAnthropic, Status: StatusActive, RateMultiplier: 3},
	}}
	ids, applied, err := openAIDefaultHighRateGroups(context.Background(), settings, groups, PlatformOpenAI, AccountTypeAPIKey)
	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, []int64{1}, ids)
	ids, _, err = openAIDefaultHighRateGroups(context.Background(), settings, groups, PlatformOpenAI, AccountTypeOAuth)
	require.NoError(t, err)
	require.Equal(t, []int64{1, 5}, ids)
	accountRepo := &autoGroupRepo{}
	admin := &adminServiceImpl{accountRepo: accountRepo, groupRepo: groups, settingService: settings}
	account, err := admin.CreateAccount(context.Background(), &CreateAccountInput{Name: "new", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test"}, SkipMixedChannelCheck: true})
	require.NoError(t, err)
	require.Equal(t, []int64{1}, accountRepo.bound)
	require.Equal(t, []int64{1}, account.GroupIDs)
	_, err = admin.CreateAccount(context.Background(), &CreateAccountInput{Name: "manual", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"api_key": "test"}, GroupIDs: []int64{3}, SkipMixedChannelCheck: true})
	require.NoError(t, err)
	require.Equal(t, []int64{3}, accountRepo.bound)
	groups.groups = nil
	_, _, err = openAIDefaultHighRateGroups(context.Background(), settings, groups, PlatformOpenAI, AccountTypeAPIKey)
	require.ErrorContains(t, err, "no compatible active groups")
}

func TestOpenAIAutoGroupCountsDistinctRequestsAcrossHTTPAndSSE(t *testing.T) {
	svc, repo, counter := autoGroupTestService(t)
	account := &Account{ID: 20, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	body := []byte(`{"error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`)
	for index := 0; index < 3; index++ {
		ctx, ginCtx := autoGroupTestRequest(3)
		svc.handleOpenAIAccountUpstreamError(ctx, account, 503, nil, body)
		svc.handleOpenAIStreamTerminalAccountSideEffects(ginCtx, account, body, "overloaded", nil)
	}
	require.Equal(t, 3, counter.calls)
	require.Equal(t, 1, repo.calls)
	require.Equal(t, int64(3), repo.state.FailureCount)
	require.Equal(t, "downgraded", repo.state.Status)
}

func TestOpenAIAutoGroupSkipsNonTargetFailures(t *testing.T) {
	svc, repo, counter := autoGroupTestService(t)
	account := &Account{ID: 20, Platform: PlatformOpenAI}
	ctx, _ := autoGroupTestRequest(3)
	for _, status := range []int{400, 401, 403, 500, 502, 503} {
		svc.observeOpenAIAutoGroupFailure(ctx, account, status, []byte(`{"error":{"message":"generic processing error"}}`))
	}
	lowCtx, _ := autoGroupTestRequest(2)
	svc.observeOpenAIAutoGroupFailure(lowCtx, account, 429, nil)
	svc.observeOpenAIAutoGroupFailure(context.Background(), account, 429, nil)
	other := *account
	other.Platform = PlatformAnthropic
	svc.observeOpenAIAutoGroupFailure(ctx, &other, 429, nil)
	require.Zero(t, counter.calls)
	require.Zero(t, repo.calls)
	require.NoError(t, svc.settingService.SetOpenAIAutoGroupSettings(context.Background(), DefaultOpenAIAutoGroupSettings()))
	svc.observeOpenAIAutoGroupFailure(ctx, account, 429, nil)
	require.Zero(t, counter.calls)
}

func TestOpenAIAutoGroupFailureIsPersisted(t *testing.T) {
	svc, repo, _ := autoGroupTestService(t)
	repo.err = errors.New("no compatible groups at or below 2x")
	account := &Account{ID: 20, Platform: PlatformOpenAI}
	for index := 0; index < 3; index++ {
		ctx, _ := autoGroupTestRequest(3)
		svc.observeOpenAIAutoGroupFailure(ctx, account, 429, nil)
	}
	require.Equal(t, "failed", repo.state.Status)
	require.Contains(t, repo.state.Message, "no compatible groups")
	svc, repo, counter := autoGroupTestService(t)
	counter.err = errors.New("Redis unavailable")
	ctx, _ := autoGroupTestRequest(3)
	svc.observeOpenAIAutoGroupFailure(ctx, account, 429, nil)
	require.Zero(t, repo.calls)
	require.Equal(t, "failed", repo.state.Status)
	require.Contains(t, repo.state.Message, "check Redis")
}

func TestOpenAIAutoGroupWebSocketTurnsAndMembership(t *testing.T) {
	svc, repo, counter := autoGroupTestService(t)
	account := &Account{ID: 20, Platform: PlatformOpenAI}
	ctx, ginCtx := autoGroupTestRequest(3)
	for turn := 1; turn <= 3; turn++ {
		BeginOpsStreamTurn(ginCtx, turn)
		svc.observeOpenAIAutoGroupFailure(ctx, account, 429, nil)
		svc.observeOpenAIAutoGroupFailure(ctx, account, 429, nil)
	}
	require.Equal(t, 3, counter.calls)
	repo.current = &Account{ID: 20, GroupIDs: []int64{7}}
	require.Error(t, svc.checkOpenAIAutoGroupMembership(ctx, account))
	ctx, _ = autoGroupTestRequest(3)
	repo.current = &Account{ID: 20, GroupIDs: []int64{1}}
	var failover *UpstreamFailoverError
	require.ErrorAs(t, svc.checkOpenAIAutoGroupMembership(ctx, account), &failover)
	require.False(t, failover.RetryableOnSameAccount)
	repo.current.GroupIDs = []int64{7}
	require.NoError(t, svc.checkOpenAIAutoGroupMembership(ctx, account))
	hookCalls := 0
	hooks := svc.openAIAutoGroupWSHooks(ctx, account, &OpenAIWSIngressHooks{BeforeTurn: func(int) error {
		hookCalls++
		return nil
	}})
	require.NoError(t, hooks.BeforeTurn(4))
	require.Equal(t, 1, hookCalls)
	repo.current.GroupIDs = []int64{1}
	require.Error(t, hooks.BeforeTurn(5))
	require.Equal(t, 1, hookCalls)
}
