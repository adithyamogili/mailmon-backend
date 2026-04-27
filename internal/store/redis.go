package store

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisStore struct {
	client *redis.Client
}

func New(client *redis.Client) *RedisStore {
	return &RedisStore{client: client}
}

func lastRunKey(userID string) string {
	return fmt.Sprintf("user:%s:state:last_run_timestamp", userID)
}

func (s *RedisStore) GetLastRunTime(ctx context.Context, userID string) (time.Time, error) {
	val, err := s.client.Get(ctx, lastRunKey(userID)).Result()
	if err == redis.Nil {
		return time.Now().Add(-1 * time.Hour), nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("store.GetLastRunTime: %w", err)
	}
	epoch, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("store.GetLastRunTime: invalid timestamp %q: %w", val, err)
	}
	return time.Unix(epoch, 0), nil
}

func (s *RedisStore) SetLastRunTime(ctx context.Context, userID string, t time.Time) error {
	err := s.client.Set(ctx, lastRunKey(userID), strconv.FormatInt(t.Unix(), 10), 0).Err()
	if err != nil {
		return fmt.Errorf("store.SetLastRunTime: %w", err)
	}
	return nil
}
