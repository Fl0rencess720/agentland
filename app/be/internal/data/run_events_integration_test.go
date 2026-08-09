package data

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestKafkaRunEventProjectionResumeConflictAndTTL(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	viper.Set("database.url", dsn)
	t.Cleanup(func() { viper.Set("database.url", "") })
	ctx := context.Background()
	auth := &authStore{}
	require.NoError(t, auth.ensureSchema(ctx))
	projects := &projectRepo{}
	_, err := projects.ready(ctx)
	require.NoError(t, err)
	runs := &runRepo{leaseStore: newMemoryWorkerLeaseStore()}
	pool, err := runs.ready(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Close()
		projects.pool.Close()
		auth.pool.Close()
	})
	ownerID, projectID := seedKafkaEventProject(t, pool)
	runID := "run-event-" + uuid.NewString()
	now := time.Now().UTC()
	_, _, err = runs.CreateRun(ctx, &models.CreateRunInput{ID: runID, OwnerID: ownerID, ProjectID: projectID, IdempotencyKey: runID, InputMessageID: "input-" + runID, AssistantMessageID: "assistant-" + runID, Message: "hello", Now: now})
	require.NoError(t, err)

	store := newKafkaRunEventStore()
	store.repo = runs
	first := &models.AgentEvent{Type: "run.started", RunID: runID, Sequence: 1, Timestamp: now, Payload: json.RawMessage(`{}`)}
	firstPayload, err := json.Marshal(runEventEnvelope{RunID: runID, Event: first})
	require.NoError(t, err)
	require.NoError(t, store.project(ctx, firstPayload))
	require.NoError(t, store.project(ctx, firstPayload))

	all, err := store.Read(ctx, runID, "0", 0)
	require.NoError(t, err)
	require.Len(t, all, 1)
	require.Equal(t, "1", all[0].ID)

	conflict := *first
	conflict.Type = "tool.started"
	conflictPayload, err := json.Marshal(runEventEnvelope{RunID: runID, Event: &conflict})
	require.NoError(t, err)
	require.NoError(t, store.project(ctx, conflictPayload))

	terminal := &models.AgentEvent{Type: "run.completed", RunID: runID, Sequence: 2, Timestamp: now.Add(time.Second), Payload: json.RawMessage(`{}`)}
	terminalPayload, err := json.Marshal(runEventEnvelope{RunID: runID, Event: terminal})
	require.NoError(t, err)
	require.NoError(t, store.project(ctx, terminalPayload))
	resumed, err := store.Read(ctx, runID, "1", 0)
	require.NoError(t, err)
	require.Len(t, resumed, 1)
	require.Equal(t, "run.completed", resumed[0].Type)
	var expiring int
	require.NoError(t, pool.QueryRow(ctx, `select count(*) from run_events where run_id=$1 and expires_at is not null`, runID).Scan(&expiring))
	require.Equal(t, 2, expiring)
}

func seedKafkaEventProject(t *testing.T, pool *pgxpool.Pool) (string, string) {
	t.Helper()
	ownerID, projectID := "event-owner-"+uuid.NewString(), "event-project-"+uuid.NewString()
	now := time.Now().UTC()
	_, err := pool.Exec(context.Background(), `insert into users(id,email,name,avatar_url,plan,status,created_at,updated_at) values($1,$2,$1,'','free','active',$3,$3)`, ownerID, ownerID+"@example.com", now)
	require.NoError(t, err)
	_, err = pool.Exec(context.Background(), `insert into projects(id,owner_id,name,template,status,thumbnail_url,metadata,created_at,updated_at) values($1,$2,'event','blank','DRAFT','','{}',$3,$3)`, projectID, ownerID, now)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `delete from project_messages where owner_id=$1`, ownerID)
		_, _ = pool.Exec(context.Background(), `delete from agent_runs where owner_id=$1`, ownerID)
		_, _ = pool.Exec(context.Background(), `delete from project_publications where owner_id=$1`, ownerID)
		_, _ = pool.Exec(context.Background(), `delete from projects where id=$1`, projectID)
		_, _ = pool.Exec(context.Background(), `delete from users where id=$1`, ownerID)
	})
	return ownerID, projectID
}

func init() { viper.SetDefault("kafka.event_topic", "agentland.app.run-events") }
