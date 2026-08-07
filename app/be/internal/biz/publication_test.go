package biz

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
)

type publicationRepoStub struct {
	mu        sync.Mutex
	item      *models.Publication
	runtime   *models.ProjectRuntime
	finished  string
	imageRef  string
	digest    string
	cancelled bool
}

func (r *publicationRepoStub) CreatePublication(_ context.Context, input *models.CreatePublicationInput) (*models.Publication, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.item = &models.Publication{ID: input.ID, OwnerID: input.OwnerID, ProjectID: input.ProjectID, IdempotencyKey: input.IdempotencyKey, Context: input.Context, Dockerfile: input.Dockerfile, Status: models.PublicationStatusQueued, CreatedAt: input.Now, UpdatedAt: input.Now}
	return r.item, false, nil
}
func (r *publicationRepoStub) FindPublicationByIdempotency(context.Context, string, string, string, string, string) (*models.Publication, error) {
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
func (r *publicationRepoStub) ClaimNextPublication(context.Context, string, time.Time) (*models.Publication, error) {
	return nil, nil
}
func (r *publicationRepoStub) HeartbeatPublication(context.Context, string, string, time.Time) (bool, error) {
	return true, nil
}
func (r *publicationRepoStub) FinishPublication(_ context.Context, input *models.FinishPublicationInput) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finished, r.imageRef, r.digest = input.Status, input.ImageRef, input.Digest
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
	publish func(context.Context) (*models.GatewayPublication, error)
}

func (g *publicationGatewayStub) PublishImage(ctx context.Context, _, _, _, _, _ string) (*models.GatewayPublication, error) {
	if g.publish != nil {
		return g.publish(ctx)
	}
	return &models.GatewayPublication{ImageRef: "registry.example/apps/project_1:pub_1", Digest: "sha256:digest", Logs: "done"}, nil
}

func TestCreatePublicationValidatesRuntimeAndQueuesOnce(t *testing.T) {
	now := time.Now().UTC()
	projects := &projectRepoStub{project: &models.Project{ID: "project_1", OwnerID: "user_1"}}
	runs := &runRepoStub{runtime: &models.ProjectRuntime{ProjectID: "project_1", OwnerID: "user_1", Status: models.RuntimeStatusActive, LastActiveAt: now, ExpiresAt: now.Add(time.Hour)}}
	publications := &publicationRepoStub{}
	publisher := &publicationGatewayStub{}
	usecase := NewProjectUsecaseWithPublishing(projects, runs, &eventStoreStub{}, &gatewayStub{}, publications, publisher).(*projectUseCase)
	usecase.now = func() time.Time { return now }
	result, apiErr := usecase.CreatePublication(context.Background(), models.AuthPrincipal{UserID: "user_1", SessionID: "auth_1"}, "project_1", "key_1", &models.PublicationCreateReq{})
	if apiErr != nil || result.Status != models.PublicationStatusQueued || publications.item.Context != "." || publications.item.Dockerfile != "Dockerfile" {
		t.Fatalf("unexpected publication result=%+v error=%+v item=%+v", result, apiErr, publications.item)
	}
}

func TestPublicationWorkerPersistsDigestAndDoesNotRetry(t *testing.T) {
	now := time.Now().UTC()
	repo := &publicationRepoStub{runtime: &models.ProjectRuntime{Status: models.RuntimeStatusActive, LastActiveAt: now, ExpiresAt: now.Add(time.Hour)}}
	item := &models.Publication{ID: "pub_1", OwnerID: "user_1", ProjectID: "project_1", Status: models.PublicationStatusRunning, CreatedAt: now}
	calls := 0
	gateway := &publicationGatewayStub{publish: func(context.Context) (*models.GatewayPublication, error) {
		calls++
		return &models.GatewayPublication{ImageRef: "registry.example/apps/project_1:pub_1", Digest: "sha256:digest", Logs: "done"}, nil
	}}
	worker := NewPublicationWorker(repo, gateway)
	worker.now = func() time.Time { return now }
	worker.execute(context.Background(), item)
	if calls != 1 || repo.finished != models.PublicationStatusCompleted || repo.imageRef == "" || repo.digest != "sha256:digest" {
		t.Fatalf("unexpected worker result calls=%d status=%s image=%s digest=%s", calls, repo.finished, repo.imageRef, repo.digest)
	}
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
