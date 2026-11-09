package handler

import (
	"bytes"
	"context"
	"errors"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestRequestBodyLimitTooLarge(t *testing.T) {
	gin.SetMode(gin.TestMode)

	limit := int64(16)
	router := gin.New()
	router.Use(middleware.RequestBodyLimit(limit))
	router.POST("/test", func(c *gin.Context) {
		_, err := io.ReadAll(c.Request.Body)
		if err != nil {
			if maxErr, ok := extractMaxBytesError(err); ok {
				c.JSON(http.StatusRequestEntityTooLarge, gin.H{
					"error": buildBodyTooLargeMessage(maxErr.Limit),
				})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "read_failed",
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	payload := bytes.Repeat([]byte("a"), int(limit+1))
	req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewReader(payload))
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)

	require.Equal(t, http.StatusRequestEntityTooLarge, recorder.Code)
	require.Contains(t, recorder.Body.String(), buildBodyTooLargeMessage(limit))
}

func TestRequestBodyClientErrorDetails(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		status     int
		code       string
		wantInBody string
	}{
		{
			name: "truncated body",
			err: &pkghttputil.RequestBodyError{
				Kind:          pkghttputil.RequestBodyErrorUnexpectedEOF,
				BytesRead:     128,
				ContentLength: 256,
				Err:           io.ErrUnexpectedEOF,
			},
			status:     http.StatusBadRequest,
			code:       "request_body_truncated",
			wantInBody: "Received 128 of 256 bytes",
		},
		{
			name:       "too large",
			err:        &http.MaxBytesError{Limit: 16},
			status:     http.StatusRequestEntityTooLarge,
			code:       "request_body_too_large",
			wantInBody: "limit is 16B",
		},
		{
			name: "unsupported encoding",
			err: &pkghttputil.RequestBodyError{
				Kind: pkghttputil.RequestBodyErrorUnsupportedEncoding,
				Err:  errors.New("unsupported Content-Encoding"),
			},
			status:     http.StatusBadRequest,
			code:       "unsupported_content_encoding",
			wantInBody: "Unsupported request Content-Encoding",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			details := RequestBodyClientErrorDetailsFrom(tt.err)
			require.Equal(t, tt.status, details.Status)
			require.Equal(t, tt.code, details.Code)
			require.Contains(t, details.Message, tt.wantInBody)
		})
	}
}

func TestHandleRequestBodyReadErrorCancellation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cancelled bool
		committed bool
		readErr   error
		handled   bool
	}{
		{name: "cancelled", cancelled: true, handled: true},
		{name: "cancelled_after_commit", cancelled: true, committed: true, handled: true},
		{name: "ordinary_read_error"},
		// An HTTP/1 read deadline also cancels the request context; the
		// timeout must reach requestBodyReadFailure (408).
		{name: "read_timeout_with_cancelled_context", cancelled: true, readErr: &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelled {
				cancel()
			}
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
			if tc.committed {
				_, err := c.Writer.WriteString("already committed")
				require.NoError(t, err)
			}
			bodyBefore := recorder.Body.String()
			readErr := tc.readErr
			if readErr == nil {
				readErr = io.ErrUnexpectedEOF
			}
			require.Equal(t, tc.handled, handleRequestBodyReadError(c, nil, readErr))
			require.Equal(t, tc.handled, c.IsAborted())
			status := http.StatusOK
			if tc.handled && !tc.committed {
				status = statusClientClosedRequest
			}
			require.Equal(t, status, c.Writer.Status())
			require.Equal(t, bodyBefore, recorder.Body.String(), "cancelled clients must not receive an error body")
		})
	}
}
