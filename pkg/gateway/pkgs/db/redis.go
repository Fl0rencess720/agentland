package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/spf13/viper"
)

var (
	keyPrefixSession     = "agentland:session:"      // 会话信息前缀
	keyLastActivityIndex = "agentland:last-activity" // 按活跃时间排序的索引

	ErrSessionNotFound = errors.New("session not found")
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

// UpdateLatestActivity 更新 Session 的最后活跃时间
func (s *SessionStore) UpdateLatestActivity(ctx context.Context, sandboxID string) error {
	key := keyPrefixSession + sandboxID

	exists, err := s.client.Exists(ctx, key).Result()
	if err != nil {
		return err
	}
	if exists == 0 {
		return ErrSessionNotFound
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
func (s *SessionStore) GetSession(ctx context.Context, sandboxID string) (*SandboxInfo, error) {
	key := keyPrefixSession + sandboxID

	data, err := s.client.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, ErrSessionNotFound
		}
		return nil, err
	}

	var info SandboxInfo
	if err := json.Unmarshal([]byte(data), &info); err != nil {
		return nil, err
	}

	return &info, nil
}
