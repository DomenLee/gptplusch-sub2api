package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/identity"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUnifiedRegistrationPolicyRequiresServiceAuthWithoutCredentialRate(t *testing.T) {
	h := &AuthHandler{authService: &service.AuthService{Unified: &identity.Service{Key: "local-service-test-key"}}}
	router := gin.New()
	router.POST("/internal/unified/:action", h.internalUnified)
	for _, key := range []string{"", "wrong-key", "local-service-test-key"} {
		recorder := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/internal/unified/registration-policy", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Unified-Key", key)
		// No identity DB is configured; any credential rate query would panic.
		require.NotPanics(t, func() { router.ServeHTTP(recorder, req) })
		if key == "local-service-test-key" {
			// Missing settings is an explicit failure, not an empty allow-all list.
			require.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		} else {
			require.Equal(t, http.StatusUnauthorized, recorder.Code)
		}
	}
}
