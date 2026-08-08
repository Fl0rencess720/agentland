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

func TestKafkaOutboxTaskConsumptionAndEventProjection(t *testing.T) {
	dsn, brokers, redisAddress := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_KAFKA_BROKERS"), os.Getenv("TEST_REDIS_ADDR")
	if dsn == "" || brokers == "" || redisAddress == "" {
		t.Skip("TEST_DATABASE_URL, TEST_KAFKA_BROKERS, and TEST_REDIS_ADDR are required")
	}
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	settings := map[string]any{
		"database.url": dsn, "kafka.brokers": strings.Split(brokers, ","),
		"redis.addr":      redisAddress,
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
	_, err = pool.Exec(ctx, `delete from kafka_outbox`)
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
	run, _, err := runs.CreateRun(ctx, &models.CreateRunInput{
		ID: runID, OwnerID: ownerID, ProjectID: projectID, IdempotencyKey: runID,
		InputMessageID: "input-" + runID, AssistantMessageID: "assistant-" + runID, Message: "build", Now: now,
	})
	require.NoError(t, err)
	var pending int
	require.NoError(t, pool.QueryRow(ctx, `select count(*) from kafka_outbox where dedupe_key=$1 and published_at is null`, outboxKindRunTask+":"+runID).Scan(&pending))
	require.Equal(t, 1, pending)
	require.NoError(t, pipeline.relayOnce(ctx))

	receiveCtx, cancelReceive := context.WithTimeout(ctx, 15*time.Second)
	delivery, err := pipeline.runQueue.Receive(receiveCtx)
	cancelReceive()
	require.NoError(t, err)
	require.Equal(t, runID, delivery.ID())
	claimed, err := runs.ClaimRun(ctx, runID, "integration-worker", now.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, models.RunStatusRunning, claimed.Status)
	require.NoError(t, delivery.Ack(ctx))

	event := &models.AgentEvent{Type: "run.completed", RunID: runID, ConversationID: projectID, Sequence: 1, Timestamp: now.Add(2 * time.Second), Payload: json.RawMessage(`{}`)}
	finished, err := runs.FinishRun(ctx, runID, "integration-worker", models.RunStatusCompleted, "", "", 1, now.Add(2*time.Second), event)
	require.NoError(t, err)
	require.True(t, finished)
	require.NoError(t, pipeline.relayOnce(ctx))

	projectCtx, cancelProject := context.WithTimeout(ctx, 15*time.Second)
	require.NoError(t, pipeline.projectNext(projectCtx))
	cancelProject()
	events, err := pipeline.events.Read(ctx, runID, "0", 0)
	require.NoError(t, err)
	require.Len(t, events, 1)
	require.Equal(t, "1", events[0].ID)
	require.Equal(t, "run.completed", events[0].Type)

	// A relay crash after Kafka accepted the record can publish it again. The projector must keep one row.
	_, err = pool.Exec(ctx, `update kafka_outbox set published_at=null where dedupe_key=$1`, runID+":1")
	require.NoError(t, err)
	require.NoError(t, pipeline.relayOnce(ctx))
	projectCtx, cancelProject = context.WithTimeout(ctx, 15*time.Second)
	require.NoError(t, pipeline.projectNext(projectCtx))
	cancelProject()
	var projected int
	require.NoError(t, pool.QueryRow(ctx, `select count(*) from run_events where run_id=$1 and sequence=1`, runID).Scan(&projected))
	require.Equal(t, 1, projected)

	publicationID := "publication-kafka-" + suffix
	publication, _, err := runs.CreatePublication(ctx, &models.CreatePublicationInput{
		ID: publicationID, OwnerID: ownerID, ProjectID: projectID, IdempotencyKey: publicationID,
		Context: ".", Dockerfile: "Dockerfile", Now: now.Add(3 * time.Second),
	})
	require.NoError(t, err)
	require.NoError(t, pipeline.relayOnce(ctx))
	receiveCtx, cancelReceive = context.WithTimeout(ctx, 15*time.Second)
	publicationDelivery, err := pipeline.pubQueue.Receive(receiveCtx)
	cancelReceive()
	require.NoError(t, err)
	require.Equal(t, publication.ID, publicationDelivery.ID())
	claimedPublication, err := runs.ClaimPublication(ctx, publication.ID, "integration-publisher", now.Add(4*time.Second))
	require.NoError(t, err)
	require.Equal(t, models.PublicationStatusRunning, claimedPublication.Status)
	require.NoError(t, publicationDelivery.Ack(ctx))
	publicationFinished, err := runs.FinishPublication(ctx, &models.FinishPublicationInput{
		ID: publication.ID, WorkerID: "integration-publisher", Status: models.PublicationStatusCompleted,
		ImageRef: "registry.example/app:latest", Digest: "sha256:integration", Now: now.Add(5 * time.Second),
	})
	require.NoError(t, err)
	require.True(t, publicationFinished)

	// Simulate an upgrade where queued rows predate the Outbox table migration.
	legacyID := "legacy-run-" + suffix
	_, err = pool.Exec(ctx, `insert into agent_runs(id,owner_id,project_id,idempotency_key,input_message_id,assistant_message_id,status,created_at,updated_at)
		values($1,$2,$3,$1,$4,$5,'queued',$6,$6)`, legacyID, ownerID, projectID, "input-"+legacyID, "assistant-"+legacyID, now.Add(3*time.Second))
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `delete from kafka_outbox where dedupe_key=$1`, outboxKindRunTask+":"+legacyID)
	require.NoError(t, err)
	require.NoError(t, runs.backfillKafkaTasks(ctx))
	require.NoError(t, pool.QueryRow(ctx, `select count(*) from kafka_outbox where dedupe_key=$1`, outboxKindRunTask+":"+legacyID).Scan(&pending))
	require.Equal(t, 1, pending)

	_ = run
}
