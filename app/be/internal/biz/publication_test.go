package biz

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/stretchr/testify/require"
)

const validPublicationDockerfile = "FROM node:24-alpine\nEXPOSE 8080\nUSER 1000\nCMD [\"node\", \"server.js\"]\n"

type publicationRepoStub struct {
	mu          sync.Mutex
	item        *models.Publication
	runtime     *models.ProjectRuntime
	finished    string
	imageRef    string
	digest      string
	deployURL   string
	cancelled   bool
	heartbeat   func() (bool, error)
	claim       func(context.Context, string, string, time.Time) (*models.Publication, error)
	snapshot    []byte
	skillUsed   bool
	dispatchAt  *time.Time
	createCalls int
	projected   bool
}

func (r *publicationRepoStub) CreatePublication(_ context.Context, input *models.CreatePublicationInput) (*models.Publication, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.createCalls++
	r.item = &models.Publication{ID: input.ID, OwnerID: input.OwnerID, ProjectID: input.ProjectID, IdempotencyKey: input.IdempotencyKey, Context: input.Context, Dockerfile: input.Dockerfile, Status: models.PublicationStatusPreparing, PreparationRunID: input.PreparationRunID, CreatedAt: input.Now, UpdatedAt: input.Now}
	return r.item, false, nil
}
func (r *publicationRepoStub) FindPublicationByIdempotency(_ context.Context, _, _, key, buildContext, dockerfile string) (*models.Publication, error) {
	if r.item != nil && r.item.IdempotencyKey == key {
		if r.item.Context != buildContext || r.item.Dockerfile != dockerfile {
			return nil, ErrIdempotencyConflict
		}
		return r.item, nil
	}
	return nil, nil
}
func (r *publicationRepoStub) GetPublication(context.Context, string, string) (*models.Publication, error) {
	if r.item == nil {
		return nil, ErrPublicationNotFound
	}
	return r.item, nil
}
func (r *publicationRepoStub) ListPublications(context.Context, string, string, int) ([]*models.Publication, error) {
	if r.item == nil {
		return nil, nil
	}
	return []*models.Publication{r.item}, nil
}
func (r *publicationRepoStub) RequestPublicationCancel(context.Context, string, string, time.Time) (*models.Publication, error) {
	r.cancelled = true
	return r.item, nil
}
func (r *publicationRepoStub) FailPublicationDispatch(context.Context, string, time.Time, error) error {
	return nil
}
func (r *publicationRepoStub) FindPublicationByPreparationRun(context.Context, string) (*models.Publication, error) {
	return r.item, nil
}
func (r *publicationRepoStub) PreparationSkillUsed(context.Context, string, string) (bool, error) {
	return r.skillUsed, nil
}
func (r *publicationRepoStub) PreparationRunProjected(context.Context, string, int64) (bool, error) {
	return r.projected, nil
}
func (r *publicationRepoStub) CompletePublicationPreparation(_ context.Context, input *models.CompletePublicationPreparationInput) (bool, error) {
	r.snapshot = append([]byte(nil), input.Snapshot...)
	r.item.Status = models.PublicationStatusQueued
	return true, nil
}
func (r *publicationRepoStub) FailPublicationPreparation(_ context.Context, _, _ string, status, code, message string, now time.Time) (bool, error) {
	r.item.Status, r.item.ErrorCode, r.item.ErrorMessage, r.item.CompletedAt = status, code, message, &now
	return true, nil
}
func (r *publicationRepoStub) MarkPublicationDispatched(_ context.Context, _ string, now time.Time) (bool, error) {
	r.dispatchAt, r.item.BuildDispatchedAt = &now, &now
	return true, nil
}
func (r *publicationRepoStub) LoadPublicationSnapshot(context.Context, string) (*models.WorkspaceSnapshot, error) {
	data := r.snapshot
	if len(data) == 0 {
		data = []byte("snapshot")
	}
	return &models.WorkspaceSnapshot{Data: data, SizeBytes: int64(len(data))}, nil
}
func (r *publicationRepoStub) ClaimPublication(ctx context.Context, id, workerID string, now time.Time) (*models.Publication, error) {
	if r.claim != nil {
		return r.claim(ctx, id, workerID, now)
	}
	return nil, nil
}
func (r *publicationRepoStub) HeartbeatPublication(context.Context, string, string, time.Time) (bool, error) {
	if r.heartbeat != nil {
		return r.heartbeat()
	}
	return true, nil
}
func (r *publicationRepoStub) FinishPublication(_ context.Context, input *models.FinishPublicationInput) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finished, r.imageRef, r.digest, r.deployURL = input.Status, input.ImageRef, input.Digest, input.DeploymentURL
	return true, nil
}
func (r *publicationRepoStub) FailOrphanedPublications(context.Context, time.Time, time.Time) (int64, error) {
	return 0, nil
}
func (r *publicationRepoStub) IsPublicationCancelRequested(context.Context, string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cancelled, nil
}
func (r *publicationRepoStub) GetRuntime(context.Context, string, string) (*models.ProjectRuntime, error) {
	return r.runtime, nil
}

