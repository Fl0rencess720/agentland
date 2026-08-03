package data

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/viper"
)

type redisRunEventStore struct {
	client *redis.Client
}

func NewRunEventStore() biz.RunEventStore {
	return &redisRunEventStore{client: redis.NewClient(&redis.Options{
		Addr: viper.GetString("redis.addr"), Password: viper.GetString("redis.password"), DB: viper.GetInt("redis.db"),
	})}
}

func (s *redisRunEventStore) Publish(ctx context.Context, runID string, event *models.AgentEvent) (string, error) {
	payload, err := json.Marshal(event)
	if err != nil {
		return "", err
	}
	return s.client.XAdd(ctx, &redis.XAddArgs{
		Stream: runEventKey(runID),
		Values: map[string]any{"event": event.Type, "data": string(payload)},
	}).Result()
}

func (s *redisRunEventStore) Read(ctx context.Context, runID, after string, block time.Duration) ([]*models.StoredRunEvent, error) {
	results, err := s.client.XRead(ctx, &redis.XReadArgs{Streams: []string{runEventKey(runID), after}, Count: 200, Block: block}).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	events := make([]*models.StoredRunEvent, 0)
	for _, stream := range results {
		for _, message := range stream.Messages {
			typeValue := fmt.Sprint(message.Values["event"])
			dataValue := fmt.Sprint(message.Values["data"])
			events = append(events, &models.StoredRunEvent{ID: message.ID, Type: typeValue, Data: json.RawMessage(dataValue)})
		}
	}
	return events, nil
}

func (s *redisRunEventStore) Expire(ctx context.Context, runID string, ttl time.Duration) error {
	return s.client.Expire(ctx, runEventKey(runID), ttl).Err()
}

func runEventKey(runID string) string {
	return "agentland:app:run:" + strings.TrimSpace(runID) + ":events"
}
