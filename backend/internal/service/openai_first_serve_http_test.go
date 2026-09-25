package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func firstServeHTTPAccount(t *testing.T) *Account {
	t.Helper()
	defaultProxyGroupResolver.RLock()
	previous := defaultProxyGroupResolver.resolver
	defaultProxyGroupResolver.RUnlock()
	SetDefaultProxyGroupResolver(firstServeTestResolver{
		first: &Proxy{ID: 101, Name: "A", Protocol: "http", Host: "first.test", Port: 8080},
		next:  &Proxy{ID: 102, Name: "B", Protocol: "http", Host: "next.test", Port: 8080},
	})
	t.Cleanup(func() { SetDefaultProxyGroupResolver(previous) })
	group := int64(9)
	return &Account{ID: 98710, Name: "HTTP test", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		ProxyGroupID: &group, Concurrency: 1, Credentials: map[string]any{"api_key": "test-key"},
		Extra: map[string]any{"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModeFirstServe},
	}
}

func firstServeHTTPContext(keyID int64, session string) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	group := int64(1)
	c.Set("api_key", &APIKey{ID: keyID, GroupID: &group})
	if session != "" {
		c.Request.Header.Set("session_id", session)
	}
	SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
	return c
}

func TestFirstServeHTTPReuseRotationAndIsolation(t *testing.T) {
	a := firstServeHTTPAccount(t)
	svc := &OpenAIGatewayService{}
	body := []byte(`{"model":"gpt-5","stream":true,"input":"hi"}`)
	ctx := context.Background()
	_, copyA, first, err := svc.prepareFirstServeHTTP(ctx, firstServeHTTPContext(1, "session"), a, body)
	require.NoError(t, err)
	require.NotSame(t, a, copyA)
	require.Nil(t, a.Proxy, "never mutate scheduler's account")
	expiry := first.entry.state.status.ExpiresAt
	ms := 15000
	first.finish(&OpenAIForwardResult{FirstTokenMs: &ms}, nil)
	_, copyB, second, err := svc.prepareFirstServeHTTP(ctx, firstServeHTTPContext(1, "session"), a, body)
	require.NoError(t, err)
	require.Equal(t, first.id, second.id)
	require.Equal(t, copyA.ProxyID, copyB.ProxyID)
	require.Equal(t, expiry, second.entry.state.status.ExpiresAt, "healthy requests do not renew TTL")
	ms = 15001
	second.finish(&OpenAIForwardResult{FirstTokenMs: &ms}, nil)
	_, copyC, third, err := svc.prepareFirstServeHTTP(ctx, firstServeHTTPContext(1, "session"), a, body)
	require.NoError(t, err)
	require.NotEqual(t, first.id, third.id)
	require.NotEqual(t, *copyA.ProxyID, *copyC.ProxyID)
	require.Equal(t, 1, third.entry.state.status.Rotations)
	third.finish(nil, nil)
	for _, c := range []*gin.Context{firstServeHTTPContext(2, "session"), firstServeHTTPContext(1, "other"), firstServeHTTPContext(1, "")} {
		_, _, isolated, prepareErr := svc.prepareFirstServeHTTP(ctx, c, a, body)
		require.NoError(t, prepareErr)
		require.NotEqual(t, third.id, isolated.id)
		isolated.finish(nil, nil)
	}
	_, _, missing, err := svc.prepareFirstServeHTTP(ctx, firstServeHTTPContext(1, ""), a, body)
	require.NoError(t, err)
	require.True(t, missing.entry.state.status.SessionMissing)
	missing.finish(nil, nil)
	require.NotContains(t, svc.openaiFirstServeHTTP.items, missing.key)
}

