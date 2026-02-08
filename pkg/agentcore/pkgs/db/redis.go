package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/spf13/viper"
)

var (
	keyPrefixSession     = "agentland:session:"      // 会话信息前缀
	keyLastActivityIndex = "agentland:last-activity" // 按活跃时间排序的索引
	keyExpiresAtIndex    = "agentland:expires-at"    // 按过期时间排序的索引

	MaxSessionDuration = 1 * time.Hour
	MaxIdleDuration    = 15 * time.Minute
)

type SessionStore struct {
	client *redis.Client
}

type SandboxInfo struct {
	SandboxID    string    `json:"sandbox_id"`
	GrpcEndpoint string    `json:"grpc_endpoint"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
}

func NewRedis() *redis.Client {
	rdb := redis.NewClient(&redis.Options{
		Addr:         viper.GetString("redis.addr"),
		Password:     viper.GetString("redis.password"),
		DB:           viper.GetInt("redis.db"),
		DialTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		ReadTimeout:  5 * time.Second,
	})

	return rdb
}

func NewSessionStore() *SessionStore {
	return &SessionStore{
		client: NewRedis(),
	}
}

// CreateSession 创建一个新的 Session，存入 Redis
func (s *SessionStore) CreateSession(ctx context.Context, info *SandboxInfo) error {
	now := time.Now()
	if info.CreatedAt.IsZero() {
		info.CreatedAt = now
	}

	if info.ExpiresAt.IsZero() {
		info.ExpiresAt = now.Add(MaxSessionDuration)
	}

	ttl := time.Until(info.ExpiresAt)
	if ttl <= 0 {
		return fmt.Errorf("session expiresAt is invalid: %s", info.ExpiresAt.Format(time.RFC3339))
	}

	data, err := json.Marshal(info)
	if err != nil {
		return err
	}

	key := keyPrefixSession + info.SandboxID

	if err := s.client.Set(ctx, key, data, ttl).Err(); err != nil {
		return err
	}

	if err := s.client.ZAdd(ctx, keyLastActivityIndex, redis.Z{
		Score:  float64(now.Unix()),
		Member: info.SandboxID,
	}).Err(); err != nil {
		return err
	}

	if err := s.client.ZAdd(ctx, keyExpiresAtIndex, redis.Z{
		Score:  float64(info.ExpiresAt.Unix()),
		Member: info.SandboxID,
	}).Err(); err != nil {
		return err
	}

	return nil
}

// DeleteSession 删除 Session 及其索引
func (s *SessionStore) DeleteSession(ctx context.Context, sandboxID string) error {
	key := keyPrefixSession + sandboxID

	if err := s.client.Del(ctx, key).Err(); err != nil {
		return err
	}

	if err := s.client.ZRem(ctx, keyLastActivityIndex, sandboxID).Err(); err != nil {
		return err
	}

	if err := s.client.ZRem(ctx, keyExpiresAtIndex, sandboxID).Err(); err != nil {
		return err
	}

	return nil
}

// ListInactiveSessions 返回超过 IdleTimeout 的 Session 列表
func (s *SessionStore) ListInactiveSessions(ctx context.Context, before time.Time, limit int64) ([]string, error) {
	// 查询 LastActivity < before 的 Session
	result, err := s.client.ZRangeByScore(ctx, keyLastActivityIndex, &redis.ZRangeBy{
		Min:   "-inf",
		Max:   fmt.Sprintf("%d", before.Unix()),
		Count: limit,
	}).Result()
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ListExpiredSessions 返回已过期 (ExpiresAt < now) 的 Session 列表
func (s *SessionStore) ListExpiredSessions(ctx context.Context, now time.Time, limit int64) ([]string, error) {
	result, err := s.client.ZRangeByScore(ctx, keyExpiresAtIndex, &redis.ZRangeBy{
		Min:   "-inf",
		Max:   fmt.Sprintf("%d", now.Unix()),
		Count: limit,
	}).Result()
	if err != nil {
		return nil, err
	}
	return result, nil
}
