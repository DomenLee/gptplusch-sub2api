package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAIFirstServeHTTPLimit = 4096

// HTTP keeps routing identities only, never prompts or response history. Each
// entry has its own lock: neither proxy lookup nor a running SSE blocks others.
type openAIFirstServeHTTPRegistry struct {
	mu    sync.Mutex
	items map[string]*openAIFirstServeHTTPEntry
}

type openAIFirstServeHTTPEntry struct {
	mu       sync.Mutex
	state    *openAIFirstServeState
	refs     int       // protected by registry.mu
	lastUsed time.Time // protected by registry.mu
}

type openAIFirstServeHTTPKey struct{}

type openAIFirstServeHTTPLease struct {
	registry  *openAIFirstServeHTTPRegistry
	entry     *openAIFirstServeHTTPEntry
	key       string
	id        string
	accountID int64
	missing   bool
	fresh     bool
	observed  bool // protected by entry.mu
}

func firstServeHTTPLease(ctx context.Context) *openAIFirstServeHTTPLease {
	l, _ := ctx.Value(openAIFirstServeHTTPKey{}).(*openAIFirstServeHTTPLease)
	return l
}

func (r *openAIFirstServeHTTPRegistry) acquire(key string, now time.Time) (*openAIFirstServeHTTPEntry, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.items == nil {
		r.items = make(map[string]*openAIFirstServeHTTPEntry)
	}
	entry := r.items[key]
	if entry == nil {
		oldestKey := ""
		oldest := now
		for k, item := range r.items {
			if item.refs == 0 && now.Sub(item.lastUsed) > 24*time.Hour {
				delete(r.items, k)
				continue
			}
			if item.refs == 0 && !item.lastUsed.After(oldest) {
				oldestKey, oldest = k, item.lastUsed
			}
		}
		if len(r.items) >= openAIFirstServeHTTPLimit {
			if oldestKey == "" {
				return nil, infraerrors.ServiceUnavailable("FIRST_SERVE_BUSY", "首服模式会话已满，请等待部分请求完成后重试。")
			}
			delete(r.items, oldestKey)
		}
		entry = &openAIFirstServeHTTPEntry{}
		r.items[key] = entry
	}
	entry.refs++
	entry.lastUsed = now
	return entry, nil
}

func (l *openAIFirstServeHTTPLease) release() {
	l.registry.mu.Lock()
	defer l.registry.mu.Unlock()
	l.entry.refs--
	l.entry.lastUsed = time.Now()
	if l.missing && l.entry.refs == 0 {
		delete(l.registry.items, l.key)
	}
}

func (s *OpenAIGatewayService) prepareFirstServeHTTP(ctx context.Context, c *gin.Context, account *Account, body []byte) (context.Context, *Account, *openAIFirstServeHTTPLease, error) {
	if !account.IsOpenAIFirstServe() || c == nil || c.Request == nil || c.Request.Method != http.MethodPost || GetOpenAIClientTransport(c) == OpenAIClientTransportWS {
		return ctx, account, nil, nil
	}
	if l := firstServeHTTPLease(ctx); l != nil && l.accountID == account.ID {
		return ctx, account, nil, nil // internal retry belongs to the same request
	}
	if err := validateOpenAIFirstServe(account); err != nil {
		return ctx, account, nil, err
	}
	cfg, err := account.firstServeConfig()
	if err != nil {
		return ctx, account, nil, err
	}
	apiKeyID := getAPIKeyIDFromContext(c)
	scope, _ := resolveOpenAIWSExecutionScope(c, body, apiKeyID)
	if scope == "" {
		session := strings.TrimSpace(gjson.GetBytes(body, "client_metadata.session_id").String())
		if session == "" {
			session = promptCacheKeyFromAnthropicMetadataSession(&apicompat.AnthropicRequest{Metadata: []byte(gjson.GetBytes(body, "metadata").Raw)})
		}
		if session != "" {
			scope, _ = deriveOpenAISessionHashes(openAIWSExecutionScopeSeed(apiKeyID, "session", session, resolveOpenAIWSExecutionLane(c, body)))
		}
	}
	missing := scope == "" || apiKeyID == 0
	if missing {
		// A content hash is not a conversation identity. Do not merge unrelated
		// chats just because they use the same account/API key or initial prompt.
		scope = uuid.NewString()
	}
	key := fmt.Sprintf("%d:%d:%s:%s", account.ID, getOpenAIGroupIDFromContext(c), scope, cfg.key(*account.ProxyGroupID))
	now := time.Now()
	entry, err := s.openaiFirstServeHTTP.acquire(key, now)
	if err != nil {
		return ctx, account, nil, err
	}
	l := &openAIFirstServeHTTPLease{registry: &s.openaiFirstServeHTTP, entry: entry, key: key, accountID: account.ID, missing: missing}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.state == nil {
		entry.state = &openAIFirstServeState{status: OpenAIFirstServeStatus{
			ID: uuid.NewString(), AccountID: account.ID, Config: cfg, Transport: "http", SessionMissing: missing,
		}}
	}
	state := entry.state
	copyAccount := *account
	copyAccount.Proxy = state.proxy
	if state.proxy == nil && now.Before(state.retryAt) {
		l.release()
		return ctx, account, nil, ErrProxyGroupNoProxy
	}
	if state.proxy == nil || state.due(now) {
		// Server-side continuation may be tied to its original session. Keep
		// that combination until the client sends a self-contained request.
		if state.proxy != nil && gjson.GetBytes(body, "previous_response_id").String() != "" {
			state.publish("context_incomplete", now)
		} else {
			proxy, selectErr := selectOpenAIFirstServeProxy(ctx, &copyAccount, state.status.ProxyID)
			if selectErr != nil {
				state.retryAt = now.Add(cfg.cooldown())
				state.publish("proxy_unavailable", now)
				if state.proxy == nil {
					l.release()
					return ctx, account, nil, selectErr
				}
			} else {
				if state.proxy != nil {
					state.attempts++
					state.status.Rotations++
				}
				state.bind(proxy, uuid.NewString(), now)
				l.fresh = true
			}
		}
	}
	state.publish(state.status.Reason, now)
	l.id = state.status.ConnID
	copyAccount.Proxy, copyAccount.ProxyID, copyAccount.proxyGroupResolved = state.proxy, &state.proxy.ID, true
	return context.WithValue(ctx, openAIFirstServeHTTPKey{}, l), &copyAccount, l, nil
}

