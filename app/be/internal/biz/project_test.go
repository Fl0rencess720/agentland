package biz

import (
	"context"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/autherr"
	"github.com/stretchr/testify/require"
)

type fakeProjectRepo struct {
	projects map[string]*models.Project
	plan     string
}

func (f *fakeProjectRepo) CreateProject(_ context.Context, input *models.CreateProjectInput) (*models.Project, error) {
	project := &models.Project{
		ID:        input.ID,
		OwnerID:   input.OwnerID,
		Name:      input.Name,
		Template:  input.Template,
		Status:    input.Status,
		CreatedAt: input.Now,
		UpdatedAt: input.Now,
	}
	if f.projects == nil {
		f.projects = map[string]*models.Project{}
	}
	f.projects[project.ID] = project
	return project, nil
}

func (f *fakeProjectRepo) ListProjects(_ context.Context, filter *models.ProjectListFilter) ([]*models.Project, int, error) {
	if filter.View == "shared" {
		return []*models.Project{}, 0, nil
	}
	items := make([]*models.Project, 0)
	for _, item := range f.projects {
		if item.OwnerID == filter.OwnerID && item.DeletedAt == nil {
			items = append(items, item)
		}
	}
	return items, len(items), nil
}

func (f *fakeProjectRepo) GetProjectByID(_ context.Context, ownerID, projectID string) (*models.Project, error) {
	project, ok := f.projects[projectID]
	if !ok || project.OwnerID != ownerID || project.DeletedAt != nil {
		return nil, autherr.ErrProjectNotFound
	}
	return project, nil
}

func (f *fakeProjectRepo) GetProjectAndTouch(_ context.Context, ownerID, projectID string, now time.Time) (*models.Project, error) {
	project, err := f.GetProjectByID(context.Background(), ownerID, projectID)
	if err != nil {
		return nil, err
	}
	project.LastOpenedAt = &now
	return project, nil
}

func (f *fakeProjectRepo) UpdateProject(_ context.Context, input *models.UpdateProjectInput) (*models.Project, error) {
	project, ok := f.projects[input.ProjectID]
	if !ok || project.OwnerID != input.OwnerID || project.DeletedAt != nil {
		return nil, autherr.ErrProjectNotFound
	}
	project.Name = input.Name
	project.Metadata = input.Metadata
	project.UpdatedAt = input.Now
	return project, nil
}

func (f *fakeProjectRepo) SoftDeleteProject(_ context.Context, ownerID, projectID string, now time.Time) error {
	project, ok := f.projects[projectID]
	if !ok || project.OwnerID != ownerID || project.DeletedAt != nil {
		return autherr.ErrProjectNotFound
	}
	project.DeletedAt = &now
	project.UpdatedAt = now
	return nil
}

func (f *fakeProjectRepo) CountActiveProjectsByOwner(_ context.Context, ownerID string) (int, error) {
	count := 0
	for _, item := range f.projects {
		if item.OwnerID == ownerID && item.DeletedAt == nil {
			count++
		}
	}
	return count, nil
}

func (f *fakeProjectRepo) GetUserPlan(_ context.Context, _ string) (string, error) {
	if f.plan == "" {
		return "free", nil
	}
	return f.plan, nil
}

func TestProjectUseCaseCreateAndUsage(t *testing.T) {
	repo := &fakeProjectRepo{plan: "pro"}
	useCase := NewProjectUsecase(repo).(*projectUseCase)
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	useCase.now = func() time.Time { return now }
	principal := models.AuthPrincipal{UserID: "u_123"}

	createResp, apiErr := useCase.Create(context.Background(), principal, &models.ProjectCreateReq{Name: " Demo ", Template: "blank"})
	require.Nil(t, apiErr)
	require.Equal(t, "Demo", createResp.Name)
	require.Equal(t, "DRAFT", createResp.Status)

	usageResp, apiErr := useCase.Usage(context.Background(), principal)
	require.Nil(t, apiErr)
	require.Equal(t, 1, usageResp.Used)
	require.Equal(t, 100, usageResp.Limit)
}

func TestProjectUseCaseUpdateValidation(t *testing.T) {
	repo := &fakeProjectRepo{projects: map[string]*models.Project{
		"p_1": {ID: "p_1", OwnerID: "u_123", Name: "One", Status: "DRAFT", CreatedAt: time.Now(), UpdatedAt: time.Now()},
	}}
	useCase := NewProjectUsecase(repo)
	principal := models.AuthPrincipal{UserID: "u_123"}

	_, apiErr := useCase.Update(context.Background(), principal, "p_1", &models.ProjectUpdateReq{Metadata: &models.ProjectMetadata{LastViewMode: "grid"}})
	require.NotNil(t, apiErr)
	require.Equal(t, 400, apiErr.StatusCode)

	resp, apiErr := useCase.Update(context.Background(), principal, "p_1", &models.ProjectUpdateReq{Metadata: &models.ProjectMetadata{LastViewMode: "code"}})
	require.Nil(t, apiErr)
	require.NotNil(t, resp.Metadata)
	require.Equal(t, "code", resp.Metadata.LastViewMode)
}

func TestProjectUseCaseDetailDeleteAndSharedView(t *testing.T) {
	now := time.Date(2026, 3, 12, 12, 0, 0, 0, time.UTC)
	repo := &fakeProjectRepo{projects: map[string]*models.Project{
		"p_1": {ID: "p_1", OwnerID: "u_123", Name: "One", Status: "DRAFT", CreatedAt: now, UpdatedAt: now},
	}}
	useCase := NewProjectUsecase(repo).(*projectUseCase)
	useCase.now = func() time.Time { return now.Add(5 * time.Minute) }
	principal := models.AuthPrincipal{UserID: "u_123"}

	listResp, apiErr := useCase.List(context.Background(), principal, &models.ProjectListReq{View: "shared"})
	require.Nil(t, apiErr)
	require.Len(t, listResp.Items, 0)

	detailResp, apiErr := useCase.Detail(context.Background(), principal, "p_1")
	require.Nil(t, apiErr)
	require.Equal(t, "u_123", detailResp.OwnerID)
	require.NotEmpty(t, detailResp.LastOpenedAt)

	deleteResp, apiErr := useCase.Delete(context.Background(), principal, "p_1")
	require.Nil(t, apiErr)
	require.True(t, deleteResp.Success)

	_, apiErr = useCase.Detail(context.Background(), principal, "p_1")
	require.NotNil(t, apiErr)
	require.Equal(t, 404, apiErr.StatusCode)
}
