package biz

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/autherr"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/response"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/token"
)

const (
	defaultProjectPageSize = 20
	maxProjectPageSize     = 100
)

var planProjectLimits = map[string]int{
	"free":       12,
	"pro":        100,
	"enterprise": 1000,
}

type ProjectRepo interface {
	CreateProject(ctx context.Context, input *models.CreateProjectInput) (*models.Project, error)
	ListProjects(ctx context.Context, filter *models.ProjectListFilter) ([]*models.Project, int, error)
	GetProjectByID(ctx context.Context, ownerID, projectID string) (*models.Project, error)
	GetProjectAndTouch(ctx context.Context, ownerID, projectID string, now time.Time) (*models.Project, error)
	UpdateProject(ctx context.Context, input *models.UpdateProjectInput) (*models.Project, error)
	SoftDeleteProject(ctx context.Context, ownerID, projectID string, now time.Time) error
	CountActiveProjectsByOwner(ctx context.Context, ownerID string) (int, error)
	GetUserPlan(ctx context.Context, userID string) (string, error)
}

type projectUseCase struct {
	repo ProjectRepo
	now  func() time.Time
}

func NewProjectUsecase(repo ProjectRepo) ProjectUseCase {
	return &projectUseCase{repo: repo, now: time.Now}
}

func (u *projectUseCase) List(ctx context.Context, principal models.AuthPrincipal, req *models.ProjectListReq) (*models.ProjectListResp, *response.APIError) {
	if strings.TrimSpace(principal.UserID) == "" {
		return nil, response.UnauthorizedError()
	}

	view := strings.ToLower(strings.TrimSpace(req.View))
	if view == "" {
		view = "all"
	}
	if view != "all" && view != "recent" && view != "shared" {
		return nil, response.InvalidArgumentError("view", "unsupported")
	}
	if view == "shared" {
		page, pageSize := normalizePagination(req.Page, req.PageSize)
		return &models.ProjectListResp{
			Items: []models.ProjectItem{},
			Pagination: models.Pagination{
				Page:     page,
				PageSize: pageSize,
				Total:    0,
			},
		}, nil
	}

	sortBy := strings.ToLower(strings.TrimSpace(req.SortBy))
	if sortBy == "" {
		sortBy = "updated_at"
	}
	if sortBy != "updated_at" && sortBy != "created_at" && sortBy != "name" {
		return nil, response.InvalidArgumentError("sort_by", "unsupported")
	}

	sortOrder := strings.ToLower(strings.TrimSpace(req.SortOrder))
	if sortOrder == "" {
		sortOrder = "desc"
	}
	if sortOrder != "asc" && sortOrder != "desc" {
		return nil, response.InvalidArgumentError("sort_order", "unsupported")
	}

	page, pageSize := normalizePagination(req.Page, req.PageSize)
	projects, total, err := u.repo.ListProjects(ctx, &models.ProjectListFilter{
		OwnerID:   principal.UserID,
		Keyword:   strings.TrimSpace(req.Keyword),
		Status:    strings.ToUpper(strings.TrimSpace(req.Status)),
		SortBy:    sortBy,
		SortOrder: sortOrder,
		Page:      page,
		PageSize:  pageSize,
		View:      view,
	})
	if err != nil {
		return nil, u.apiError(err)
	}

	items := make([]models.ProjectItem, 0, len(projects))
	for _, item := range projects {
		items = append(items, models.ProjectItem{
			ID:           item.ID,
			Name:         item.Name,
			Status:       item.Status,
			ThumbnailURL: item.ThumbnailURL,
			CreatedAt:    formatProjectTime(item.CreatedAt),
			UpdatedAt:    formatProjectTime(item.UpdatedAt),
			IsShared:     false,
		})
	}

	return &models.ProjectListResp{
		Items: items,
		Pagination: models.Pagination{
			Page:     page,
			PageSize: pageSize,
			Total:    total,
		},
	}, nil
}

func (u *projectUseCase) Create(ctx context.Context, principal models.AuthPrincipal, req *models.ProjectCreateReq) (*models.ProjectCreateResp, *response.APIError) {
	if strings.TrimSpace(principal.UserID) == "" {
		return nil, response.UnauthorizedError()
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, response.InvalidArgumentError("name", "required")
	}
	template := strings.TrimSpace(req.Template)
	if template == "" {
		return nil, response.InvalidArgumentError("template", "required")
	}

	now := u.now().UTC()
	project, err := u.repo.CreateProject(ctx, &models.CreateProjectInput{
		ID:       token.NewID("p"),
		OwnerID:  principal.UserID,
		Name:     name,
		Template: template,
		Status:   "DRAFT",
		Now:      now,
	})
	if err != nil {
		return nil, u.apiError(err)
	}

	return &models.ProjectCreateResp{
		ID:        project.ID,
		Name:      project.Name,
		Status:    project.Status,
		CreatedAt: formatProjectTime(project.CreatedAt),
	}, nil
}

