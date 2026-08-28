package handler

import (
	"context"
	"testing"
	"time"

	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/stretchr/testify/require"
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
