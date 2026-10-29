package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestOpenAIAutoGroupCounterRequestsDedupAndRevision(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	counter := &tempUnschedCache{rdb: client}
	p := service.DefaultOpenAIAutoGroupSettings()
	p.Enabled = true
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		sample := service.OpenAIAutoGroupSample{ID: fmt.Sprint(i), Overload: true}
		h, added, err := counter.RecordOpenAIAutoGroupSample(ctx, 7, "first", sample, p, false)
		require.NoError(t, err)
		require.True(t, added)
		require.Len(t, h.Requests, i+1)
		_, added, err = counter.RecordOpenAIAutoGroupSample(ctx, 7, "first", sample, p, false)
		require.NoError(t, err)
		require.False(t, added)
	}
	server.FastForward(24 * 365 * time.Hour)
	h, _, err := counter.RecordOpenAIAutoGroupSample(ctx, 7, "first", service.OpenAIAutoGroupSample{ID: "after-year"}, p, false)
	require.NoError(t, err)
	require.Len(t, h.Requests, 4, "elapsed time must not discard requests")
	d := service.EvaluateOpenAIAutoGroup(service.OpenAIAutoGroupState{}, h, service.OpenAIAutoGroupSample{}, p, true, time.Now())
	require.Equal(t, "down", d.Action)
	for i := 0; i < 20; i++ {
		h, _, err = counter.RecordOpenAIAutoGroupSample(ctx, 7, "first", service.OpenAIAutoGroupSample{ID: fmt.Sprint(100 + i), Success: true, Cohort: "model", FirstTokenMS: 1000}, p, false)
		require.NoError(t, err)
	}
	d = service.EvaluateOpenAIAutoGroup(service.OpenAIAutoGroupState{}, h, service.OpenAIAutoGroupSample{}, p, true, time.Now())
	require.Empty(t, d.Action)
	require.Zero(t, d.State.FailureCount)
	h, _, err = counter.RecordOpenAIAutoGroupSample(ctx, 7, "new-settings-or-generation", service.OpenAIAutoGroupSample{ID: "fresh"}, p, false)
	require.NoError(t, err)
	require.Len(t, h.Requests, 1)
	require.Empty(t, h.Cohorts)
	require.Len(t, server.Keys(), 1, "only one bounded key per account")
}