func (u *projectUseCase) Detail(ctx context.Context, principal models.AuthPrincipal, projectID string) (*models.ProjectDetailResp, *response.APIError) {
	if strings.TrimSpace(principal.UserID) == "" {
		return nil, response.UnauthorizedError()
	}
	project, err := u.repo.GetProjectAndTouch(ctx, principal.UserID, strings.TrimSpace(projectID), u.now().UTC())
	if err != nil {
		return nil, u.apiError(err)
	}
	return &models.ProjectDetailResp{
		ID:           project.ID,
		Name:         project.Name,
		Status:       project.Status,
		OwnerID:      project.OwnerID,
		LastOpenedAt: formatProjectTimePointer(project.LastOpenedAt),
		Metadata:     metadataPointer(project.Metadata),
	}, nil
}

func (u *projectUseCase) Update(ctx context.Context, principal models.AuthPrincipal, projectID string, req *models.ProjectUpdateReq) (*models.ProjectUpdateResp, *response.APIError) {
	if strings.TrimSpace(principal.UserID) == "" {
		return nil, response.UnauthorizedError()
	}

	hasName := strings.TrimSpace(req.Name) != ""
	hasMetadata := req.Metadata != nil && strings.TrimSpace(req.Metadata.LastViewMode) != ""
	if !hasName && !hasMetadata {
		return nil, response.InvalidArgumentError("request", "name or metadata.last_view_mode required")
	}
	if req.Metadata != nil {
		lastViewMode := strings.TrimSpace(req.Metadata.LastViewMode)
		if lastViewMode != "" && lastViewMode != "preview" && lastViewMode != "code" {
			return nil, response.InvalidArgumentError("metadata.last_view_mode", "unsupported")
		}
	}

	existing, err := u.repo.GetProjectByID(ctx, principal.UserID, strings.TrimSpace(projectID))
	if err != nil {
		return nil, u.apiError(err)
	}

	name := existing.Name
	if strings.TrimSpace(req.Name) != "" {
		name = strings.TrimSpace(req.Name)
	}

	metadata := existing.Metadata
	if req.Metadata != nil && strings.TrimSpace(req.Metadata.LastViewMode) != "" {
		metadata.LastViewMode = strings.TrimSpace(req.Metadata.LastViewMode)
	}

	project, err := u.repo.UpdateProject(ctx, &models.UpdateProjectInput{
		ProjectID: strings.TrimSpace(projectID),
		OwnerID:   principal.UserID,
		Name:      name,
		Metadata:  metadata,
		Now:       u.now().UTC(),
	})
	if err != nil {
		return nil, u.apiError(err)
	}

	return &models.ProjectUpdateResp{
		ID:        project.ID,
		Name:      project.Name,
		UpdatedAt: formatProjectTime(project.UpdatedAt),
		Metadata:  metadataPointer(project.Metadata),
	}, nil
}

func (u *projectUseCase) Delete(ctx context.Context, principal models.AuthPrincipal, projectID string) (*models.ProjectDeleteResp, *response.APIError) {
	if strings.TrimSpace(principal.UserID) == "" {
		return nil, response.UnauthorizedError()
	}
	if err := u.repo.SoftDeleteProject(ctx, principal.UserID, strings.TrimSpace(projectID), u.now().UTC()); err != nil {
		return nil, u.apiError(err)
	}
	return &models.ProjectDeleteResp{Success: true}, nil
}

func (u *projectUseCase) Usage(ctx context.Context, principal models.AuthPrincipal) (*models.ProjectUsageResp, *response.APIError) {
	if strings.TrimSpace(principal.UserID) == "" {
		return nil, response.UnauthorizedError()
	}
	used, err := u.repo.CountActiveProjectsByOwner(ctx, principal.UserID)
	if err != nil {
		return nil, u.apiError(err)
	}
	plan, err := u.repo.GetUserPlan(ctx, principal.UserID)
	if err != nil {
		return nil, u.apiError(err)
	}
	limit, ok := planProjectLimits[strings.ToLower(strings.TrimSpace(plan))]
	if !ok {
		limit = planProjectLimits["free"]
	}
	return &models.ProjectUsageResp{Used: used, Limit: limit}, nil
}

func normalizePagination(page, pageSize int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = defaultProjectPageSize
	}
	if pageSize > maxProjectPageSize {
		pageSize = maxProjectPageSize
	}
	return page, pageSize
}

func formatProjectTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func formatProjectTimePointer(value *time.Time) string {
	if value == nil {
		return ""
	}
	return formatProjectTime(*value)
}

func metadataPointer(metadata models.ProjectMetadata) *models.ProjectMetadata {
	if strings.TrimSpace(metadata.LastViewMode) == "" {
		return nil
	}
	copy := metadata
	return &copy
}

func (u *projectUseCase) apiError(err error) *response.APIError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, autherr.ErrProjectNotFound):
		return response.NotFoundError()
	case errors.Is(err, autherr.ErrUserNotFound):
		return response.UnauthorizedError()
	default:
		return response.InternalError()
	}
}
