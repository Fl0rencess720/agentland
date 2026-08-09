package data

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestKafkaDirectTaskAndIdempotentEventProjection(t *testing.T) {
	dsn, brokers, redisAddress := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_KAFKA_BROKERS"), os.Getenv("TEST_REDIS_ADDR")
	if dsn == "" || brokers == "" || redisAddress == "" {
		t.Skip("TEST_DATABASE_URL, TEST_KAFKA_BROKERS, and TEST_REDIS_ADDR are required")
	}
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	settings := map[string]any{
		"database.url": dsn, "kafka.brokers": strings.Split(brokers, ","), "redis.addr": redisAddress,
		"kafka.run_topic": "agentland.test.run." + suffix, "kafka.publication_topic": "agentland.test.publication." + suffix,
		"kafka.event_topic": "agentland.test.events." + suffix, "kafka.run_consumer_group": "agentland.test.run-workers." + suffix,
		"kafka.publication_consumer_group": "agentland.test.publication-workers." + suffix, "kafka.event_projector_group": "agentland.test.projector." + suffix,
		"kafka.task_partitions": 2, "kafka.event_partitions": 2, "kafka.replication_factor": 1, "kafka.event_retention": 24 * time.Hour,
	}
	for key, value := range settings {
		viper.Set(key, value)
	}
	t.Cleanup(func() {
		for key := range settings {
			viper.Set(key, nil)
		}
	})
	ctx := context.Background()
	auth := &authStore{}
	require.NoError(t, auth.ensureSchema(ctx))
	projects := &projectRepo{}
	_, err := projects.ready(ctx)
	require.NoError(t, err)
	redisClient := redis.NewClient(&redis.Options{Addr: redisAddress})
	require.NoError(t, redisClient.Ping(ctx).Err())
	runs := &runRepo{leaseStore: &redisWorkerLeaseStore{client: redisClient}}
	pool, err := runs.ready(ctx)
	require.NoError(t, err)
	pipeline, err := newKafkaPipeline(ctx, runs)
	require.NoError(t, err)
	t.Cleanup(func() {
		pipeline.Close()
		_ = redisClient.Close()
		pool.Close()
		projects.pool.Close()
		auth.pool.Close()
	})

	ownerID, projectID := seedKafkaEventProject(t, pool)
	now := time.Now().UTC()
	runID := "run-kafka-" + suffix
	_, _, err = runs.CreateRun(ctx, &models.CreateRunInput{
		ID: runID, OwnerID: ownerID, ProjectID: projectID, IdempotencyKey: runID,
		InputMessageID: "input-" + runID, AssistantMessageID: "assistant-" + runID, Message: "build", Now: now,
	})
	require.NoError(t, err)
	require.NoError(t, pipeline.PublishRunTask(ctx, runID, projectID))
	receiveCtx, cancelReceive := context.WithTimeout(ctx, 15*time.Second)
	delivery, err := pipeline.runQueue.Receive(receiveCtx)
	cancelReceive()
	require.NoError(t, err)
	require.Equal(t, runID, delivery.ID())
	require.NoError(t, delivery.Ack(ctx))

	delta, _ := json.Marshal(map[string]string{"content": "hello"})
	events := []*models.AgentEvent{
		{Type: "message.delta", RunID: runID, ConversationID: projectID, Sequence: 1, Timestamp: now.Add(time.Second), Payload: delta},
		{Type: "run.completed", RunID: runID, ConversationID: projectID, Sequence: 2, Timestamp: now.Add(2 * time.Second), Payload: json.RawMessage(`{}`)},
	}
	for _, event := range events {
		require.NoError(t, pipeline.PublishRunEvent(ctx, event))
		projectCtx, cancelProject := context.WithTimeout(ctx, 15*time.Second)
		require.NoError(t, pipeline.projectNext(projectCtx))
		cancelProject()
	}
	require.NoError(t, pipeline.PublishRunEvent(ctx, events[0]))
	projectCtx, cancelProject := context.WithTimeout(ctx, 15*time.Second)
	require.NoError(t, pipeline.projectNext(projectCtx))
	cancelProject()

	var projected int
	var assistant, status string
	require.NoError(t, pool.QueryRow(ctx, `select count(*) from run_events where run_id=$1`, runID).Scan(&projected))
	require.NoError(t, pool.QueryRow(ctx, `select message.content,run.status from agent_runs run join project_messages message on message.id=run.assistant_message_id where run.id=$1`, runID).Scan(&assistant, &status))
	require.Equal(t, 2, projected)
	require.Equal(t, "hello", assistant)
	require.Equal(t, models.RunStatusCompleted, status)

	publicationID := "publication-kafka-" + suffix
	_, _, err = runs.CreatePublication(ctx, &models.CreatePublicationInput{
		ID: publicationID, OwnerID: ownerID, ProjectID: projectID, IdempotencyKey: publicationID,
		Context: ".", Dockerfile: "Dockerfile", Now: now.Add(3 * time.Second),
	})
	require.NoError(t, err)
	require.NoError(t, pipeline.PublishPublicationTask(ctx, publicationID, projectID))
	receiveCtx, cancelReceive = context.WithTimeout(ctx, 15*time.Second)
	publicationDelivery, err := pipeline.pubQueue.Receive(receiveCtx)
	cancelReceive()
	require.NoError(t, err)
	require.Equal(t, publicationID, publicationDelivery.ID())
	require.NoError(t, publicationDelivery.Ack(ctx))
}