type publicationGatewayStub struct {
	publish  func(context.Context) (*models.GatewayPublication, error)
	snapshot []byte
}

func (g *publicationGatewayStub) PublishApplication(ctx context.Context, _, _, _, _ string, snapshot []byte) (*models.GatewayPublication, error) {
	g.snapshot = append([]byte(nil), snapshot...)
	if g.publish != nil {
		return g.publish(ctx)
	}
	return &models.GatewayPublication{ImageRef: "registry.example/apps/project_1:pub_1", Digest: "sha256:digest", DeploymentURL: "https://app.example.com", Logs: "done"}, nil
}

func TestCreatePublicationValidatesRuntimeAndQueuesOnce(t *testing.T) {
	now := time.Now().UTC()
	projects := &projectRepoStub{project: &models.Project{ID: "project_1", OwnerID: "user_1"}}
	runs := &runRepoStub{runtime: &models.ProjectRuntime{ProjectID: "project_1", OwnerID: "user_1", Status: models.RuntimeStatusActive, LastActiveAt: now, ExpiresAt: now.Add(time.Hour)}}
	publications := &publicationRepoStub{}
	publisher := &publicationGatewayStub{}
	tasks := &taskPublisherStub{}
	usecase := NewProjectUsecaseWithPublishingAndTasks(projects, runs, &eventStoreStub{}, &gatewayStub{}, publications, publisher, tasks).(*projectUseCase)
	usecase.now = func() time.Time { return now }
	result, apiErr := usecase.CreatePublication(context.Background(), models.AuthPrincipal{UserID: "user_1", SessionID: "auth_1"}, "project_1", "key_1", &models.PublicationCreateReq{})
	if apiErr != nil || result.Status != models.PublicationStatusPreparing || publications.item.Context != "." || publications.item.Dockerfile != "Dockerfile" || result.PreparationRunID == "" {
		t.Fatalf("unexpected publication result=%+v error=%+v item=%+v", result, apiErr, publications.item)
	}
	require.Equal(t, []string{result.PreparationRunID}, tasks.runIDs)
	repeated, apiErr := usecase.CreatePublication(context.Background(), models.AuthPrincipal{UserID: "user_1", SessionID: "auth_1"}, "project_1", "key_1", &models.PublicationCreateReq{})
	require.Nil(t, apiErr)
	require.Equal(t, result.PreparationRunID, repeated.PreparationRunID)
	require.Equal(t, 1, publications.createCalls)
}

func TestPublicationPreparationFreezesSnapshotAndDispatchesOnce(t *testing.T) {
	now := time.Now().UTC()
	repo := &publicationRepoStub{
		item:      &models.Publication{ID: "pub-1", OwnerID: "user-1", ProjectID: "project-1", Status: models.PublicationStatusPreparing, PreparationRunID: "run-1", Context: ".", Dockerfile: "Dockerfile"},
		runtime:   &models.ProjectRuntime{GatewaySessionID: "session-1", Status: models.RuntimeStatusActive, LastActiveAt: now, ExpiresAt: now.Add(time.Hour)},
		skillUsed: true,
		projected: true,
	}
	gateway := &replayGatewayStub{gatewayStub: &gatewayStub{file: &models.GatewayFile{Path: "Dockerfile", Size: 128, Content: validPublicationDockerfile}}, snapshot: []byte("prepared-workspace")}
	tasks := &taskPublisherStub{}
	coordinator := NewPublicationPreparationCoordinator(repo, gateway, tasks)
	coordinator.now = func() time.Time { return now }
	event := &models.AgentEvent{Type: "run.completed", RunID: "run-1", Timestamp: now}

	require.NoError(t, coordinator.Handle(context.Background(), event))
	require.Equal(t, models.PublicationStatusQueued, repo.item.Status)
	require.Equal(t, []byte("prepared-workspace"), repo.snapshot)
	require.Equal(t, []string{"pub-1"}, tasks.publicationIDs)
	require.NotNil(t, repo.item.BuildDispatchedAt)

	require.NoError(t, coordinator.Handle(context.Background(), event))
	require.Equal(t, []string{"pub-1"}, tasks.publicationIDs)
}

