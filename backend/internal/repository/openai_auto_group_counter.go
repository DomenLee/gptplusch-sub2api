package repository

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

var openAIAutoGroupFailureScript = redis.NewScript(`
	local now = redis.call('TIME')
	local now_ms = tonumber(now[1]) * 1000 + math.floor(tonumber(now[2]) / 1000)
	local window_ms = tonumber(ARGV[1]) * 60000
	local ttl = tonumber(ARGV[1]) * 60 + 60
	redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now_ms - window_ms)
	local added = redis.call('ZADD', KEYS[1], 'NX', now_ms, ARGV[3])
	redis.call('EXPIRE', KEYS[1], ttl)
	local count = redis.call('ZCARD', KEYS[1])
	if added == 1 and count >= tonumber(ARGV[2]) then
		if redis.call('SET', KEYS[2], '1', 'NX', 'EX', 30) then
			return {count, 1}
		end
	end
	return {count, 0}
`)

func (c *tempUnschedCache) RecordOpenAIAutoGroupFailure(ctx context.Context, accountID int64, revision, requestID string, windowMinutes, threshold int) (int64, bool, error) {
	key := fmt.Sprintf("openai_auto_group:{%d:%s}", accountID, revision)
	result, err := openAIAutoGroupFailureScript.Run(ctx, c.rdb, []string{key, key + ":lock"}, windowMinutes, threshold, requestID).Int64Slice()
	if err != nil {
		return 0, false, err
	}
	if len(result) != 2 {
		return 0, false, fmt.Errorf("invalid GPT auto-group counter response")
	}
	return result[0], result[1] == 1, nil
}
