package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type autoGroupSettingsRepo struct {
	service.SettingRepository
	value string
}

func (repo *autoGroupSettingsRepo) GetValue(context.Context, string) (string, error) {
	if repo.value == "" {
		return "", service.ErrSettingNotFound
	}
	return repo.value, nil
}

func (repo *autoGroupSettingsRepo) Set(_ context.Context, _, value string) error {
	repo.value = value
	return nil
}

func TestOpenAIAutoGroupSettingsAPI(t *testing.T) {
	repo := &autoGroupSettingsRepo{}
	handler := &SettingHandler{settingService: service.NewSettingService(repo, nil)}
	for _, test := range []struct {
		name   string
		body   string
		status int
	}{
		{name: "valid", body: `{"enabled":true,"default_high_rate":true,"window_minutes":10,"failure_threshold":6}`, status: http.StatusOK},
		{name: "fractional window", body: `{"window_minutes":1.5,"failure_threshold":3}`, status: http.StatusBadRequest},
		{name: "invalid window", body: `{"window_minutes":0,"failure_threshold":3}`, status: http.StatusBadRequest},
		{name: "invalid threshold", body: `{"window_minutes":5,"failure_threshold":1001}`, status: http.StatusBadRequest},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPut, "/settings/openai-auto-group", strings.NewReader(test.body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			handler.UpdateOpenAIAutoGroupSettings(ctx)
			require.Equal(t, test.status, recorder.Code)
		})
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/settings/openai-auto-group", nil)
	handler.GetOpenAIAutoGroupSettings(ctx)
	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"window_minutes":10`)
	require.Contains(t, recorder.Body.String(), `"failure_threshold":6`)
}