func TestPublicationPreparationFailsWithoutDockerfile(t *testing.T) {
	now := time.Now().UTC()
	repo := &publicationRepoStub{
		item:      &models.Publication{ID: "pub-1", OwnerID: "user-1", ProjectID: "project-1", Status: models.PublicationStatusPreparing, PreparationRunID: "run-1", Context: ".", Dockerfile: "Dockerfile"},
		runtime:   &models.ProjectRuntime{GatewaySessionID: "session-1", Status: models.RuntimeStatusActive, LastActiveAt: now, ExpiresAt: now.Add(time.Hour)},
		skillUsed: true,
		projected: true,
	}
	gateway := &replayGatewayStub{gatewayStub: &gatewayStub{}, snapshot: []byte("prepared-workspace")}
	coordinator := NewPublicationPreparationCoordinator(repo, gateway, &taskPublisherStub{})
	coordinator.now = func() time.Time { return now }

	require.NoError(t, coordinator.Handle(context.Background(), &models.AgentEvent{Type: "run.completed", RunID: "run-1", Timestamp: now}))
	require.Equal(t, models.PublicationStatusFailed, repo.item.Status)
	require.Equal(t, "DOCKERFILE_PREPARATION_FAILED", repo.item.ErrorCode)
}

func TestValidateRuntimeDockerfileChecksFinalStageContract(t *testing.T) {
	require.NoError(t, validateRuntimeDockerfile("FROM node:24 AS build\nUSER 0\nFROM node:24-alpine\nEXPOSE 8080/tcp\nUSER 10001:10001\n"))
	require.ErrorContains(t, validateRuntimeDockerfile("FROM node:24-alpine\nEXPOSE 3000\nUSER 1000\n"), "8080")
	require.ErrorContains(t, validateRuntimeDockerfile("FROM node:24-alpine\nEXPOSE 8080\nUSER node\n"), "numeric")
	require.ErrorContains(t, validateRuntimeDockerfile("FROM node:24-alpine\nEXPOSE 8080\nUSER 0\n"), "non-root")
}

func TestPublicationPreparationFailsWithoutDockerignore(t *testing.T) {
	now := time.Now().UTC()
	repo := &publicationRepoStub{
		item:      &models.Publication{ID: "pub-1", OwnerID: "user-1", ProjectID: "project-1", Status: models.PublicationStatusPreparing, PreparationRunID: "run-1", Context: ".", Dockerfile: "Dockerfile"},
		runtime:   &models.ProjectRuntime{GatewaySessionID: "session-1", Status: models.RuntimeStatusActive, LastActiveAt: now, ExpiresAt: now.Add(time.Hour)},
		skillUsed: true,
		projected: true,
	}
	gateway := &replayGatewayStub{gatewayStub: &gatewayStub{getFile: func(path string) (*models.GatewayFile, error) {
		if path == "Dockerfile" {
			return &models.GatewayFile{Path: path, Size: 128, Content: validPublicationDockerfile}, nil
		}
		return nil, nil
	}}, snapshot: []byte("prepared-workspace")}
	coordinator := NewPublicationPreparationCoordinator(repo, gateway, &taskPublisherStub{})
	coordinator.now = func() time.Time { return now }

	require.NoError(t, coordinator.Handle(context.Background(), &models.AgentEvent{Type: "run.completed", RunID: "run-1", Timestamp: now}))
	require.Equal(t, models.PublicationStatusFailed, repo.item.Status)
	require.Contains(t, repo.item.ErrorMessage, ".dockerignore")
}

func TestPublicationPreparationWaitsForEventProjection(t *testing.T) {
	now := time.Now().UTC()
	repo := &publicationRepoStub{
		item:      &models.Publication{ID: "pub-1", OwnerID: "user-1", ProjectID: "project-1", Status: models.PublicationStatusPreparing, PreparationRunID: "run-1", Context: ".", Dockerfile: "Dockerfile"},
		runtime:   &models.ProjectRuntime{GatewaySessionID: "session-1", Status: models.RuntimeStatusActive, LastActiveAt: now, ExpiresAt: now.Add(time.Hour)},
		skillUsed: true,
	}
	gateway := &replayGatewayStub{gatewayStub: &gatewayStub{file: &models.GatewayFile{Path: "Dockerfile", Size: 128, Content: validPublicationDockerfile}}, snapshot: []byte("prepared-workspace")}
	tasks := &taskPublisherStub{}
	coordinator := NewPublicationPreparationCoordinator(repo, gateway, tasks)

	err := coordinator.Handle(context.Background(), &models.AgentEvent{Type: "run.completed", RunID: "run-1", Sequence: 10, Timestamp: now})
	require.ErrorContains(t, err, "not projected yet")
	require.Equal(t, models.PublicationStatusPreparing, repo.item.Status)
	require.Empty(t, tasks.publicationIDs)
}