// Called where the existing streaming parser recognizes first output. A slow
// stream can mark the combination before it ends, without interrupting it.
func observeFirstServeHTTP(ctx context.Context, ms int) {
	l := firstServeHTTPLease(ctx)
	if l == nil {
		return
	}
	l.entry.mu.Lock()
	defer l.entry.mu.Unlock()
	if l.observed || l.entry.state.status.ConnID != l.id {
		return
	}
	l.observed = true
	l.entry.state.observe(ms, time.Now())
}

func (l *openAIFirstServeHTTPLease) finish(result *OpenAIForwardResult, err error) {
	if l == nil {
		return
	}
	defer l.release()
	l.entry.mu.Lock()
	defer l.entry.mu.Unlock()
	state := l.entry.state
	if state.status.ConnID != l.id {
		return // an older concurrent response must not overwrite the new combination
	}
	now := time.Now()
	if l.observed {
		state.publish(state.status.Reason, now)
	} else if result != nil && result.FirstTokenMs != nil {
		state.observe(*result.FirstTokenMs, now)
	} else if err != nil {
		state.pending = true
		state.publish("connection_failed", now)
	} else if !state.pending {
		state.publish("no_token", now)
	}
	if l.missing {
		state.status.Active = false
		state.publish(state.status.Reason, now)
	}
}

// Apply after all protocol adapters, account overrides and fingerprint changes.
// Only the outbound copy changes; ingress and retry bodies remain intact.
func applyFirstServeHTTPRequest(req *http.Request, account *Account) error {
	l := firstServeHTTPLease(req.Context())
	if l == nil || l.accountID != account.ID {
		return nil
	}
	for _, key := range []string{"session_id", "session-id", "conversation_id"} {
		req.Header.Set(key, l.id)
	}
	rewriteCodexTurnMetadataFields(req.Header, map[string]any{"session_id": l.id})
	if l.fresh {
		req.Header.Del("x-codex-turn-state")
	}
	if !strings.HasSuffix(strings.TrimRight(req.URL.Path, "/"), "/responses") || req.GetBody == nil {
		return nil
	}
	reader, err := req.GetBody()
	if err != nil {
		return err
	}
	body, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		return err
	}
	body, err = sjson.SetBytes(body, "prompt_cache_key", l.id)
	if err != nil {
		return err
	}
	if gjson.GetBytes(body, "client_metadata.session_id").Exists() {
		body, err = sjson.SetBytes(body, "client_metadata.session_id", l.id)
		if err != nil {
			return err
		}
	}
	if metadata := gjson.GetBytes(body, "client_metadata.x-codex-turn-metadata"); metadata.Type == gjson.String {
		headers := http.Header{}
		headers.Set("x-codex-turn-metadata", metadata.String())
		rewriteCodexTurnMetadataFields(headers, map[string]any{"session_id": l.id})
		body, err = sjson.SetBytes(body, "client_metadata.x-codex-turn-metadata", headers.Get("x-codex-turn-metadata"))
		if err != nil {
			return err
		}
	}
	_ = req.Body.Close()
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(body)), nil }
	return nil
}
