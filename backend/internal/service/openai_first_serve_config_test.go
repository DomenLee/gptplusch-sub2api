package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFirstServeCustomTimings(t *testing.T) {
	now := time.Now()
	a := &Account{ID: 98704, Extra: map[string]any{"openai_first_serve": map[string]any{
		"ttl_minutes": 2, "ttft_seconds": 4, "max_switches": 2, "cooldown_seconds": 10,
	}}}
	s := newOpenAIFirstServeState(a, "conn", now)
	s.observe(4000, now.Add(time.Minute))
	require.False(t, s.due(now.Add(time.Minute)))
	require.Equal(t, now.Add(2*time.Minute), s.status.ExpiresAt)
	require.True(t, s.due(now.Add(2*time.Minute)))
	s.bind(nil, "new", now)
	s.observe(4001, now)
	require.True(t, s.due(now))
	s.attempts = 2
	require.False(t, s.due(now))
	require.False(t, s.due(now.Add(9*time.Second)))
	require.True(t, s.due(now.Add(10*time.Second)))
	s.observe(3000, now)
	require.Zero(t, s.attempts)
}

func TestFirstServeConfigValidation(t *testing.T) {
	for name, raw := range map[string]any{
		"object": "bad", "null": nil,
		"zero":               map[string]any{"ttl_minutes": 0},
		"invalid_scope":      map[string]any{"reuse_scope": "global"},
		"negative":           map[string]any{"max_switches": -1},
		"large":              map[string]any{"cooldown_seconds": 3601},
		"decimal":            map[string]any{"ttft_seconds": 1.5},
		"string":             map[string]any{"ttft_seconds": "15"},
		"field_null":         map[string]any{"ttft_seconds": nil},
		"typo":               map[string]any{"ttl_minute": 3},
		"empty_selected":     map[string]any{"proxy_mode": "selected", "proxy_ids": []int64{}},
		"duplicate_selected": map[string]any{"proxy_mode": "selected", "proxy_ids": []int64{1, 1}},
		"invalid_id":         map[string]any{"proxy_mode": "selected", "proxy_ids": []int64{0, 1}},
		"ambiguous_all":      map[string]any{"proxy_mode": "all", "proxy_ids": []int64{1, 2}},
	} {
		t.Run(name, func(t *testing.T) {
			a := &Account{Name: "account A", Extra: map[string]any{"openai_first_serve": raw}}
			_, err := a.firstServeConfig()
			require.ErrorContains(t, err, "account A")
		})
	}
	cfg, err := (&Account{}).firstServeConfig()
	require.NoError(t, err)
	require.Equal(t, defaultOpenAIFirstServeConfig(), cfg)
	changed := cfg
	changed.TTFTSeconds = 3
	require.NotEqual(t, cfg.key(1), changed.key(1), "new settings isolate old connection state")
	require.NotEqual(t, cfg.key(1), cfg.key(2), "changing groups isolates old connections")
}

func TestFirstServeConfigDefaultsToAccountSharing(t *testing.T) {
	for _, tc := range []struct {
		extra map[string]any
		want  string
	}{
		{want: "account"},
		{extra: map[string]any{"openai_first_serve": map[string]any{"ttl_minutes": 12}}, want: "account"},
		{extra: map[string]any{"openai_first_serve": map[string]any{"reuse_scope": "session"}}, want: "session"},
	} {
		cfg, err := (&Account{Extra: tc.extra}).firstServeConfig()
		require.NoError(t, err)
		require.Equal(t, tc.want, cfg.ReuseScope)
	}
}

type firstServeConfigRepo struct {
	proxyGroupServiceRepoStub
	groupID, previousID int64
	allowed             []int64
}

func (r *firstServeConfigRepo) SelectAvailableProxyExcluding(_ context.Context, groupID, previousID int64, allowed []int64) (*Proxy, error) {
	r.groupID, r.previousID, r.allowed = groupID, previousID, allowed
	return r.proxy, r.err
}

func (r *firstServeConfigRepo) GetByID(context.Context, int64) (*ProxyGroup, error) {
	return &ProxyGroup{ID: 10, ProxyIDs: []int64{1, 2, 3}}, nil
}

func TestFirstServeProxyAllowlist(t *testing.T) {
	defaultProxyGroupResolver.RLock()
	old := defaultProxyGroupResolver.resolver
	defaultProxyGroupResolver.RUnlock()
	defer SetDefaultProxyGroupResolver(old)
	repo := &firstServeConfigRepo{proxyGroupServiceRepoStub: proxyGroupServiceRepoStub{proxy: &Proxy{ID: 2, Host: "b", Port: 8080}}}
	NewProxyGroupService(repo)
	groupID := int64(10)
	a := &Account{Name: "account A", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, ProxyGroupID: &groupID, Extra: map[string]any{
		"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModeFirstServe,
		"openai_first_serve":                         map[string]any{"proxy_mode": "selected", "proxy_ids": []int64{2, 1}},
	}}
	require.NoError(t, validateOpenAIFirstServeProxies(context.Background(), a))
	for _, previous := range []int64{0, 1} {
		proxy, err := selectOpenAIFirstServeProxy(context.Background(), a, previous)
		require.NoError(t, err)
		require.Equal(t, int64(2), proxy.ID)
		require.Equal(t, groupID, repo.groupID)
		require.Equal(t, previous, repo.previousID)
		require.Equal(t, []int64{1, 2}, repo.allowed, "initial selection and rotation share the allowlist")
	}
	repo.proxy.ID = 3
	_, err := selectOpenAIFirstServeProxy(context.Background(), a, 0)
	require.ErrorIs(t, err, ErrProxyGroupNoProxy, "a resolver cannot escape the allowlist")
	a.Extra["openai_first_serve"] = map[string]any{"proxy_mode": "selected", "proxy_ids": []int64{2, 99}}
	require.ErrorContains(t, validateOpenAIFirstServeProxies(context.Background(), a), "#99")
	a.Extra["openai_first_serve"] = map[string]any{"proxy_mode": "all"}
	_, err = selectOpenAIFirstServeProxy(context.Background(), a, 0)
	require.NoError(t, err)
	require.Nil(t, repo.allowed, "all-mode uses NULL, not an empty SQL array that matches no proxies")
}