func TestPublicationPreparationRetriesTransientGatewayFailure(t *testing.T) {
	now := time.Now().UTC()
	repo := &publicationRepoStub{
		item:      &models.Publication{ID: "pub-1", OwnerID: "user-1", ProjectID: "project-1", Status: models.PublicationStatusPreparing, PreparationRunID: "run-1", Context: ".", Dockerfile: "Dockerfile"},
		runtime:   &models.ProjectRuntime{GatewaySessionID: "session-1", Status: models.RuntimeStatusActive, LastActiveAt: now, ExpiresAt: now.Add(time.Hour)},
		skillUsed: true,
		projected: true,
	}
	gateway := &replayGatewayStub{gatewayStub: &gatewayStub{getErr: &models.GatewayResponseError{StatusCode: 503, Message: "unavailable"}}}
	coordinator := NewPublicationPreparationCoordinator(repo, gateway, &taskPublisherStub{})

	err := coordinator.Handle(context.Background(), &models.AgentEvent{Type: "run.completed", RunID: "run-1", Sequence: 10, Timestamp: now})
	require.ErrorContains(t, err, "unavailable")
	require.Equal(t, models.PublicationStatusPreparing, repo.item.Status)
}

func TestPublicationPreparationCancellationDoesNotDispatchBuild(t *testing.T) {
	now := time.Now().UTC()
	repo := &publicationRepoStub{item: &models.Publication{
		ID: "pub-1", ProjectID: "project-1", Status: models.PublicationStatusPreparing, PreparationRunID: "run-1",
	}, projected: true}
	tasks := &taskPublisherStub{}
	coordinator := NewPublicationPreparationCoordinator(repo, &gatewayStub{}, tasks)
	coordinator.now = func() time.Time { return now }

	require.NoError(t, coordinator.Handle(context.Background(), &models.AgentEvent{Type: "run.cancelled", RunID: "run-1", Timestamp: now}))
	require.Equal(t, models.PublicationStatusCancelled, repo.item.Status)
	require.Empty(t, tasks.publicationIDs)
}

func TestPublicationWorkerPersistsDigestAndDoesNotRetry(t *testing.T) {
	now := time.Now().UTC()
	repo := &publicationRepoStub{runtime: &models.ProjectRuntime{Status: models.RuntimeStatusActive, LastActiveAt: now, ExpiresAt: now.Add(time.Hour)}}
	item := &models.Publication{ID: "pub_1", OwnerID: "user_1", ProjectID: "project_1", Status: models.PublicationStatusRunning, CreatedAt: now}
	calls := 0
	gateway := &publicationGatewayStub{publish: func(context.Context) (*models.GatewayPublication, error) {
		calls++
		return &models.GatewayPublication{ImageRef: "registry.example/apps/project_1:pub_1", Digest: "sha256:digest", DeploymentURL: "https://app.example.com", Logs: "done"}, nil
	}}
	worker := NewPublicationWorker(repo, gateway)
	worker.now = func() time.Time { return now }
	worker.execute(context.Background(), item)
	if calls != 1 || repo.finished != models.PublicationStatusCompleted || repo.imageRef == "" || repo.digest != "sha256:digest" || repo.deployURL != "https://app.example.com" {
		t.Fatalf("unexpected worker result calls=%d status=%s image=%s digest=%s", calls, repo.finished, repo.imageRef, repo.digest)
	}
	require.Equal(t, []byte("snapshot"), gateway.snapshot)
}

