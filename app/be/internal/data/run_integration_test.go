package data

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/google/uuid"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestRunRepositoryPostgresConcurrency(t *testing.T) {
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
	leaseStore := newMemoryWorkerLeaseStore()
	runs := &runRepo{leaseStore: leaseStore}
	pool, err := runs.ready(ctx)
	require.NoError(t, err)

	suffix := uuid.NewString()
	ownerID := "test-user-" + suffix
	projectOne := "test-project-one-" + suffix
	projectTwo := "test-project-two-" + suffix
	now := time.Now().UTC()
	_, err = pool.Exec(ctx, `insert into users(id,email,name,avatar_url,plan,status,created_at,updated_at) values($1,$2,'test','','free','active',$3,$3)`, ownerID, suffix+"@example.com", now)
	require.NoError(t, err)
	for _, projectID := range []string{projectOne, projectTwo} {
		_, err = pool.Exec(ctx, `insert into projects(id,owner_id,name,template,status,thumbnail_url,metadata,created_at,updated_at) values($1,$2,'test','blank','DRAFT','','{}',$3,$3)`, projectID, ownerID, now)
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `delete from project_publications where owner_id=$1`, ownerID)
		_, _ = pool.Exec(context.Background(), `delete from project_previews where owner_id=$1`, ownerID)
		_, _ = pool.Exec(context.Background(), `delete from project_runtimes where owner_id=$1`, ownerID)
		_, _ = pool.Exec(context.Background(), `delete from project_messages where owner_id=$1`, ownerID)
		_, _ = pool.Exec(context.Background(), `delete from agent_runs where owner_id=$1`, ownerID)
		_, _ = pool.Exec(context.Background(), `delete from projects where owner_id=$1`, ownerID)
		_, _ = pool.Exec(context.Background(), `delete from users where id=$1`, ownerID)
		pool.Close()
		projects.pool.Close()
		auth.pool.Close()
	})

	makePublication := func(projectID, key string) *models.CreatePublicationInput {
		return &models.CreatePublicationInput{
			ID: "pub-" + uuid.NewString(), OwnerID: ownerID, ProjectID: projectID, IdempotencyKey: key,
			Context: ".", Dockerfile: "Dockerfile", Now: now,
		}
	}

	makeInput := func(projectID, key, message string) *models.CreateRunInput {
		id := uuid.NewString()
		return &models.CreateRunInput{
			ID: id, OwnerID: ownerID, ProjectID: projectID, IdempotencyKey: key,
			InputMessageID: "user-" + id, AssistantMessageID: "assistant-" + id,
			Message: message, TraceParent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", TraceState: "vendor=value", Now: now,
		}
	}

	t.Run("run claim rolls back when Redis lease creation fails", func(t *testing.T) {
		queued, _, createErr := runs.CreateRun(ctx, makeInput(projectOne, "lease-failure", "message"))
		require.NoError(t, createErr)
		leaseStore.acquireErr = errors.New("redis unavailable")
		claimed, claimErr := runs.ClaimRun(ctx, queued.ID, "worker-failed", now.Add(4*time.Minute))
		leaseStore.acquireErr = nil
		require.ErrorContains(t, claimErr, "redis unavailable")
		require.Nil(t, claimed)
		stored, getErr := runs.GetRun(ctx, ownerID, queued.ID)
		require.NoError(t, getErr)
		require.Equal(t, models.RunStatusQueued, stored.Status)
		require.Empty(t, stored.WorkerID)
		_, _, cancelErr := runs.RequestCancel(ctx, ownerID, queued.ID, now.Add(5*time.Minute))
		require.NoError(t, cancelErr)
	})

	t.Run("run delivery remains queued while a ghost Redis lease exists", func(t *testing.T) {
		queued, _, createErr := runs.CreateRun(ctx, makeInput(projectOne, "ghost-lease", "message"))
		require.NoError(t, createErr)
		acquired, acquireErr := leaseStore.Acquire(ctx, runLeaseKind, queued.ID, "ghost-worker", time.Minute)
		require.NoError(t, acquireErr)
		require.True(t, acquired)
		claimed, claimErr := runs.ClaimRun(ctx, queued.ID, "worker-new", now.Add(4*time.Minute))
		require.ErrorIs(t, claimErr, biz.ErrWorkerLeaseBusy)
		require.Nil(t, claimed)
		stored, getErr := runs.GetRun(ctx, ownerID, queued.ID)
		require.NoError(t, getErr)
		require.Equal(t, models.RunStatusQueued, stored.Status)
		require.Empty(t, stored.WorkerID)
		_, _ = leaseStore.Release(ctx, runLeaseKind, queued.ID, "ghost-worker")
		_, _, cancelErr := runs.RequestCancel(ctx, ownerID, queued.ID, now.Add(5*time.Minute))
		require.NoError(t, cancelErr)
	})

	t.Run("concurrent idempotency returns one run", func(t *testing.T) {
		start := make(chan struct{})
		type result struct {
			run *models.Run
			err error
		}
		results := make(chan result, 2)
		var workers sync.WaitGroup
		for range 2 {
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				run, _, createErr := runs.CreateRun(ctx, makeInput(projectOne, "same-key", "same message"))
				results <- result{run: run, err: createErr}
			}()
		}
		close(start)
		workers.Wait()
		close(results)
		var runID string
		for result := range results {
			require.NoError(t, result.err)
			require.NotNil(t, result.run)
			if runID == "" {
				runID = result.run.ID
			}
			require.Equal(t, runID, result.run.ID)
		}
		var messages int
		require.NoError(t, pool.QueryRow(ctx, `select count(*) from project_messages where run_id=$1`, runID).Scan(&messages))
		require.Equal(t, 2, messages)
		cancelled, transitioned, cancelErr := runs.RequestCancel(ctx, ownerID, runID, now.Add(time.Second))
		require.NoError(t, cancelErr)
		require.True(t, transitioned)
		require.Equal(t, models.RunStatusCancelled, cancelled.Status)
	})

	t.Run("one active run per project and worker leases are fenced", func(t *testing.T) {
		start := make(chan struct{})
		errorsCh := make(chan error, 2)
		for index := range 2 {
			go func(index int) {
				<-start
				_, _, createErr := runs.CreateRun(ctx, makeInput(projectOne, "active-key-"+string(rune('a'+index)), "message"))
				errorsCh <- createErr
			}(index)
		}
		close(start)
		firstErr, secondErr := <-errorsCh, <-errorsCh
		successes, conflicts := 0, 0
		for _, createErr := range []error{firstErr, secondErr} {
			if createErr == nil {
				successes++
			} else if errors.Is(createErr, biz.ErrActiveRun) {
				conflicts++
			}
		}
		require.Equal(t, 1, successes)
		require.Equal(t, 1, conflicts)

		projectTwoRun, _, err := runs.CreateRun(ctx, makeInput(projectTwo, "project-two", "message"))
		require.NoError(t, err)
		var projectOneRunID string
		require.NoError(t, pool.QueryRow(ctx, `select id from agent_runs where project_id=$1 and status=$2`, projectOne, models.RunStatusQueued).Scan(&projectOneRunID))
		claimTime := now.Add(2 * time.Minute)
		type claimResult struct {
			run *models.Run
			err error
		}
		claimed := make(chan claimResult, 2)
		for index, workerID := range []string{"worker-a", "worker-b"} {
			go func(runID, workerID string) {
				run, claimErr := runs.ClaimRun(ctx, runID, workerID, claimTime)
				claimed <- claimResult{run: run, err: claimErr}
			}([]string{projectOneRunID, projectTwoRun.ID}[index], workerID)
		}
		firstResult, secondResult := <-claimed, <-claimed
		require.NoError(t, firstResult.err)
		require.NoError(t, secondResult.err)
		first, second := firstResult.run, secondResult.run
		require.NotNil(t, first)
		require.NotNil(t, second)
		require.NotEqual(t, first.ID, second.ID)
		require.ElementsMatch(t, []string{projectOne, projectTwo}, []string{first.ProjectID, second.ProjectID})
		require.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", first.TraceParent)
		require.Equal(t, "vendor=value", first.TraceState)

		acquired, heartbeatErr := runs.Heartbeat(ctx, first.ID, first.WorkerID, claimTime.Add(time.Second))
		require.NoError(t, heartbeatErr)
		require.True(t, acquired)
		var storedHeartbeat time.Time
		require.NoError(t, pool.QueryRow(ctx, `select heartbeat_at from agent_runs where id=$1`, first.ID).Scan(&storedHeartbeat))
		require.WithinDuration(t, claimTime, storedHeartbeat, time.Millisecond)
		acquired, heartbeatErr = runs.Heartbeat(ctx, first.ID, "another-worker", claimTime.Add(time.Second))
		require.NoError(t, heartbeatErr)
		require.False(t, acquired)
		_, _ = leaseStore.Release(ctx, runLeaseKind, first.ID, first.WorkerID)
		_, _ = leaseStore.Release(ctx, runLeaseKind, second.ID, second.WorkerID)
		fenced, fenceErr := leaseStore.AcquireRecovery(ctx, runLeaseKind, []leaseCandidate{{ID: first.ID, OwnerID: first.WorkerID}}, "recovery:test", time.Minute)
		require.NoError(t, fenceErr)
		require.True(t, fenced[first.ID])
		finished, finishErr := runs.FinishRun(ctx, first.ID, first.WorkerID, models.RunStatusCompleted, "", "", first.LastSequence+1, claimTime.Add(time.Second), nil)
		require.NoError(t, finishErr)
		require.False(t, finished)
		_, _ = leaseStore.Release(ctx, runLeaseKind, first.ID, "recovery:test")
		leaseStore.recoveryErr = errors.New("redis unavailable")
		orphaned, orphanErr := runs.FailOrphanedRuns(ctx, claimTime.Add(time.Second), claimTime.Add(2*time.Second))
		require.ErrorContains(t, orphanErr, "redis unavailable")
		require.Empty(t, orphaned)
		stillRunning, getErr := runs.GetRun(ctx, ownerID, first.ID)
		require.NoError(t, getErr)
		require.Equal(t, models.RunStatusRunning, stillRunning.Status)
		leaseStore.recoveryErr = nil
		orphaned, orphanErr = runs.FailOrphanedRuns(ctx, claimTime.Add(time.Second), claimTime.Add(2*time.Second))
		require.NoError(t, orphanErr)
		require.Len(t, orphaned, 2)
		acquired, heartbeatErr = runs.Heartbeat(ctx, first.ID, first.WorkerID, claimTime.Add(3*time.Second))
		require.NoError(t, heartbeatErr)
		require.False(t, acquired)
		_, ownerErr := runs.GetRun(ctx, "another-owner", first.ID)
		require.ErrorIs(t, ownerErr, biz.ErrRunNotFound)
	})

	t.Run("run recovery scans beyond a full page of live leases", func(t *testing.T) {
		startedAt := now.Add(-time.Hour)
		for index := range 101 {
			projectID := fmt.Sprintf("page-project-%s-%03d", suffix, index)
			runID := fmt.Sprintf("page-run-%s-%03d", suffix, index)
			assistantID := "assistant-" + runID
			_, err = pool.Exec(ctx, `insert into projects(id,owner_id,name,template,status,thumbnail_url,metadata,created_at,updated_at) values($1,$2,'page','blank','DRAFT','','{}',$3,$3)`, projectID, ownerID, startedAt)
			require.NoError(t, err)
			_, err = pool.Exec(ctx, `insert into agent_runs(id,owner_id,project_id,idempotency_key,input_message_id,assistant_message_id,status,worker_id,created_at,updated_at,started_at,heartbeat_at)
				values($1,$2,$3,$1,'input-'||$1,$4,$5,$6,$7,$7,$7,$7)`, runID, ownerID, projectID, assistantID, models.RunStatusRunning, "page-worker", startedAt)
			require.NoError(t, err)
			_, err = pool.Exec(ctx, `insert into project_messages(id,project_id,owner_id,run_id,role,content,status,created_at,updated_at) values($1,$2,$3,$4,'assistant','','pending',$5,$5)`, assistantID, projectID, ownerID, runID, startedAt)
			require.NoError(t, err)
			if index < 100 {
				acquired, acquireErr := leaseStore.Acquire(ctx, runLeaseKind, runID, "page-worker", time.Minute)
				require.NoError(t, acquireErr)
				require.True(t, acquired)
			}
		}
		orphaned, orphanErr := runs.FailOrphanedRuns(ctx, now.Add(-time.Minute), now)
		require.NoError(t, orphanErr)
		require.Equal(t, []models.RunSequence{{RunID: fmt.Sprintf("page-run-%s-100", suffix), Sequence: 1}}, orphaned)
	})

	t.Run("preview activity only renews an unexpired preview", func(t *testing.T) {
		lastActive := now.Add(-2 * time.Minute)
		absoluteExpiry := now.Add(time.Hour)
		_, err = pool.Exec(ctx, `insert into project_runtimes(project_id,owner_id,gateway_session_id,agent_conversation_id,status,created_at,last_active_at,expires_at,updated_at)
		values($1,$2,'session','conversation',$3,$4,$5,$6,$5)
		on conflict(project_id) do update set status=excluded.status,last_active_at=excluded.last_active_at,expires_at=excluded.expires_at,updated_at=excluded.updated_at`, projectTwo, ownerID, models.RuntimeStatusActive, now, lastActive, absoluteExpiry)
		require.NoError(t, err)
		token := "preview-" + suffix
		require.NoError(t, runs.SavePreview(ctx, &models.ProjectPreview{
			ID: "preview-id-" + suffix, ProjectID: projectTwo, OwnerID: ownerID, Status: "running",
			PreviewURL: "http://" + token + ".localhost:18081/p/" + token + "/", PreviewToken: token, Port: 3000,
			CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(-time.Minute), UpdatedAt: now,
		}))

		require.NoError(t, runs.TouchRuntimeByPreviewToken(ctx, token, now))
		var observed, observedExpiry time.Time
		require.NoError(t, pool.QueryRow(ctx, `select last_active_at,expires_at from project_runtimes where project_id=$1`, projectTwo).Scan(&observed, &observedExpiry))
		require.WithinDuration(t, lastActive, observed, time.Millisecond)
		require.WithinDuration(t, absoluteExpiry, observedExpiry, time.Millisecond)

		activeAt := now.Add(time.Second)
		_, err = pool.Exec(ctx, `update project_previews set expires_at=$2 where project_id=$1`, projectTwo, activeAt.Add(time.Hour))
		require.NoError(t, err)
		require.NoError(t, runs.TouchRuntimeByPreviewToken(ctx, token, activeAt))
		require.NoError(t, pool.QueryRow(ctx, `select last_active_at,expires_at from project_runtimes where project_id=$1`, projectTwo).Scan(&observed, &observedExpiry))
		require.WithinDuration(t, activeAt, observed, time.Millisecond)
		require.WithinDuration(t, absoluteExpiry, observedExpiry, time.Millisecond)

		require.NoError(t, runs.TouchRuntime(ctx, projectTwo, activeAt.Add(time.Second)))
		require.NoError(t, pool.QueryRow(ctx, `select last_active_at,expires_at from project_runtimes where project_id=$1`, projectTwo).Scan(&observed, &observedExpiry))
		require.WithinDuration(t, activeAt.Add(time.Second), observed, time.Millisecond)
		require.WithinDuration(t, absoluteExpiry, observedExpiry, time.Millisecond)

		require.NoError(t, runs.UpsertRuntime(ctx, &models.ProjectRuntime{
			ProjectID: projectTwo, OwnerID: ownerID, GatewaySessionID: "session", AgentConversationID: "conversation",
			Status: models.RuntimeStatusActive, CreatedAt: now, LastActiveAt: activeAt.Add(2 * time.Second), ExpiresAt: absoluteExpiry.Add(time.Hour), UpdatedAt: activeAt.Add(2 * time.Second),
		}))
		require.NoError(t, pool.QueryRow(ctx, `select expires_at from project_runtimes where project_id=$1`, projectTwo).Scan(&observedExpiry))
		require.WithinDuration(t, absoluteExpiry, observedExpiry, time.Millisecond)
	})

	t.Run("publication idempotency, active constraint, and lease fencing", func(t *testing.T) {
		first, _, createErr := runs.CreatePublication(ctx, makePublication(projectOne, "publication-same-key"))
		require.NoError(t, createErr)
		repeated, repeatedExisting, repeatErr := runs.CreatePublication(ctx, &models.CreatePublicationInput{
			ID: "ignored", OwnerID: ownerID, ProjectID: projectOne, IdempotencyKey: "publication-same-key",
			Context: ".", Dockerfile: "Dockerfile", Now: now,
		})
		require.NoError(t, repeatErr)
		require.True(t, repeatedExisting)
		require.Equal(t, first.ID, repeated.ID)
		_, _, createErr = runs.CreatePublication(ctx, makePublication(projectOne, "publication-active-key"))
		require.ErrorIs(t, createErr, biz.ErrActivePublication)

		claimed, claimErr := runs.ClaimPublication(ctx, first.ID, "publication-worker", now.Add(time.Second))
		require.NoError(t, claimErr)
		require.Equal(t, first.ID, claimed.ID)
		acquired, heartbeatErr := runs.HeartbeatPublication(ctx, claimed.ID, "other-worker", now.Add(2*time.Second))
		require.NoError(t, heartbeatErr)
		require.False(t, acquired)
		acquired, heartbeatErr = runs.HeartbeatPublication(ctx, claimed.ID, "publication-worker", now.Add(2*time.Second))
		require.NoError(t, heartbeatErr)
		require.True(t, acquired)
		var storedHeartbeat time.Time
		require.NoError(t, pool.QueryRow(ctx, `select heartbeat_at from project_publications where id=$1`, claimed.ID).Scan(&storedHeartbeat))
		require.WithinDuration(t, now.Add(time.Second), storedHeartbeat, time.Millisecond)
		finished, finishErr := runs.FinishPublication(ctx, &models.FinishPublicationInput{
			ID: claimed.ID, WorkerID: "publication-worker", Status: models.PublicationStatusCompleted,
			ImageRef: "registry.example/apps/project:pub", Digest: "sha256:digest", Logs: "done", Now: now.Add(3 * time.Second),
		})
		require.NoError(t, finishErr)
		require.True(t, finished)
		renewed, renewErr := runs.HeartbeatPublication(ctx, claimed.ID, "publication-worker", now.Add(4*time.Second))
		require.NoError(t, renewErr)
		require.False(t, renewed)
		stored, getErr := runs.GetPublication(ctx, ownerID, claimed.ID)
		require.NoError(t, getErr)
		require.Equal(t, "sha256:digest", stored.Digest)
	})

	t.Run("publication recovery requires an expired Redis lease", func(t *testing.T) {
		queued, _, createErr := runs.CreatePublication(ctx, makePublication(projectOne, "publication-recovery"))
		require.NoError(t, createErr)
		claimed, claimErr := runs.ClaimPublication(ctx, queued.ID, "publication-recovery-worker", now.Add(4*time.Minute))
		require.NoError(t, claimErr)
		require.Equal(t, queued.ID, claimed.ID)
		recovered, recoveryErr := runs.FailOrphanedPublications(ctx, now.Add(4*time.Minute+time.Second), now.Add(4*time.Minute+2*time.Second))
		require.NoError(t, recoveryErr)
		require.Zero(t, recovered)
		_, _ = leaseStore.Release(ctx, publicationLeaseKind, claimed.ID, claimed.WorkerID)
		fenced, fenceErr := leaseStore.AcquireRecovery(ctx, publicationLeaseKind, []leaseCandidate{{ID: claimed.ID, OwnerID: claimed.WorkerID}}, "recovery:publication-test", time.Minute)
		require.NoError(t, fenceErr)
		require.True(t, fenced[claimed.ID])
		finished, finishErr := runs.FinishPublication(ctx, &models.FinishPublicationInput{ID: claimed.ID, WorkerID: claimed.WorkerID, Status: models.PublicationStatusCompleted, Now: now.Add(4*time.Minute + time.Second)})
		require.NoError(t, finishErr)
		require.False(t, finished)
		_, _ = leaseStore.Release(ctx, publicationLeaseKind, claimed.ID, "recovery:publication-test")
		recovered, recoveryErr = runs.FailOrphanedPublications(ctx, now.Add(4*time.Minute+time.Second), now.Add(4*time.Minute+2*time.Second))
		require.NoError(t, recoveryErr)
		require.Equal(t, int64(1), recovered)
		stored, getErr := runs.GetPublication(ctx, ownerID, claimed.ID)
		require.NoError(t, getErr)
		require.Equal(t, models.PublicationStatusFailed, stored.Status)
	})

	t.Run("publication recovery scans beyond a full page of live leases", func(t *testing.T) {
		startedAt := now.Add(-time.Hour)
		for index := range 101 {
			projectID := fmt.Sprintf("publication-page-project-%s-%03d", suffix, index)
			publicationID := fmt.Sprintf("publication-page-%s-%03d", suffix, index)
			_, err = pool.Exec(ctx, `insert into projects(id,owner_id,name,template,status,thumbnail_url,metadata,created_at,updated_at) values($1,$2,'page','blank','DRAFT','','{}',$3,$3)`, projectID, ownerID, startedAt)
			require.NoError(t, err)
			_, err = pool.Exec(ctx, `insert into project_publications(id,owner_id,project_id,idempotency_key,build_context,dockerfile,status,worker_id,created_at,updated_at,started_at,heartbeat_at)
				values($1,$2,$3,$1,'.','Dockerfile',$4,$5,$6,$6,$6,$6)`, publicationID, ownerID, projectID, models.PublicationStatusRunning, "publication-page-worker", startedAt)
			require.NoError(t, err)
			if index < 100 {
				acquired, acquireErr := leaseStore.Acquire(ctx, publicationLeaseKind, publicationID, "publication-page-worker", time.Minute)
				require.NoError(t, acquireErr)
				require.True(t, acquired)
			}
		}
		recovered, recoveryErr := runs.FailOrphanedPublications(ctx, now.Add(-time.Minute), now)
		require.NoError(t, recoveryErr)
		require.Equal(t, int64(1), recovered)
		stored, getErr := runs.GetPublication(ctx, ownerID, fmt.Sprintf("publication-page-%s-100", suffix))
		require.NoError(t, getErr)
		require.Equal(t, models.PublicationStatusFailed, stored.Status)
	})

	t.Run("publication claim rolls back when Redis lease creation fails", func(t *testing.T) {
		queued, _, createErr := runs.CreatePublication(ctx, makePublication(projectOne, "publication-lease-failure"))
		require.NoError(t, createErr)
		leaseStore.acquireErr = errors.New("redis unavailable")
		claimed, claimErr := runs.ClaimPublication(ctx, queued.ID, "publication-worker-failed", now.Add(5*time.Minute))
		leaseStore.acquireErr = nil
		require.ErrorContains(t, claimErr, "redis unavailable")
		require.Nil(t, claimed)
		stored, getErr := runs.GetPublication(ctx, ownerID, queued.ID)
		require.NoError(t, getErr)
		require.Equal(t, models.PublicationStatusQueued, stored.Status)
		require.Empty(t, stored.WorkerID)
		_, cancelErr := runs.RequestPublicationCancel(ctx, ownerID, queued.ID, now.Add(6*time.Minute))
		require.NoError(t, cancelErr)
	})
}
