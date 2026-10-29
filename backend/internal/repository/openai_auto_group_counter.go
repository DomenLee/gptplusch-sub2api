package repository

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// The caller holds the account row lock across this read/update and its group
// decision. One bounded key per account replaces the previous time-window keys.
func (c *tempUnschedCache) RecordOpenAIAutoGroupSample(ctx context.Context, accountID int64, version string, sample service.OpenAIAutoGroupSample, policy *service.OpenAIAutoGroupSettings, recovering bool) (*service.OpenAIAutoGroupHistory, bool, error) {
	key := fmt.Sprintf("openai_auto_group:requests:{%d}", accountID)
	history := &service.OpenAIAutoGroupHistory{}
	added := false
	value, err := c.rdb.Get(ctx, key).Bytes()
	if err != nil && err != redis.Nil {
		return nil, false, err
	}
	if len(value) > 0 {
		if err := json.Unmarshal(value, history); err != nil {
			return nil, false, err
		}
	}
	if history.Version != version {
		*history = service.OpenAIAutoGroupHistory{Version: version}
	}
	added = service.UpdateOpenAIAutoGroupHistory(history, sample, policy, recovering)
	if !added {
		return history, false, nil
	}
	value, err = json.Marshal(history)
	if err != nil {
		return nil, false, err
	}
	if err := c.rdb.Set(ctx, key, value, 0).Err(); err != nil {
		return nil, false, err
	}
	return history, true, nil
}