func TestFirstServeHTTPExpiryContinuationAndOldResponse(t *testing.T) {
	a := firstServeHTTPAccount(t)
	svc := &OpenAIGatewayService{}
	c := firstServeHTTPContext(1, "session")
	ctx, _, old, err := svc.prepareFirstServeHTTP(context.Background(), c, a, []byte(`{}`))
	require.NoError(t, err)
	old.entry.state.status.ExpiresAt = time.Now().Add(-time.Second)
	_, _, continuation, err := svc.prepareFirstServeHTTP(context.Background(), c, a, []byte(`{"previous_response_id":"resp_previous","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`))
	require.NoError(t, err)
	require.Equal(t, old.id, continuation.id)
	require.Equal(t, "context_incomplete", continuation.entry.state.status.Reason)
	continuation.finish(nil, nil)
	_, _, fresh, err := svc.prepareFirstServeHTTP(context.Background(), c, a, []byte(`{"input":"full history"}`))
	require.NoError(t, err)
	require.NotEqual(t, old.id, fresh.id)
	observeFirstServeHTTP(ctx, 20000)
	ms := 20000
	old.finish(&OpenAIForwardResult{FirstTokenMs: &ms}, nil)
	require.Nil(t, fresh.entry.state.status.FirstTokenMs, "old in-flight response cannot poison the current generation")
	require.False(t, fresh.entry.state.pending)
	fresh.finish(nil, nil)
}

func TestFirstServeHTTPUnavailableProxyAndCooldown(t *testing.T) {
	a := firstServeHTTPAccount(t)
	SetDefaultProxyGroupResolver(firstServeTestResolver{first: &Proxy{ID: 101, Host: "first.test", Port: 8080}})
	svc := &OpenAIGatewayService{}
	c := firstServeHTTPContext(1, "session")
	ctx, _, first, err := svc.prepareFirstServeHTTP(context.Background(), c, a, []byte(`{}`))
	require.NoError(t, err)
	observeFirstServeHTTP(ctx, 16000)
	first.finish(nil, nil)
	_, _, second, err := svc.prepareFirstServeHTTP(context.Background(), c, a, []byte(`{}`))
	require.NoError(t, err)
	require.Equal(t, first.id, second.id)
	require.Equal(t, "proxy_unavailable", second.entry.state.status.Reason)
	require.True(t, second.entry.state.retryAt.After(time.Now()))
	second.finish(nil, nil)
	second.entry.state.retryAt = time.Time{}
	second.entry.state.attempts = second.entry.state.status.Config.MaxSwitches
	_, _, third, err := svc.prepareFirstServeHTTP(context.Background(), c, a, []byte(`{}`))
	require.NoError(t, err)
	require.Equal(t, first.id, third.id)
	require.Equal(t, "cooldown", third.entry.state.status.Reason)
	third.finish(nil, nil)
}

func TestFirstServeHTTPConcurrentRequests(t *testing.T) {
	a := firstServeHTTPAccount(t)
	svc := &OpenAIGatewayService{}
	var wg sync.WaitGroup
	ids := make(chan string, 16)
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, _, lease, err := svc.prepareFirstServeHTTP(context.Background(), firstServeHTTPContext(1, "shared"), a, []byte(`{}`))
			if err != nil {
				t.Error(err)
				return
			}
			ids <- lease.id
			observeFirstServeHTTP(ctx, 1)
			lease.finish(nil, nil)
		}()
	}
	wg.Wait()
	close(ids)
	unique := map[string]bool{}
	for id := range ids {
		unique[id] = true
	}
	require.Len(t, unique, 1)
}

