//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

func TestRequestBodyFailureCounterRaisesEachLevelOnce(t *testing.T) {
	counter := requestBodyFailureCounter{entries: make(map[string]requestBodyFailureWindowEntry)}
	now := time.Now()

	for i := 1; i <= 9; i++ {
		count, score, level := counter.record("user:1", 1, now)
		require.Equal(t, i, count)
		require.Equal(t, i, score)
		require.Zero(t, level)
	}
	_, _, level := counter.record("user:1", 1, now)
	require.Equal(t, 1, level)
	_, _, level = counter.record("user:1", 1, now)
	require.Zero(t, level)

	for i := 12; i <= 30; i++ {
		_, _, level = counter.record("user:1", 1, now)
	}
	require.Equal(t, 2, level)
}

func TestRequestBodyFailureCounterResetsExpiredWindow(t *testing.T) {
	counter := requestBodyFailureCounter{entries: make(map[string]requestBodyFailureWindowEntry)}
	now := time.Now()
	counter.record("user:1", 5, now)

	count, score, level := counter.record("user:1", 1, now.Add(requestBodyFailureWindow))
	require.Equal(t, 1, count)
	require.Equal(t, 1, score)
	require.Zero(t, level)
}

func TestRequestBodyFailureWeight(t *testing.T) {
	require.Zero(t, requestBodyFailureWeight(pkghttputil.RequestBodyErrorReadCanceled))
	require.Equal(t, 1, requestBodyFailureWeight(pkghttputil.RequestBodyErrorUnexpectedEOF))
	require.Equal(t, 3, requestBodyFailureWeight(pkghttputil.RequestBodyErrorInvalidCompression))
	require.Equal(t, 5, requestBodyFailureWeight(pkghttputil.RequestBodyErrorTooLarge))
}

func TestFormatRequestBodyDiagnosticContainsMetadataOnly(t *testing.T) {
	diagnostic := requestBodyDiagnostic{
		Kind:             pkghttputil.RequestBodyErrorUnexpectedEOF,
		Encoding:         "gzip",
		BytesRead:        128,
		ContentLength:    256,
		TransferEncoding: "chunked",
		WindowCount:      4,
		WindowScore:      4,
	}

	got := formatRequestBodyDiagnostic(diagnostic)
	require.Contains(t, got, "request_body_error=unexpected_eof")
	require.Contains(t, got, "bytes_read=128")
	require.NotContains(t, got, "request_body=")
}

func TestRequestBodyDiagnosticsClassifiesUnknownError(t *testing.T) {
	diagnostic := pkghttputil.RequestBodyDiagnostics(context.DeadlineExceeded)
	require.Equal(t, pkghttputil.RequestBodyErrorReadFailed, diagnostic.Kind)
}

func TestLogRequestBodyReadFailureClassifiesWithoutPayload(t *testing.T) {
	log, logs := newObservedLogger(t)
	req, err := http.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("secret-payload-marker"))
	require.NoError(t, err)
	req.Header.Set("Content-Encoding", "gzip")
	req.ContentLength = 21

	logRequestBodyReadFailure(log, req, errors.New(`decode Content-Encoding "gzip": unexpected EOF secret-payload-marker`))

	entries := logs.All()
	require.Len(t, entries, 1)
	require.Equal(t, "read request body failed", entries[0].Message)
	fields := entries[0].ContextMap()
	require.Equal(t, "decode_content_encoding", fields["error_kind"])
	require.Equal(t, "gzip", fields["content_encoding"])
	require.EqualValues(t, 21, fields["content_length"])
	require.NotContains(t, entries[0].Message+fmt.Sprint(fields), "secret-payload-marker")
}

func TestRequestBodyReadErrorKind(t *testing.T) {
	require.Equal(t, "unsupported_content_encoding", requestBodyReadErrorKind(errors.New(`decode Content-Encoding "br": unsupported Content-Encoding`)))
	require.Equal(t, "truncated_body", requestBodyReadErrorKind(io.ErrUnexpectedEOF))
	require.Equal(t, "max_bytes", requestBodyReadErrorKind(&http.MaxBytesError{Limit: 10}))
	require.Equal(t, "other", requestContentEncodingCategory("private-payload-marker"))
}

func TestRequestBodyReadFailureLogsWithoutPayload(t *testing.T) {
	log, logs := newObservedLogger(t)
	req, err := http.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader("secret-payload-marker"))
	require.NoError(t, err)
	req.Header.Set("Content-Encoding", "gzip")
	req.ContentLength = 21

	status, message := requestBodyReadFailure(log, req, errors.New(`decode Content-Encoding "gzip": unexpected EOF secret-payload-marker`))

	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "Failed to read request body", message)
	entries := logs.All()
	require.Len(t, entries, 1)
	require.Equal(t, "read request body failed", entries[0].Message)
	fields := entries[0].ContextMap()
	require.Equal(t, "decode_content_encoding", fields["error_kind"])
	require.Equal(t, "gzip", fields["content_encoding"])
	require.EqualValues(t, 21, fields["content_length"])
	require.NotContains(t, entries[0].Message+fmt.Sprint(fields), "secret-payload-marker")
}

