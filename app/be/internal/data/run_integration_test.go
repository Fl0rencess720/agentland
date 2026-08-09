package data

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/google/uuid"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestRunRepositoryCreatesRunningRunAndUsesRedisOwnership(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	viper.Set("database.url", dsn)
	viper.Set("worker.orphan_timeout", 10*time.Millisecond)
	t.Cleanup(func() {
		viper.Set("database.url", "")
		viper.Set("worker.orphan_timeout", nil)
	})
	ctx := context.Background()
	auth := &authStore{}
	require.NoError(t, auth.ensureSchema(ctx))
	projects := &projectRepo{}
	_, err := projects.ready(ctx)
	require.NoError(t, err)
	leases := newMemoryWorkerLeaseStore()
	runs := &runRepo{leaseStore: leases}
	pool, err := runs.ready(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		pool.Close()
		projects.pool.Close()
		auth.pool.Close()
	})

	ownerID, projectID := seedKafkaEventProject(t, pool)
	now := time.Now().UTC()
	input := &models.CreateRunInput{
		ID: "run-" + uuid.NewString(), OwnerID: ownerID, ProjectID: projectID, IdempotencyKey: "key-" + uuid.NewString(),
		InputMessageID: "input-" + uuid.NewString(), AssistantMessageID: "assistant-" + uuid.NewString(), Message: "build", Now: now,
	}
	run, existing, err := runs.CreateRun(ctx, input)
	require.NoError(t, err)
	require.False(t, existing)
	require.Equal(t, models.RunStatusRunning, run.Status)
	require.Equal(t, run.ID, run.AgentRunID)

	duplicate, existing, err := runs.CreateRun(ctx, input)
	require.NoError(t, err)
	require.True(t, existing)
	require.Equal(t, run.ID, duplicate.ID)

	conflict := *input
	conflict.ID, conflict.IdempotencyKey = "run-"+uuid.NewString(), "key-"+uuid.NewString()
	conflict.InputMessageID, conflict.AssistantMessageID = "input-"+uuid.NewString(), "assistant-"+uuid.NewString()
	_, _, err = runs.CreateRun(ctx, &conflict)
	require.ErrorIs(t, err, biz.ErrActiveRun)

	owned, err := runs.AcquireRunOwnership(ctx, run.ID, "worker-a")
	require.NoError(t, err)
	require.True(t, owned)
	owned, err = runs.AcquireRunOwnership(ctx, run.ID, "worker-b")
	require.NoError(t, err)
	require.False(t, owned)
	require.Eventually(t, func() bool {
		expired, listErr := runs.ExpiredRunOwnerships(ctx, time.Now(), 10)
		return listErr == nil && len(expired) == 1 && expired[0].OwnerID == "worker-a"
	}, time.Second, 5*time.Millisecond)
	taken, err := runs.TakeoverRunOwnership(ctx, run.ID, "worker-a", "worker-b")
	require.NoError(t, err)
	require.True(t, taken)
	renewed, err := runs.RenewRunOwnership(ctx, run.ID, "worker-a")
	require.NoError(t, err)
	require.False(t, renewed)
	renewed, err = runs.RenewRunOwnership(ctx, run.ID, "worker-b")
	require.NoError(t, err)
	require.True(t, renewed)

	var removedColumns int
	require.NoError(t, pool.QueryRow(ctx, `select count(*) from information_schema.columns where table_name='agent_runs' and column_name in ('worker_id','heartbeat_at')`).Scan(&removedColumns))
	require.Zero(t, removedColumns)
}