func TestPublicationWorkerCancelsBuild(t *testing.T) {
	now := time.Now().UTC()
	repo := &publicationRepoStub{runtime: &models.ProjectRuntime{Status: models.RuntimeStatusActive, LastActiveAt: now, ExpiresAt: now.Add(time.Hour)}}
	item := &models.Publication{ID: "pub_1", OwnerID: "user_1", ProjectID: "project_1", Status: models.PublicationStatusRunning, CreatedAt: now}
	gateway := &publicationGatewayStub{publish: func(ctx context.Context) (*models.GatewayPublication, error) {
		repo.mu.Lock()
		repo.cancelled = true
		repo.mu.Unlock()
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	worker := NewPublicationWorker(repo, gateway)
	worker.cancelPoll = time.Millisecond
	worker.heartbeat = time.Hour
	worker.execute(context.Background(), item)
	if repo.finished != models.PublicationStatusCancelled {
		t.Fatalf("expected cancelled publication, got %s", repo.finished)
	}
}

func TestPublicationWorkerPreservesImageAfterDeploymentFailure(t *testing.T) {
	now := time.Now().UTC()
	repo := &publicationRepoStub{}
	item := &models.Publication{ID: "pub_1", OwnerID: "user_1", ProjectID: "project_1", Status: models.PublicationStatusRunning, CreatedAt: now}
	gateway := &publicationGatewayStub{publish: func(context.Context) (*models.GatewayPublication, error) {
		return nil, &models.GatewayResponseError{
			StatusCode: 422, Code: "APPLICATION_DEPLOY_FAILED", Message: "rollout timed out",
			ImageRef: "registry.example/apps/project_1:pub_1", Digest: "sha256:digest", Logs: "build complete",
		}
	}}
	worker := NewPublicationWorker(repo, gateway)
	worker.now = func() time.Time { return now }

	worker.execute(context.Background(), item)

	require.Equal(t, models.PublicationStatusFailed, repo.finished)
	require.Equal(t, "registry.example/apps/project_1:pub_1", repo.imageRef)
	require.Equal(t, "sha256:digest", repo.digest)
}

func TestPublicationWorkerContinuesDuringTemporaryRedisLeaseFailure(t *testing.T) {
	now := time.Now().UTC()
	repo := &publicationRepoStub{
		runtime:   &models.ProjectRuntime{Status: models.RuntimeStatusActive, LastActiveAt: now, ExpiresAt: now.Add(time.Hour)},
		heartbeat: func() (bool, error) { return false, errors.New("redis unavailable") },
	}
	item := &models.Publication{ID: "pub_1", OwnerID: "user_1", ProjectID: "project_1", Status: models.PublicationStatusRunning, CreatedAt: now}
	gateway := &publicationGatewayStub{publish: func(ctx context.Context) (*models.GatewayPublication, error) {
		select {
		case <-time.After(15 * time.Millisecond):
			return &models.GatewayPublication{ImageRef: "registry.example/app:pub", Digest: "sha256:digest"}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	worker := NewPublicationWorker(repo, gateway)
	worker.heartbeat = time.Millisecond
	worker.cancelPoll = time.Hour

	worker.execute(context.Background(), item)

	if repo.finished != models.PublicationStatusCompleted {
		t.Fatalf("expected completed publication, got %s", repo.finished)
	}
}

func TestPublicationWorkerRetriesClaimAndAckWithoutLosingExecution(t *testing.T) {
	now := time.Now().UTC()
	item := &models.Publication{ID: "publication-queue", OwnerID: "user-1", ProjectID: "project-1", Context: ".", Dockerfile: "Dockerfile", Status: models.PublicationStatusQueued, CreatedAt: now}
	repo := &publicationRepoStub{item: item, runtime: &models.ProjectRuntime{ProjectID: item.ProjectID, OwnerID: item.OwnerID, GatewaySessionID: "session-1", Status: models.RuntimeStatusActive, CreatedAt: now, LastActiveAt: now, ExpiresAt: now.Add(time.Hour)}}
	var claimCalls int
	repo.claim = func(_ context.Context, id, workerID string, _ time.Time) (*models.Publication, error) {
		claimCalls++
		if claimCalls == 1 {
			return nil, errors.New("redis unavailable")
		}
		item.Status, item.WorkerID = models.PublicationStatusRunning, workerID
		return item, nil
	}
	delivery := &taskDeliveryStub{id: item.ID, failAck: 1}
	queue := &taskQueueStub{delivery: delivery}
	gateway := &publicationGatewayStub{publish: func(context.Context) (*models.GatewayPublication, error) {
		return &models.GatewayPublication{ImageRef: "registry/app:latest", Digest: "sha256:digest"}, nil
	}}
	worker := NewPublicationWorker(repo, gateway, queue)
	worker.workerID = "publisher-1"
	worker.orphanAge = time.Hour
	worker.heartbeat = time.Hour
	worker.cancelPoll = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { worker.Run(ctx); close(done) }()
	require.Eventually(t, func() bool {
		repo.mu.Lock()
		defer repo.mu.Unlock()
		return repo.finished == models.PublicationStatusCompleted
	}, 3*time.Second, 10*time.Millisecond)
	cancel()
	<-done
	require.Equal(t, 2, claimCalls)
	require.Equal(t, int64(2), delivery.ackCalls.Load())
}