func TestFirstServeHTTPSessionMetadataAndConfigChange(t *testing.T) {
	a := firstServeHTTPAccount(t)
	svc := &OpenAIGatewayService{}
	for _, body := range []string{
		`{"client_metadata":{"session_id":"body-session"}}`,
		`{"metadata":{"user_id":"{\"device_id\":\"device\",\"account_uuid\":\"account\",\"session_id\":\"claude-session\"}"}}`,
	} {
		c := firstServeHTTPContext(1, "")
		_, _, first, err := svc.prepareFirstServeHTTP(context.Background(), c, a, []byte(body))
		require.NoError(t, err)
		require.False(t, first.missing)
		first.finish(nil, nil)
		_, _, second, err := svc.prepareFirstServeHTTP(context.Background(), c, a, []byte(body))
		require.NoError(t, err)
		require.Equal(t, first.id, second.id)
		second.finish(nil, nil)
	}
	c := firstServeHTTPContext(1, "session")
	_, _, first, err := svc.prepareFirstServeHTTP(context.Background(), c, a, []byte(`{}`))
	require.NoError(t, err)
	first.finish(nil, nil)
	a.Extra["openai_first_serve"] = map[string]any{"ttl_minutes": 12, "proxy_mode": "selected", "proxy_ids": []int64{101, 102}}
	_, _, second, err := svc.prepareFirstServeHTTP(context.Background(), c, a, []byte(`{}`))
	require.NoError(t, err)
	require.NotEqual(t, first.id, second.id)
	require.Equal(t, 12, second.entry.state.status.Config.TTLMinutes)
	second.finish(nil, nil)
	// Initial selection must also obey the whitelist; never silently fall back
	// to the group's default proxy if it is outside the permitted range.
	a.Extra["openai_first_serve"] = map[string]any{"proxy_mode": "selected", "proxy_ids": []int64{103, 104}}
	_, _, lease, err := svc.prepareFirstServeHTTP(context.Background(), c, a, []byte(`{}`))
	require.Error(t, err)
	require.Nil(t, lease)
}

func TestFirstServeHTTPRegistryBounded(t *testing.T) {
	now := time.Now()
	r := &openAIFirstServeHTTPRegistry{items: make(map[string]*openAIFirstServeHTTPEntry)}
	for i := range openAIFirstServeHTTPLimit {
		r.items[string(rune(i+1))] = &openAIFirstServeHTTPEntry{refs: 1, lastUsed: now}
	}
	_, err := r.acquire("new", now)
	require.Error(t, err, "in-flight entries must not be evicted")
	r.items["\x01"].refs = 0
	_, err = r.acquire("new", now)
	require.NoError(t, err)
	require.Len(t, r.items, openAIFirstServeHTTPLimit)
	require.NotContains(t, r.items, "\x01")
}

type firstServeDelayedReader struct {
	io.Reader
	delay time.Duration
}

func (r *firstServeDelayedReader) Read(p []byte) (int, error) {
	if r.delay > 0 {
		time.Sleep(r.delay)
		r.delay = 0
	}
	return r.Reader.Read(p)
}

type firstServeHTTPUpstream struct {
	HTTPUpstream
	headers []http.Header
	bodies  [][]byte
	proxies []string
	delay   time.Duration
	raw     bool
}

func (u *firstServeHTTPUpstream) Do(req *http.Request, proxy string, _ int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	u.headers = append(u.headers, req.Header.Clone())
	u.bodies = append(u.bodies, body)
	u.proxies = append(u.proxies, proxy)
	preamble := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_test\",\"status\":\"in_progress\"}}\n\n"
	output := "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\",\"output_index\":0,\"content_index\":0}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_test\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"hello\"}]}],\"usage\":{\"input_tokens\":2,\"output_tokens\":1}}}\n\ndata: [DONE]\n\n"
	if u.raw {
		preamble = ""
		output = "data: {\"id\":\"chat_test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
	}
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
		Body: io.NopCloser(io.MultiReader(strings.NewReader(preamble), &firstServeDelayedReader{Reader: strings.NewReader(output), delay: u.delay})),
	}, nil
}