func TestRequestBodyReadFailureKeepsBodyLimitMessageWithoutLogging(t *testing.T) {
	log, logs := newObservedLogger(t)
	status, message := requestBodyReadFailure(log, nil, &http.MaxBytesError{Limit: 2 << 20})
	require.Equal(t, http.StatusRequestEntityTooLarge, status)
	require.Equal(t, buildBodyTooLargeMessage(2<<20), message)
	require.Empty(t, logs.All())
}

// cancelOnReadErr models net/http on HTTP/1: a failed body read cancels the
// request context before the error reaches the handler.

type cancelOnReadErr struct {
	err    error
	cancel context.CancelFunc
}

func TestGatewayMessagesClassifiesRequestBodyReadFailures(t *testing.T) {
	gin.SetMode(gin.TestMode)
	deadlineErr := &net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}

	tests := []struct {
		name        string
		readErr     error
		cancelCtx   bool
		wantStatus  int
		wantMessage string
	}{
		{"h2 read timeout", deadlineErr, false, http.StatusRequestTimeout, "Timed out reading request body"},
		// net/http cancels the request context when an HTTP/1 read deadline
		// fires; this is still a timeout, not a client that went away.
		{"http1 read timeout", deadlineErr, true, http.StatusRequestTimeout, "Timed out reading request body"},
		// A client that went away gets no body, matching the failover path.
		{"client disconnect", io.ErrUnexpectedEOF, true, 499, ""},
		{"truncated body", io.ErrUnexpectedEOF, false, http.StatusBadRequest, "Request body was truncated before upload completed."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failing := iotest.ErrReader(tc.readErr)
			if tc.cancelCtx {
				failing = cancelOnReadErr{err: tc.readErr, cancel: cancel}
			}
			body := io.MultiReader(strings.NewReader(`{"model":"claude-sonnet-4-5","messages":[`), failing)
			req := httptest.NewRequest(http.MethodPost, "/v1/messages", body).WithContext(ctx)
			req.ContentLength = 1 << 20
			c.Request = req
			groupID := int64(1)
			c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 1, UserID: 1, GroupID: &groupID})
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1, Concurrency: 1})

			(&GatewayHandler{}).Messages(c)

			require.Equal(t, tc.wantStatus, c.Writer.Status())
			if tc.wantMessage == "" {
				require.Empty(t, rec.Body.String())
				return
			}
			var resp struct {
				Type  string `json:"type"`
				Error struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
			require.Equal(t, "error", resp.Type)
			require.Equal(t, "invalid_request_error", resp.Error.Type)
			require.Contains(t, resp.Error.Message, tc.wantMessage)
		})
	}
}

// Google-format errors carry no type field; body read failures must still be
// attributed to the client request rather than the platform.

func TestGeminiRequestBodyReadTimeoutClassifiedAsClient(t *testing.T) {
	entry := recordGoogleFormatOpsError(t, func(c *gin.Context) {
		c.Request.Body = io.NopCloser(iotest.ErrReader(&net.OpError{Op: "read", Net: "tcp", Err: os.ErrDeadlineExceeded}))
		readGeminiCachedContentBody(c)
	})
	require.Equal(t, http.StatusRequestTimeout, entry.StatusCode)
	require.Equal(t, "invalid_request_error", entry.ErrorType)
	require.Equal(t, "request", entry.ErrorPhase)
	require.Equal(t, "client", entry.ErrorOwner)
	require.Equal(t, "P3", entry.Severity)
}

func TestRecordRequestBodyReadFailureKeepsCountersWithoutPayload(t *testing.T) {
	log, logs := newObservedLogger(t)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	recordRequestBodyReadFailure(c, log, errors.New("secret-payload-marker"))
	diagnostic, ok := requestBodyDiagnosticFromContext(c)
	require.True(t, ok)
	require.Positive(t, diagnostic.WindowCount)
	for _, entry := range logs.All() {
		require.NotContains(t, entry.Message+fmt.Sprint(entry.ContextMap()), "secret-payload-marker")
	}
	require.NotPanics(t, func() { recordRequestBodyReadFailure(nil, log, io.ErrUnexpectedEOF) })
}

func (r cancelOnReadErr) Read([]byte) (int, error) {
	r.cancel()
	return 0, r.err
}
