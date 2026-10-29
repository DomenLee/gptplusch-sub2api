package service

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const OpenAIAutoGroupStateExtraKey = "openai_auto_group_state"
const openAIAutoGroupRequestKey = "openai_auto_group_request"

type openAIAutoGroupContextKey struct{}

type openAIAutoGroupRequest struct {
	id          string
	turn        atomic.Int64
	group       atomic.Pointer[Group]
	removed     sync.Map
	mu          sync.Mutex
	attempts    map[string]*openAIAutoGroupAttempt
	lastAccount int64
	lastEnd     time.Time
	attemptTurn int64
}

type OpenAIAutoGroupState struct {
	Status           string                   `json:"status"`
	At               string                   `json:"at"`
	StatusCode       int                      `json:"status_code"`
	FailureCount     int64                    `json:"failure_count"`
	WindowMinutes    int                      `json:"window_minutes,omitempty"`
	Message          string                   `json:"message"`
	FromGroupIDs     []int64                  `json:"from_group_ids,omitempty"`
	ToGroupIDs       []int64                  `json:"to_group_ids,omitempty"`
	Generation       string                   `json:"generation,omitempty"`
	ProtectedUntil   string                   `json:"protected_until,omitempty"`
	Paused           bool                     `json:"paused,omitempty"`
	Reason           string                   `json:"reason,omitempty"`
	Cohort           string                   `json:"cohort,omitempty"`
	RequestCount     int                      `json:"request_count"`
	SampleCount      int                      `json:"sample_count"`
	SlowCount        int                      `json:"slow_count"`
	RecoveryCount    int                      `json:"recovery_count"`
	FailureWindow    int                      `json:"failure_window"`
	LatencyWindow    int                      `json:"latency_window"`
	RecoverySamples  int                      `json:"recovery_samples"`
	RestoreBindings  []OpenAIAutoGroupBinding `json:"restore_bindings,omitempty"`
	ExpectedBindings []OpenAIAutoGroupBinding `json:"expected_bindings,omitempty"`
	RecoveryCohorts  []string                 `json:"recovery_cohorts,omitempty"`
}

type OpenAIAutoGroupRepository interface {
	ProcessOpenAIAutoGroup(context.Context, int64, string, func(*Account) (*OpenAIAutoGroupDecision, error)) (bool, error)
}

type OpenAIAutoGroupCounter interface {
	RecordOpenAIAutoGroupSample(context.Context, int64, string, OpenAIAutoGroupSample, *OpenAIAutoGroupSettings, bool) (*OpenAIAutoGroupHistory, bool, error)
}

func withOpenAIAutoGroupRequest(ctx context.Context, c *gin.Context) context.Context {
	if c == nil || c.Request == nil {
		return ctx
	}
	request, _ := c.Value(openAIAutoGroupRequestKey).(*openAIAutoGroupRequest)
	if request == nil {
		request = &openAIAutoGroupRequest{id: uuid.NewString()}
		request.turn.Store(int64(c.GetInt(OpsStreamTurnKey)))
		c.Set(openAIAutoGroupRequestKey, request)
	}
	if group, ok := ctx.Value(ctxkey.Group).(*Group); ok {
		request.group.Store(group)
	}
	ctx = context.WithValue(ctx, openAIAutoGroupContextKey{}, request)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), openAIAutoGroupContextKey{}, request))
	return ctx
}

func isOpenAIAutoGroupFailure(status int, body []byte) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	return (status == http.StatusServiceUnavailable || status == 529) && isOpenAIRequestScopedCapacityShed("", body)
}

func (s *OpenAIGatewayService) observeOpenAIAutoGroupFailure(ctx context.Context, account *Account, status int, body []byte) {
	if status < 400 {
		return
	}
	attempt := s.openAIAutoGroupAttempt(ctx, account)
	if attempt == nil {
		return
	}
	s.recordOpenAIAutoGroupSample(ctx, account, attempt, OpenAIAutoGroupSample{ID: attempt.id, Overload: isOpenAIAutoGroupFailure(status, body), Failed: true, StatusCode: status, FirstTokenMS: -1})
}

func (s *OpenAIGatewayService) checkOpenAIAutoGroupMembership(ctx context.Context, account *Account) error {
	if s == nil || s.settingService == nil || account == nil || account.Platform != PlatformOpenAI || account.IsShadow() {
		return nil
	}
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	if group == nil || group.Platform != PlatformOpenAI || group.AutoRouteEnabled {
		return nil
	}
	if request, ok := ctx.Value(openAIAutoGroupContextKey{}).(*openAIAutoGroupRequest); ok {
		if _, removed := request.removed.Load(account.ID); removed {
			return &UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable}
		}
	}
	settings, err := s.settingService.openAIAutoGroupRuntimeSettings(ctx)
	if err != nil {
		slog.Warn("openai_auto_group_membership_settings_failed", "account_id", account.ID, "error", err)
		return nil
	}
	if !settings.Active() {
		return nil
	}
	var current *Account
	if s.schedulerSnapshot != nil {
		current, err = s.schedulerSnapshot.GetAccount(ctx, account.ID)
	} else {
		current, err = s.accountRepo.GetByID(ctx, account.ID)
	}
	if err != nil {
		return err
	}
	if !openAIStickyAccountMatchesGroup(current, &group.ID) {
		return &UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable, ResponseBody: []byte(`{"error":{"message":"Account is no longer bound to this group; select another account"}}`)}
	}
	return nil
}

func (s *OpenAIGatewayService) openAIAutoGroupWSHooks(ctx context.Context, account *Account, hooks *OpenAIWSIngressHooks) *OpenAIWSIngressHooks {
	wrapped := OpenAIWSIngressHooks{}
	if hooks != nil {
		wrapped = *hooks
	}
	beforeTurn := wrapped.BeforeTurn
	afterTurn := wrapped.AfterTurn
	var finish func(*OpenAIForwardResult, error)
	wrapped.BeforeTurn = func(turn int) error {
		if request, ok := ctx.Value(openAIAutoGroupContextKey{}).(*openAIAutoGroupRequest); ok {
			if request.turn.Load() != int64(turn) {
				request.mu.Lock()
				request.attempts = nil
				request.lastAccount, request.lastEnd = 0, time.Time{}
				request.mu.Unlock()
			}
			request.turn.Store(int64(turn))
		}
		if err := s.checkOpenAIAutoGroupMembership(ctx, account); err != nil {
			return NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "account group changed; reconnect to select another account", err)
		}
		if beforeTurn != nil {
			if err := beforeTurn(turn); err != nil {
				return err
			}
		}
		finish = s.beginOpenAIAutoGroup(ctx, account)
		return nil
	}
	wrapped.AfterTurn = func(turn int, result *OpenAIForwardResult, err error) {
		if finish != nil {
			finish(result, err)
			finish = nil
		}
		if afterTurn != nil {
			afterTurn(turn, result, err)
		}
	}
	return &wrapped
}
