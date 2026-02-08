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

	MaxSessionDuration = 2 * time.Hour
	MaxIdleDuration    = 30 * time.Minute
)

type SessionStore struct {
	client *redis.Client
}

type SessionInfo struct {
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
func (s *SessionStore) CreateSession(ctx context.Context, info *SessionInfo) error {
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}

	key := keyPrefixSession + info.SandboxID

	if err := s.client.Set(ctx, key, data, MaxSessionDuration).Err(); err != nil {
		return err
	}

	now := time.Now()

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

// UpdateLatestActivity 更新 Session 的最后活跃时间
func (s *SessionStore) UpdateLatestActivity(ctx context.Context, sandboxID string) error {
	key := keyPrefixSession + sandboxID

	exists, err := s.client.Exists(ctx, key).Result()
	if err != nil {
		return err
	}
	if exists == 0 {
		return fmt.Errorf("session not found")
	}

	// 更新 LastActivity 索引
	now := time.Now()
	if err := s.client.ZAdd(ctx, keyLastActivityIndex, redis.Z{
		Score:  float64(now.Unix()),
		Member: sandboxID,
	}).Err(); err != nil {
		return err
	}

	return nil
}

// GetSession 获取 Session 信息
func (s *SessionStore) GetSession(ctx context.Context, sandboxID string) (*SessionInfo, error) {
	key := keyPrefixSession + sandboxID

	data, err := s.client.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, fmt.Errorf("session not found")
		}
		return nil, err
	}

	var info SessionInfo
	if err := json.Unmarshal([]byte(data), &info); err != nil {
		return nil, err
	}

	return &info, nil
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
