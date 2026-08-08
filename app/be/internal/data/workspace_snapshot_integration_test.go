package data

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/google/uuid"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestWorkspaceSnapshotMetadataPostgresIntegration(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	viper.Set("database.url", dsn)
	viper.Set("storage.s3.key_prefix", "agentland")
	viper.Set("storage.s3.max_snapshot_bytes", int64(8<<20))
	t.Cleanup(func() {
		viper.Set("database.url", "")
		viper.Set("storage.s3.key_prefix", "")
		viper.Set("storage.s3.max_snapshot_bytes", 0)
	})
	ctx := context.Background()
	auth := &authStore{}
	require.NoError(t, auth.ensureSchema(ctx))
	projects := &projectRepo{}
	_, err := projects.ready(ctx)
	require.NoError(t, err)
	objects := &snapshotObjectStoreStub{}
	runs := &runRepo{snapshotStore: objects}
	pool, err := runs.ready(ctx)
	require.NoError(t, err)

	suffix := uuid.NewString()
	ownerID := "snapshot-user-" + suffix
	projectID := "snapshot-project-" + suffix
	legacyProjectID := "snapshot-legacy-project-" + suffix
	now := time.Now().UTC()
	_, err = pool.Exec(ctx, `insert into users(id,email,name,avatar_url,plan,status,created_at,updated_at) values($1,$2,'test','','free','active',$3,$3)`, ownerID, suffix+"@example.com", now)
	require.NoError(t, err)
	for _, id := range []string{projectID, legacyProjectID} {
		_, err = pool.Exec(ctx, `insert into projects(id,owner_id,name,template,status,thumbnail_url,metadata,created_at,updated_at) values($1,$2,'snapshot','blank','DRAFT','','{}',$3,$3)`, id, ownerID, now)
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `delete from project_messages where owner_id=$1`, ownerID)
		_, _ = pool.Exec(context.Background(), `delete from agent_runs where owner_id=$1`, ownerID)
		_, _ = pool.Exec(context.Background(), `delete from projects where owner_id=$1`, ownerID)
		_, _ = pool.Exec(context.Background(), `delete from users where id=$1`, ownerID)
		pool.Close()
		projects.pool.Close()
		auth.pool.Close()
	})

	createRun := func(project, key string) *models.Run {
		id := uuid.NewString()
		run, _, createErr := runs.CreateRun(ctx, &models.CreateRunInput{
			ID: id, OwnerID: ownerID, ProjectID: project, IdempotencyKey: key,
			InputMessageID: "user-" + id, AssistantMessageID: "assistant-" + id, Message: "snapshot", Now: now,
		})
		require.NoError(t, createErr)
		return run
	}

	run := createRun(projectID, "object")
	_, err = pool.Exec(ctx, `update agent_runs set status=$2,worker_id='worker-1' where id=$1`, run.ID, models.RunStatusRunning)
	require.NoError(t, err)
	data := []byte("compressed workspace")
	digest := sha256.Sum256(data)
	sha := hex.EncodeToString(digest[:])
	saved, err := runs.SaveWorkspaceSnapshot(ctx, run.ID, "worker-1", data, sha, "", now)
	require.NoError(t, err)
	require.True(t, saved)
	var contentIsNull bool
	var objectKey, storedSHA string
	var size int64
	require.NoError(t, pool.QueryRow(ctx, `select content is null,object_key,sha256,size_bytes from run_workspace_snapshots where run_id=$1`, run.ID).
		Scan(&contentIsNull, &objectKey, &storedSHA, &size))
	require.True(t, contentIsNull)
	require.Equal(t, snapshotObjectKey("agentland", sha), objectKey)
	require.Equal(t, sha, storedSHA)
	require.Equal(t, int64(len(data)), size)
	loaded, err := runs.LoadWorkspaceSnapshot(ctx, run.ID)
	require.NoError(t, err)
	require.Equal(t, data, loaded.Data)

	legacyRun := createRun(legacyProjectID, "legacy")
	legacyData := []byte("legacy bytea")
	legacyDigest := sha256.Sum256(legacyData)
	legacySHA := hex.EncodeToString(legacyDigest[:])
	_, err = pool.Exec(ctx, `insert into run_workspace_snapshots(run_id,content,sha256,capture_error,created_at) values($1,$2,$3,'',$4)`, legacyRun.ID, legacyData, legacySHA, now)
	require.NoError(t, err)
	legacy, err := runs.LoadWorkspaceSnapshot(ctx, legacyRun.ID)
	require.NoError(t, err)
	require.Equal(t, legacyData, legacy.Data)
}
