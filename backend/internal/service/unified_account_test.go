package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/identity"
)

func TestUnifiedStoreEmailCheck(t *testing.T) {
	for _, tc := range []struct {
		name, body   string
		status, want int
	}{
		{"available", `{"available":true}`, 200, 0},
		{"occupied", `{}`, 409, 409},
		{"unavailable", `{}`, 503, 503},
		{"wrong service", `<html>index</html>`, 200, 503},
		{"denied", `{"available":false}`, 200, 503},
		{"redirect", `{}`, 302, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Unified-Key") != "test-key" || r.URL.Path != "/api/auth/unified/email-check" || r.Method != http.MethodPost {
					t.Error("invalid authenticated callback")
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			t.Setenv("UNIFIED_STORE_URL", server.URL)
			s := &AuthService{Unified: &identity.Service{Key: "test-key"}}
			err := s.checkStoreEmail(context.Background(), "id", "user@example.test")
			if tc.want == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var typed *identity.Error
			if !errors.As(err, &typed) || typed.Status != tc.want {
				t.Fatalf("unexpected result: %v", err)
			}
		})
	}
	t.Run("requires secure configured target", func(t *testing.T) {
		for _, target := range []string{"", "http://example.com", ":invalid", "https://user:password@example.com", "https://example.com?target=other", "https://example.com#other"} {
			t.Setenv("UNIFIED_STORE_URL", target)
			s := &AuthService{Unified: &identity.Service{Key: "test-key"}}
			if err := s.checkStoreEmail(context.Background(), "id", "user@example.test"); err == nil {
				t.Fatalf("accepted %q", target)
			}
		}
	})
}
