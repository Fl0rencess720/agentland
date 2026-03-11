package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const keyPrefixPreview = "agentland:preview:"

var ErrPreviewNotFound = fmt.Errorf("preview not found")

type PreviewInfo struct {
	SessionID string `json:"session_id"`
	Port      int    `json:"port"`
}

type PreviewStore struct {
	client *redis.Client
}

func NewPreviewStore() *PreviewStore {
	return &PreviewStore{client: NewRedis()}
}

func (s *PreviewStore) Create(ctx context.Context, token string, info *PreviewInfo, ttl time.Duration) error {
	if info == nil {
		return fmt.Errorf("preview info is required")
	}
	data, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, keyPrefixPreview+token, data, ttl).Err()
}

func (s *PreviewStore) Get(ctx context.Context, token string) (*PreviewInfo, error) {
	data, err := s.client.Get(ctx, keyPrefixPreview+token).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, ErrPreviewNotFound
		}
		return nil, err
	}

	var info PreviewInfo
	if err := json.Unmarshal([]byte(data), &info); err != nil {
		return nil, err
	}
	return &info, nil
}
