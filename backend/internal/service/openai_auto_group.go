package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
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
	id      string
	turn    atomic.Int64
	group   atomic.Pointer[Group]
	removed sync.Map
}

type OpenAIAutoGroupState struct {
	Status        string  `json:"status"`
	At            string  `json:"at"`
	StatusCode    int     `json:"status_code"`
	FailureCount  int64   `json:"failure_count"`
	WindowMinutes int     `json:"window_minutes"`
	Message       string  `json:"message"`
	FromGroupIDs  []int64 `json:"from_group_ids,omitempty"`
	ToGroupIDs    []int64 `json:"to_group_ids,omitempty"`
}

type OpenAIAutoGroupRepository interface {
	DowngradeOpenAIAccountGroups(ctx context.Context, accountID, triggerGroupID int64, state OpenAIAutoGroupState) (bool, error)
}

type OpenAIAutoGroupCounter interface {
	RecordOpenAIAutoGroupFailure(ctx context.Context, accountID int64, revision, requestID string, windowMinutes, threshold int) (int64, bool, error)
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
	if s == nil || s.settingService == nil || s.rateLimitService == nil || account == nil ||
		account.ID <= 0 || account.Platform != PlatformOpenAI || account.IsShadow() || !isOpenAIAutoGroupFailure(status, body) {
		return
	}
	request, _ := ctx.Value(openAIAutoGroupContextKey{}).(*openAIAutoGroupRequest)
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	if request != nil && request.group.Load() != nil {
		group = request.group.Load()
	}
	if group == nil || group.Platform != PlatformOpenAI || group.AutoRouteEnabled || group.RateMultiplier <= OpenAIAutoGroupRateBoundary || request == nil {
		return
	}
	stateCtx, cancel := openAIAccountStateContext(ctx)
	defer cancel()
	settings, err := s.settingService.openAIAutoGroupRuntimeSettings(stateCtx)
	if err != nil {
		slog.Warn("openai_auto_group_settings_failed", "account_id", account.ID, "error", err)
		return
	}
	if !settings.Enabled {
		return
	}
	state := OpenAIAutoGroupState{At: time.Now().UTC().Format(time.RFC3339), StatusCode: status, WindowMinutes: settings.WindowMinutes}
	repo, repoOK := s.accountRepo.(OpenAIAutoGroupRepository)
	counter, counterOK := s.rateLimitService.tempUnschedCache.(OpenAIAutoGroupCounter)
	if !repoOK || !counterOK {
		s.recordOpenAIAutoGroupFailure(stateCtx, account, state, fmt.Errorf("automatic grouping is unavailable; check server repository and Redis configuration"))
		return
	}
	requestID := request.id + ":" + strconv.FormatInt(request.turn.Load(), 10)
	count, triggered, err := counter.RecordOpenAIAutoGroupFailure(stateCtx, account.ID, settings.Revision, requestID, settings.WindowMinutes, settings.FailureThreshold)
	state.FailureCount = count
	if err != nil {
		s.recordOpenAIAutoGroupFailure(stateCtx, account, state, fmt.Errorf("failure counting unavailable; check Redis and retry: %w", err))
		return
	}
	if !triggered {
		return
	}
	state.Status = "downgraded"
	state.Message = "Frequent upstream overload/rate limits; moved to compatible groups at or below 2x"
	changed, err := repo.DowngradeOpenAIAccountGroups(stateCtx, account.ID, group.ID, state)
	if err != nil {
		s.recordOpenAIAutoGroupFailure(stateCtx, account, state, err)
		return
	}
	if changed {
		request.removed.Store(account.ID, true)
		slog.Info("openai_auto_group_downgraded", "account_id", account.ID, "trigger_group_id", group.ID, "count", count)
	}
}

func (s *OpenAIGatewayService) recordOpenAIAutoGroupFailure(ctx context.Context, account *Account, state OpenAIAutoGroupState, cause error) {
	state.Status = "failed"
	state.Message = cause.Error()
	slog.Warn("openai_auto_group_failed", "account_id", account.ID, "error", cause)
	if err := s.accountRepo.UpdateExtra(ctx, account.ID, map[string]any{OpenAIAutoGroupStateExtraKey: state}); err != nil {
		slog.Error("openai_auto_group_status_save_failed", "account_id", account.ID, "error", err)
	}
}

func (s *OpenAIGatewayService) checkOpenAIAutoGroupMembership(ctx context.Context, account *Account) error {
	if s == nil || s.settingService == nil || account == nil || account.Platform != PlatformOpenAI || account.IsShadow() {
		return nil
	}
	group, _ := ctx.Value(ctxkey.Group).(*Group)
	if group == nil || group.Platform != PlatformOpenAI || group.AutoRouteEnabled || group.RateMultiplier <= OpenAIAutoGroupRateBoundary {
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
	if !settings.Enabled {
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
	wrapped.BeforeTurn = func(turn int) error {
		if request, ok := ctx.Value(openAIAutoGroupContextKey{}).(*openAIAutoGroupRequest); ok {
			request.turn.Store(int64(turn))
		}
		if err := s.checkOpenAIAutoGroupMembership(ctx, account); err != nil {
			return NewOpenAIWSClientCloseError(coderws.StatusTryAgainLater, "account group changed; reconnect to select another account", err)
		}
		if beforeTurn != nil {
			return beforeTurn(turn)
		}
		return nil
	}
	return &wrapped
}
