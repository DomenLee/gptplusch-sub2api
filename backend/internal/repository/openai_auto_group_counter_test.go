package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestOpenAIAutoGroupCounterWindowDedupAndRevision(t *testing.T) {
	server := miniredis.RunT(t)
	now := time.Now()
	server.SetTime(now)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	counter := &tempUnschedCache{rdb: client}
	ctx := context.Background()
	for index := 1; index <= 3; index++ {
		requestID := fmt.Sprint(index)
		count, triggered, err := counter.RecordOpenAIAutoGroupFailure(ctx, 7, "first", requestID, 5, 3)
		require.NoError(t, err)
		require.Equal(t, int64(index), count)
		require.Equal(t, index == 3, triggered)
		count, triggered, err = counter.RecordOpenAIAutoGroupFailure(ctx, 7, "first", requestID, 5, 3)
		require.NoError(t, err)
		require.Equal(t, int64(index), count)
		require.False(t, triggered)
	}
	count, triggered, err := counter.RecordOpenAIAutoGroupFailure(ctx, 7, "new-settings", "4", 5, 3)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	require.False(t, triggered)
	server.SetTime(now.Add(6 * time.Minute))
	server.FastForward(6 * time.Minute)
	count, triggered, err = counter.RecordOpenAIAutoGroupFailure(ctx, 7, "first", "5", 5, 3)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	require.False(t, triggered)
}

func TestOpenAIAutoGroupCounterConcurrentTrip(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	counter := &tempUnschedCache{rdb: client}
	var workers sync.WaitGroup
	results := make(chan bool, 12)
	errors := make(chan error, 12)
	for index := 0; index < 12; index++ {
		workers.Add(1)
		go func(requestID int) {
			defer workers.Done()
			_, triggered, err := counter.RecordOpenAIAutoGroupFailure(context.Background(), 7, "revision", fmt.Sprint(requestID), 5, 3)
			results <- triggered
			errors <- err
		}(index)
	}
	workers.Wait()
	close(results)
	close(errors)
	trips := 0
	for triggered := range results {
		if triggered {
			trips++
		}
	}
	for err := range errors {
		require.NoError(t, err)
	}
	require.Equal(t, 1, trips)
}

func TestOpenAIAutoGroupCounterUsesSlidingWindow(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	counter := &tempUnschedCache{rdb: client}
	now := time.Now()
	for index, offset := range []time.Duration{0, 4 * time.Minute, 5*time.Minute + time.Second} {
		server.SetTime(now.Add(offset))
		count, triggered, err := counter.RecordOpenAIAutoGroupFailure(context.Background(), 7, "revision", fmt.Sprint(index), 5, 3)
		require.NoError(t, err)
		require.False(t, triggered)
		if index == 0 {
			require.Equal(t, int64(1), count)
		} else {
			require.Equal(t, int64(2), count)
		}
	}
}
