package dedup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mohitd/mail-cron/internal/classifier"
	"github.com/mohitd/mail-cron/internal/gmail"
	"github.com/redis/go-redis/v9"
)

const ttl = 24 * time.Hour

type RedisDedup struct {
	client *redis.Client
}

func New(client *redis.Client) *RedisDedup {
	return &RedisDedup{client: client}
}

// --- Classification cache: avoid redundant LLM calls ---

func classifiedKey(userID, messageID string) string {
	return fmt.Sprintf("user:%s:classified:%s", userID, messageID)
}

func (d *RedisDedup) GetCached(ctx context.Context, userID, messageID string) (*classifier.Classification, error) {
	val, err := d.client.Get(ctx, classifiedKey(userID, messageID)).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("dedup.GetCached: %w", err)
	}
	var c classifier.Classification
	if err := json.Unmarshal([]byte(val), &c); err != nil {
		return nil, fmt.Errorf("dedup.GetCached: unmarshal: %w", err)
	}
	return &c, nil
}

func (d *RedisDedup) CacheResult(ctx context.Context, userID, messageID string, c *classifier.Classification) error {
	data, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("dedup.CacheResult: marshal: %w", err)
	}
	if err := d.client.Set(ctx, classifiedKey(userID, messageID), data, ttl).Err(); err != nil {
		return fmt.Errorf("dedup.CacheResult: %w", err)
	}
	return nil
}

// --- Notification dedup: avoid duplicate WhatsApp alerts (Flow 1 only) ---

func notifiedKey(userID, messageID string) string {
	return fmt.Sprintf("user:%s:notified:%s", userID, messageID)
}

func (d *RedisDedup) IsNotified(ctx context.Context, userID, messageID string) (bool, error) {
	n, err := d.client.Exists(ctx, notifiedKey(userID, messageID)).Result()
	if err != nil {
		return false, fmt.Errorf("dedup.IsNotified: %w", err)
	}
	return n > 0, nil
}

func (d *RedisDedup) MarkNotified(ctx context.Context, userID, messageID string) error {
	if err := d.client.Set(ctx, notifiedKey(userID, messageID), "1", ttl).Err(); err != nil {
		return fmt.Errorf("dedup.MarkNotified: %w", err)
	}
	return nil
}

func (d *RedisDedup) FilterUnnotified(ctx context.Context, userID string, emails []gmail.Email) ([]gmail.Email, error) {
	if len(emails) == 0 {
		return nil, nil
	}
	pipe := d.client.Pipeline()
	cmds := make([]*redis.IntCmd, len(emails))
	for i, e := range emails {
		cmds[i] = pipe.Exists(ctx, notifiedKey(userID, e.ID))
	}
	if _, err := pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("dedup.FilterUnnotified: %w", err)
	}
	var unseen []gmail.Email
	for i, cmd := range cmds {
		if cmd.Val() == 0 {
			unseen = append(unseen, emails[i])
		}
	}
	return unseen, nil
}