func TestFirstServeHTTPForwardStreaming(t *testing.T) {
	for _, path := range []string{"responses", "passthrough", "oauth", "oauth_passthrough", "messages", "chat", "chat_raw"} {
		t.Run(path, func(t *testing.T) {
			a := firstServeHTTPAccount(t)
			a.Extra["openai_first_serve"] = map[string]any{"ttft_seconds": 1}
			if strings.Contains(path, "passthrough") {
				a.Extra["openai_passthrough"] = true
			}
			if strings.HasPrefix(path, "oauth") {
				a.Type = AccountTypeOAuth
				a.Credentials = map[string]any{"access_token": "test-token", "chatgpt_account_id": "account"}
				a.Extra["openai_oauth_responses_websockets_v2_mode"] = OpenAIWSIngressModeFirstServe
			}
			if path == "chat_raw" {
				a.Extra["openai_responses_supported"] = false
			}
			upstream := &firstServeHTTPUpstream{raw: path == "chat_raw"}
			svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
			body := []byte(`{"model":"gpt-5.4","stream":true,"instructions":"test","input":"hello"}`)
			if path == "messages" || strings.HasPrefix(path, "chat") {
				body = []byte(`{"model":"gpt-5.4","stream":true,"messages":[{"role":"user","content":"hello"}],"max_tokens":32}`)
			}
			original := bytes.Clone(body)
			for turn := range 3 {
				upstream.delay = 0
				if turn == 1 {
					upstream.delay = 1100 * time.Millisecond
				}
				c := firstServeHTTPContext(1, "session")
				var result *OpenAIForwardResult
				var err error
				switch path {
				case "messages":
					result, err = svc.ForwardAsAnthropic(context.Background(), c, a, body, "", "")
				case "chat", "chat_raw":
					result, err = svc.ForwardAsChatCompletions(context.Background(), c, a, body, "", "")
				default:
					result, err = svc.Forward(context.Background(), c, a, body)
				}
				require.NoError(t, err)
				require.NotNil(t, result.FirstTokenMs)
				if turn == 1 {
					require.Greater(t, *result.FirstTokenMs, 1000, "metadata must not hide a slow first token")
				}
				require.True(t, result.Stream)
				require.False(t, result.OpenAIWSMode)
			}
			require.Equal(t, original, body)
			require.Equal(t, upstream.proxies[0], upstream.proxies[1])
			require.NotEqual(t, upstream.proxies[1], upstream.proxies[2])
			require.Equal(t, upstream.headers[0].Get("session_id"), upstream.headers[1].Get("session_id"))
			require.NotEqual(t, upstream.headers[1].Get("session_id"), upstream.headers[2].Get("session_id"))
			if !upstream.raw {
				for i, b := range upstream.bodies {
					require.Equal(t, upstream.headers[i].Get("session_id"), gjson.GetBytes(b, "prompt_cache_key").String())
				}
			}
			require.Nil(t, a.Proxy)
		})
	}
}

func TestFirstServeHTTPRequestPreservesContinuationAndNonTarget(t *testing.T) {
	a := firstServeHTTPAccount(t)
	svc := &OpenAIGatewayService{}
	body := []byte(`{"previous_response_id":"resp_old","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}],"client_metadata":{"session_id":"old","x-codex-turn-metadata":"{\"session_id\":\"old\",\"thread_id\":\"thread\"}"}}`)
	ctx, _, lease, err := svc.prepareFirstServeHTTP(context.Background(), firstServeHTTPContext(1, "session"), a, body)
	require.NoError(t, err)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/responses", bytes.NewReader(body))
	require.NoError(t, err)
	require.NoError(t, applyFirstServeHTTPRequest(req, a))
	rewritten, err := io.ReadAll(req.Body)
	require.NoError(t, err)
	_ = req.Body.Close()
	require.Equal(t, "resp_old", gjson.GetBytes(rewritten, "previous_response_id").String())
	require.JSONEq(t, gjson.GetBytes(body, "input").Raw, gjson.GetBytes(rewritten, "input").Raw)
	require.Equal(t, lease.id, gjson.GetBytes(rewritten, "client_metadata.session_id").String())
	embedded := gjson.GetBytes(rewritten, "client_metadata.x-codex-turn-metadata").String()
	require.Equal(t, lease.id, gjson.Get(embedded, "session_id").String())
	require.Equal(t, "thread", gjson.Get(embedded, "thread_id").String())
	lease.finish(nil, errors.New("test failure"))
	require.Equal(t, "connection_failed", lease.entry.state.status.Reason)
	a.Extra["openai_apikey_responses_websockets_v2_mode"] = OpenAIWSIngressModeCtxPool
	_, same, off, err := svc.prepareFirstServeHTTP(context.Background(), firstServeHTTPContext(1, "session"), a, body)
	require.NoError(t, err)
	require.Same(t, a, same)
	require.Nil(t, off)
}
