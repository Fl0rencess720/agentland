package data

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestRedisRunEventsResumeAndTerminalTTL(t *testing.T) {
	address := os.Getenv("TEST_REDIS_ADDR")
	if address == "" {
		t.Skip("TEST_REDIS_ADDR is not set")
	}
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	require.NoError(t, client.Ping(ctx).Err())
	store := &redisRunEventStore{client: client}
	runID := "integration-" + uuid.NewString()
	t.Cleanup(func() { _ = client.Del(context.Background(), runEventKey(runID)).Err() })

	firstID, err := store.Publish(ctx, runID, &models.AgentEvent{Type: "run.started", RunID: runID, Sequence: 1, Timestamp: time.Now().UTC(), Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)
	_, err = store.Publish(ctx, runID, &models.AgentEvent{Type: "run.completed", RunID: runID, Sequence: 2, Timestamp: time.Now().UTC(), Payload: json.RawMessage(`{}`)})
	require.NoError(t, err)

	all, err := store.Read(ctx, runID, "0-0", 10*time.Millisecond)
	require.NoError(t, err)
	require.Len(t, all, 2)
	resumed, err := store.Read(ctx, runID, firstID, 10*time.Millisecond)
	require.NoError(t, err)
	require.Len(t, resumed, 1)
	require.Equal(t, "run.completed", resumed[0].Type)

	require.NoError(t, store.Expire(ctx, runID, 24*time.Hour))
	ttl, err := client.TTL(ctx, runEventKey(runID)).Result()
	require.NoError(t, err)
	require.Greater(t, ttl, 23*time.Hour)
	require.LessOrEqual(t, ttl, 24*time.Hour)
}
