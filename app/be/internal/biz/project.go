package biz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/autherr"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/response"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/token"
	"go.uber.org/zap"
)

const (
	defaultProjectPageSize        = 20
	maxProjectPageSize            = 100
	projectStatusDraft            = "DRAFT"
	projectStatusBuilding         = "BUILDING"
	generationJobType             = "APP_GENERATION"
	generationJobQueued           = "QUEUED"
	generationJobStarting         = "STARTING"
	generationJobRunning          = "RUNNING"
	generationJobSuccess          = "SUCCESS"
	generationJobFailed           = "FAILED"
	defaultGenerationWorkspace    = "/workspace"
	defaultGenerationIterations   = 10
	generationExecutionTimeout    = 15 * time.Minute
	maxPersistedGenerationJobLogs = 100
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
	UpdateProjectStatus(ctx context.Context, ownerID, projectID, status string, now time.Time) error
	SoftDeleteProject(ctx context.Context, ownerID, projectID string, now time.Time) error
	CountActiveProjectsByOwner(ctx context.Context, ownerID string) (int, error)
	GetUserPlan(ctx context.Context, userID string) (string, error)
	GetProjectChatSession(ctx context.Context, ownerID, projectID string) (*models.ProjectChatSession, error)
	UpsertProjectChatSession(ctx context.Context, input *models.UpsertProjectChatSessionInput) (*models.ProjectChatSession, error)
	ListProjectChatMessages(ctx context.Context, ownerID, projectID, cursor string, limit int) ([]*models.ProjectChatMessage, *string, error)
	CreateProjectChatMessage(ctx context.Context, input *models.CreateProjectChatMessageInput) (*models.ProjectChatMessage, error)
	UpdateProjectChatMessageContent(ctx context.Context, ownerID, projectID, messageID, content string) error
}

type AgentlandGateway interface {
	EnsureSessionReady(ctx context.Context) (*models.AgentSessionInfo, error)
	StreamChat(ctx context.Context, gatewaySessionID string, req *models.AgentChatStreamReq, onEvent func(*models.AgentSSEEvent) error) error
	CreatePreview(ctx context.Context, gatewaySessionID string, port int) (*models.GatewayPreviewInfo, error)
	GetFSTree(ctx context.Context, gatewaySessionID, targetPath string, depth int) (*models.GatewayFSTreeResp, error)
	GetFSFile(ctx context.Context, gatewaySessionID, targetPath, encoding string) (*models.GatewayFSFileResp, error)
	CreateExecContext(ctx context.Context, gatewaySessionID, language, cwd string) (*models.GatewayExecContextInfo, error)
	ExecuteInContext(ctx context.Context, gatewaySessionID, contextID, code string, timeoutMs int) (*models.GatewayExecutionResult, error)
	ProbePort(ctx context.Context, gatewaySessionID string, port int, requestPath string) (int, error)
}

type projectUseCase struct {
	repo         ProjectRepo
	jobRepo      JobRepo
	gateway      AgentlandGateway
	now          func() time.Time
	runAsync     func(func())
	sessionLocks sync.Map
}

type generationRunState struct {
	job                models.Job
	ownerID            string
	projectID          string
	projectName        string
	prompt             string
	userMessageID      string
	assistantMessageID string
	routeIntent        string
	assistantText      strings.Builder
	done               bool
}

func NewProjectUsecase(repo ProjectRepo, jobRepo JobRepo, gateway AgentlandGateway) ProjectUseCase {
	return &projectUseCase{
		repo:    repo,
		jobRepo: jobRepo,
		gateway: gateway,
		now:     time.Now,
		runAsync: func(fn func()) {
			go fn()
		},
	}
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
		Status:   projectStatusDraft,
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

func (u *projectUseCase) ListMessages(ctx context.Context, principal models.AuthPrincipal, projectID string, req *models.ChatMessagesReq) (*models.ChatMessagesResp, *response.APIError) {
	if strings.TrimSpace(principal.UserID) == "" {
		return nil, response.UnauthorizedError()
	}
	trimmedProjectID := strings.TrimSpace(projectID)
	if _, err := u.repo.GetProjectByID(ctx, principal.UserID, trimmedProjectID); err != nil {
		return nil, u.apiError(err)
	}
	messages, nextCursor, err := u.repo.ListProjectChatMessages(ctx, principal.UserID, trimmedProjectID, strings.TrimSpace(req.Cursor), 200)
	if err != nil {
		return nil, u.apiError(err)
	}
	items := make([]models.ChatMessageItem, 0, len(messages))
	for _, item := range messages {
		items = append(items, models.ChatMessageItem{
			ID:        item.ID,
			Role:      item.Role,
			Content:   item.Content,
			CreatedAt: formatProjectTime(item.CreatedAt),
		})
	}
	return &models.ChatMessagesResp{Items: items, NextCursor: nextCursor}, nil
}

func (u *projectUseCase) CreateMessage(ctx context.Context, principal models.AuthPrincipal, projectID string, req *models.ChatMessageCreateReq, onDelta func(string) error) (*models.ChatMessageStreamDoneResp, error) {
	if strings.TrimSpace(principal.UserID) == "" {
		return nil, response.UnauthorizedError()
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		return nil, response.InvalidArgumentError("content", "required")
	}
	trimmedProjectID := strings.TrimSpace(projectID)
	project, err := u.repo.GetProjectByID(ctx, principal.UserID, trimmedProjectID)
	if err != nil {
		return nil, u.apiError(err)
	}
	chatSession, err := u.ensureProjectChatSession(ctx, principal.UserID, project)
	if err != nil {
		return nil, gatewayAPIError(err)
	}
	_, err = u.repo.CreateProjectChatMessage(ctx, &models.CreateProjectChatMessageInput{
		ID:        token.NewID("m"),
		ProjectID: trimmedProjectID,
		OwnerID:   principal.UserID,
		Role:      "user",
		Content:   content,
		Now:       u.now().UTC(),
	})
	if err != nil {
		return nil, err
	}

	assistantText := strings.Builder{}
	if err = u.gateway.StreamChat(ctx, chatSession.GatewaySessionID, &models.AgentChatStreamReq{
		Message:       content,
		Deep:          req.Deep,
		SessionID:     chatSession.AgentChatSessionID,
		WorkspacePath: chatSession.WorkspacePath,
		ProjectName:   project.Name,
	}, func(event *models.AgentSSEEvent) error {
		switch event.Event {
		case "assistant_delta":
			var payload struct {
				Content string `json:"content"`
			}
			if json.Unmarshal(event.Data, &payload) == nil {
				delta := payload.Content
				if strings.TrimSpace(delta) != "" {
					assistantText.WriteString(delta)
					if onDelta != nil {
						return onDelta(delta)
					}
				}
			}
		case "session":
			var payload struct {
				SessionID     string `json:"session_id"`
				WorkspacePath string `json:"workspace_path"`
			}
			if json.Unmarshal(event.Data, &payload) == nil {
				updatedSession, upsertErr := u.repo.UpsertProjectChatSession(ctx, &models.UpsertProjectChatSessionInput{
					ProjectID:          trimmedProjectID,
					OwnerID:            principal.UserID,
					GatewaySessionID:   chatSession.GatewaySessionID,
					AgentChatSessionID: firstNonEmpty(strings.TrimSpace(payload.SessionID), chatSession.AgentChatSessionID),
					WorkspacePath:      firstNonEmpty(strings.TrimSpace(payload.WorkspacePath), chatSession.WorkspacePath),
					Now:                u.now().UTC(),
				})
				if upsertErr != nil {
					return upsertErr
				}
				chatSession = updatedSession
			}
		case "error":
			var payload struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(event.Data, &payload) == nil && strings.TrimSpace(payload.Message) != "" {
				return errors.New(payload.Message)
			}
			return errors.New("agent chat stream failed")
		}
		return nil
	}); err != nil {
		return nil, err
	}
	assistantMessage, err := u.repo.CreateProjectChatMessage(ctx, &models.CreateProjectChatMessageInput{
		ID:        token.NewID("m"),
		ProjectID: trimmedProjectID,
		OwnerID:   principal.UserID,
		Role:      "assistant",
		Content:   strings.TrimSpace(assistantText.String()),
		Now:       u.now().UTC(),
	})
	if err != nil {
		return nil, err
	}
	return &models.ChatMessageStreamDoneResp{MessageID: assistantMessage.ID, Changes: []models.FileChange{}}, nil
}

func (u *projectUseCase) ensureProjectChatSession(ctx context.Context, ownerID string, project *models.Project) (*models.ProjectChatSession, error) {
	chatSession, err := u.repo.GetProjectChatSession(ctx, ownerID, project.ID)
	if err != nil {
		return nil, err
	}
	if chatSession != nil && strings.TrimSpace(chatSession.GatewaySessionID) != "" {
		return chatSession, nil
	}

	mutex := u.projectSessionMutex(ownerID, project.ID)
	mutex.Lock()
	defer mutex.Unlock()

	chatSession, err = u.repo.GetProjectChatSession(ctx, ownerID, project.ID)
	if err != nil {
		return nil, err
	}
	if chatSession != nil && strings.TrimSpace(chatSession.GatewaySessionID) != "" {
		return chatSession, nil
	}

	job, err := u.latestProjectRuntime(ctx, ownerID, project.ID)
	if err != nil {
		return nil, err
	}
	seedGatewaySessionID := strings.TrimSpace(job.GatewaySessionID)
	seedAgentChatSessionID := firstNonEmpty(strings.TrimSpace(job.AgentSessionID), token.NewID("chat"))
	seedWorkspacePath := firstNonEmpty(strings.TrimSpace(job.WorkspacePath), defaultGenerationWorkspace)
	return u.repo.UpsertProjectChatSession(ctx, &models.UpsertProjectChatSessionInput{
		ProjectID:          project.ID,
		OwnerID:            ownerID,
		GatewaySessionID:   seedGatewaySessionID,
		AgentChatSessionID: seedAgentChatSessionID,
		WorkspacePath:      seedWorkspacePath,
		Now:                u.now().UTC(),
	})
}

func (u *projectUseCase) latestProjectRuntime(ctx context.Context, ownerID, projectID string) (*models.Job, error) {
	if u.jobRepo == nil {
		return nil, autherr.ErrProjectRuntimeUnavailable
	}
	job, err := u.jobRepo.GetLatestProjectRuntime(ctx, ownerID, projectID)
	if err != nil {
		if errors.Is(err, autherr.ErrJobNotFound) {
			return nil, autherr.ErrProjectRuntimeUnavailable
		}
		return nil, err
	}
	if job == nil || strings.TrimSpace(job.GatewaySessionID) == "" {
		return nil, autherr.ErrProjectRuntimeUnavailable
	}
	return job, nil
}

func firstNonEmpty(values ...string) string {
	for _, item := range values {
		if strings.TrimSpace(item) != "" {
			return item
		}
	}
	return ""
}

func (u *projectUseCase) CreateGeneration(ctx context.Context, principal models.AuthPrincipal, projectID string, req *models.GenerationCreateReq) (*models.GenerationCreateResp, *response.APIError) {
	if strings.TrimSpace(principal.UserID) == "" {
		return nil, response.UnauthorizedError()
	}
	prompt := strings.TrimSpace(req.Prompt)
	if prompt == "" {
		return nil, response.InvalidArgumentError("prompt", "required")
	}
	trimmedProjectID := strings.TrimSpace(projectID)
	project, err := u.repo.GetProjectByID(ctx, principal.UserID, trimmedProjectID)
	if err != nil {
		return nil, u.apiError(err)
	}
	if u.jobRepo == nil || u.gateway == nil {
		return nil, response.InternalError()
	}

	now := u.now().UTC()
	assistantPlaceholderAt := now.Add(time.Millisecond)
	job, err := u.jobRepo.CreateJob(ctx, &models.CreateJobInput{
		ID:        token.NewID("job_gen"),
		OwnerID:   principal.UserID,
		ProjectID: trimmedProjectID,
		Type:      generationJobType,
		Status:    generationJobQueued,
		Progress:  0,
		Logs: []string{
			"Generation queued",
		},
		RequestPayload: models.GenerationRequestPayload{
			Prompt:      prompt,
			Attachments: req.Attachments,
			Deep:        req.Deep,
		},
		Now: now,
	})
	if err != nil {
		zap.L().Error("create generation job failed", zap.Error(err), zap.String("project_id", trimmedProjectID), zap.String("user_id", principal.UserID))
		return nil, response.InternalError()
	}

	userMessageID := token.NewID("m")
	assistantMessageID := token.NewID("m")
	if _, err = u.repo.CreateProjectChatMessage(ctx, &models.CreateProjectChatMessageInput{
		ID:        userMessageID,
		ProjectID: trimmedProjectID,
		OwnerID:   principal.UserID,
		Role:      "user",
		Content:   prompt,
		Now:       now,
	}); err != nil {
		zap.L().Error("create generation user chat message failed", zap.Error(err), zap.String("project_id", trimmedProjectID), zap.String("user_id", principal.UserID))
		return nil, response.InternalError()
	}
	if _, err = u.repo.CreateProjectChatMessage(ctx, &models.CreateProjectChatMessageInput{
		ID:        assistantMessageID,
		ProjectID: trimmedProjectID,
		OwnerID:   principal.UserID,
		Role:      "assistant",
		Content:   "",
		Now:       assistantPlaceholderAt,
	}); err != nil {
		zap.L().Error("create generation assistant chat placeholder failed", zap.Error(err), zap.String("project_id", trimmedProjectID), zap.String("user_id", principal.UserID))
		return nil, response.InternalError()
	}

	state := &generationRunState{
		job: models.Job{
			ID:             job.ID,
			OwnerID:        principal.UserID,
			ProjectID:      trimmedProjectID,
			Type:           generationJobType,
			Status:         generationJobQueued,
			Progress:       0,
			Logs:           append([]string{}, job.Logs...),
			RequestPayload: job.RequestPayload,
			WorkspacePath:  defaultGenerationWorkspace,
			AgentSessionID: token.NewID("chat"),
			CreatedAt:      now,
			UpdatedAt:      now,
		},
		ownerID:            principal.UserID,
		projectID:          trimmedProjectID,
		projectName:        project.Name,
		prompt:             prompt,
		userMessageID:      userMessageID,
		assistantMessageID: assistantMessageID,
	}

	u.runAsync(func() {
		u.runGeneration(state, prompt, req.Attachments, req.Deep)
	})

	return &models.GenerationCreateResp{
		JobID:  job.ID,
		Status: generationJobQueued,
	}, nil
}

func (u *projectUseCase) runGeneration(state *generationRunState, prompt string, attachments []models.AttachmentRef, deep bool) {
	ctx, cancel := context.WithTimeout(context.Background(), generationExecutionTimeout)
	defer cancel()

	startedAt := u.now().UTC()
	state.job.Status = generationJobStarting
	state.job.Progress = 5
	state.job.StartedAt = &startedAt
	state.job.UpdatedAt = startedAt
	state.job.Logs = appendJobLog(state.job.Logs, "Preparing sandbox")
	u.persistJob(ctx, &state.job)
	u.persistProjectStatus(ctx, state.ownerID, state.projectID, projectStatusBuilding)

	sessionInfo, err := u.gateway.EnsureSessionReady(ctx)
	if err != nil {
		u.failGeneration(ctx, state, fmt.Errorf("prepare sandbox: %w", err))
		return
	}

	state.job.GatewaySessionID = sessionInfo.GatewaySessionID
	state.job.Status = generationJobRunning
	state.job.Progress = maxInt(state.job.Progress, 15)
	state.job.Logs = appendJobLog(state.job.Logs, "Sandbox ready")
	state.job.UpdatedAt = u.now().UTC()
	u.persistJob(ctx, &state.job)
	if _, upsertErr := u.repo.UpsertProjectChatSession(ctx, &models.UpsertProjectChatSessionInput{
		ProjectID:          state.projectID,
		OwnerID:            state.ownerID,
		GatewaySessionID:   strings.TrimSpace(state.job.GatewaySessionID),
		AgentChatSessionID: firstNonEmpty(strings.TrimSpace(state.job.AgentSessionID), token.NewID("chat")),
		WorkspacePath:      firstNonEmpty(strings.TrimSpace(state.job.WorkspacePath), defaultGenerationWorkspace),
		Now:                u.now().UTC(),
	}); upsertErr != nil {
		zap.L().Warn("seed project chat session from generation runtime failed", zap.Error(upsertErr), zap.String("project_id", state.projectID))
	}

	streamReq := &models.AgentChatStreamReq{
		Message:       buildGenerationMessage(prompt, attachments),
		Deep:          deep,
		SessionID:     state.job.AgentSessionID,
		WorkspacePath: state.job.WorkspacePath,
		ProjectName:   state.projectName,
		Iterations:    defaultGenerationIterations,
	}

	err = u.gateway.StreamChat(ctx, state.job.GatewaySessionID, streamReq, func(event *models.AgentSSEEvent) error {
		return u.handleGenerationEvent(ctx, state, event)
	})
	if err != nil {
		u.failGeneration(ctx, state, err)
		return
	}
	if !state.done {
		u.failGeneration(ctx, state, errors.New("agent stream ended before done event"))
		return
	}
}

func (u *projectUseCase) handleGenerationEvent(ctx context.Context, state *generationRunState, event *models.AgentSSEEvent) error {
	switch event.Event {
	case "ping":
		return nil
	case "assistant_delta":
		var payload struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal(event.Data, &payload); err == nil && payload.Content != "" {
			state.assistantText.WriteString(payload.Content)
			state.job.Progress = maxInt(state.job.Progress, 55)
			state.job.Result = buildGenerationResult(state, nil)
			state.job.UpdatedAt = u.now().UTC()
			if persistErr := u.persistGenerationAssistantMessage(ctx, state); persistErr != nil {
				return persistErr
			}
			u.persistJob(ctx, &state.job)
		}
		return nil
	case "route":
		var payload struct {
			Intent string `json:"intent"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal(event.Data, &payload); err == nil {
			state.routeIntent = strings.TrimSpace(payload.Intent)
			message := "Agent route resolved"
			if payload.Intent != "" {
				message = fmt.Sprintf("Routed to %s mode", payload.Intent)
			}
			state.job.Progress = maxInt(state.job.Progress, 20)
			state.job.Logs = appendJobLog(state.job.Logs, message)
			state.job.Result = buildGenerationResult(state, nil)
			state.job.UpdatedAt = u.now().UTC()
			u.persistJob(ctx, &state.job)
		}
		return nil
	case "session":
		var payload struct {
			SessionID     string `json:"session_id"`
			WorkspacePath string `json:"workspace_path"`
			Mode          string `json:"mode"`
		}
		if err := json.Unmarshal(event.Data, &payload); err == nil {
			if strings.TrimSpace(payload.SessionID) != "" {
				state.job.AgentSessionID = strings.TrimSpace(payload.SessionID)
			}
			if strings.TrimSpace(payload.WorkspacePath) != "" {
				state.job.WorkspacePath = strings.TrimSpace(payload.WorkspacePath)
			}
			if _, upsertErr := u.repo.UpsertProjectChatSession(ctx, &models.UpsertProjectChatSessionInput{
				ProjectID:          state.projectID,
				OwnerID:            state.ownerID,
				GatewaySessionID:   strings.TrimSpace(state.job.GatewaySessionID),
				AgentChatSessionID: firstNonEmpty(strings.TrimSpace(state.job.AgentSessionID), token.NewID("chat")),
				WorkspacePath:      firstNonEmpty(strings.TrimSpace(state.job.WorkspacePath), defaultGenerationWorkspace),
				Now:                u.now().UTC(),
			}); upsertErr != nil {
				zap.L().Warn("update project chat session from generation event failed", zap.Error(upsertErr), zap.String("project_id", state.projectID))
			}
			state.job.Progress = maxInt(state.job.Progress, 25)
			state.job.Logs = appendJobLog(state.job.Logs, "Agent session established")
			state.job.Result = buildGenerationResult(state, nil)
			state.job.UpdatedAt = u.now().UTC()
			u.persistJob(ctx, &state.job)
		}
		return nil
	case "planner_fallback":
		state.job.Progress = maxInt(state.job.Progress, 30)
		state.job.Logs = appendJobLog(state.job.Logs, "Planner fallback engaged")
		state.job.UpdatedAt = u.now().UTC()
		u.persistJob(ctx, &state.job)
		return nil
	case "plan_ready":
		state.job.Progress = maxInt(state.job.Progress, 40)
		state.job.Logs = appendJobLog(state.job.Logs, "Execution plan ready")
		state.job.UpdatedAt = u.now().UTC()
		u.persistJob(ctx, &state.job)
		return nil
	case "iteration_start":
		var payload struct {
			Iteration     int `json:"iteration"`
			MaxIterations int `json:"max_iterations"`
		}
		if err := json.Unmarshal(event.Data, &payload); err == nil {
			progress := 45
			if payload.MaxIterations > 0 && payload.Iteration > 0 {
				progress = 45 + ((payload.Iteration - 1) * 40 / payload.MaxIterations)
			}
			state.job.Progress = maxInt(state.job.Progress, progress)
			state.job.Logs = appendJobLog(state.job.Logs, fmt.Sprintf("Iteration %d started", maxInt(payload.Iteration, 1)))
			state.job.UpdatedAt = u.now().UTC()
			u.persistJob(ctx, &state.job)
		}
		return nil
	case "iteration_complete":
		var payload struct {
			Iteration int  `json:"iteration"`
			Complete  bool `json:"complete"`
		}
		if err := json.Unmarshal(event.Data, &payload); err == nil {
			progress := 80
			if payload.Complete {
				progress = 90
			}
			state.job.Progress = maxInt(state.job.Progress, progress)
			state.job.Logs = appendJobLog(state.job.Logs, fmt.Sprintf("Iteration %d complete", maxInt(payload.Iteration, 1)))
			state.job.UpdatedAt = u.now().UTC()
			u.persistJob(ctx, &state.job)
		}
		return nil
	case "tool_call":
		var payload struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(event.Data, &payload); err == nil && strings.TrimSpace(payload.Name) != "" {
			state.job.Logs = appendJobLog(state.job.Logs, fmt.Sprintf("Tool call: %s", payload.Name))
			state.job.UpdatedAt = u.now().UTC()
			u.persistJob(ctx, &state.job)
		}
		return nil
	case "error":
		var payload struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(event.Data, &payload); err == nil && strings.TrimSpace(payload.Message) != "" {
			return errors.New(payload.Message)
		}
		return errors.New("agent stream returned error event")
	case "done":
		var payload map[string]any
		_ = json.Unmarshal(event.Data, &payload)
		completedAt := u.now().UTC()
		if err := u.persistGenerationChatHistory(ctx, state, completedAt); err != nil {
			return err
		}
		state.job.Status = generationJobSuccess
		state.job.Progress = 100
		state.job.CompletedAt = &completedAt
		state.job.UpdatedAt = completedAt
		state.job.Result = buildGenerationResult(state, payload)
		state.job.Logs = appendJobLog(state.job.Logs, "Generation complete")
		state.done = true
		u.persistJob(ctx, &state.job)
		u.persistProjectStatus(ctx, state.ownerID, state.projectID, projectStatusDraft)
		return nil
	default:
		return nil
	}
}

func (u *projectUseCase) failGeneration(ctx context.Context, state *generationRunState, err error) {
	completedAt := u.now().UTC()
	state.job.Status = generationJobFailed
	state.job.Progress = maxInt(state.job.Progress, 100)
	state.job.CompletedAt = &completedAt
	state.job.UpdatedAt = completedAt
	state.job.ErrorMessage = strings.TrimSpace(err.Error())
	state.job.Result = buildGenerationFailureResult(state)
	state.job.Logs = appendJobLog(state.job.Logs, fmt.Sprintf("Generation failed: %s", state.job.ErrorMessage))
	z := zap.L().With(zap.String("job_id", state.job.ID), zap.String("project_id", state.projectID))
	z.Error("generation job failed", zap.Error(err))
	u.persistJob(ctx, &state.job)
	u.persistProjectStatus(ctx, state.ownerID, state.projectID, projectStatusDraft)
}

func (u *projectUseCase) persistJob(ctx context.Context, job *models.Job) {
	if u.jobRepo == nil {
		return
	}
	if err := u.jobRepo.UpdateJob(ctx, &models.UpdateJobInput{
		JobID:            job.ID,
		Status:           job.Status,
		Progress:         job.Progress,
		Logs:             append([]string{}, job.Logs...),
		Result:           job.Result,
		GatewaySessionID: job.GatewaySessionID,
		AgentSessionID:   job.AgentSessionID,
		WorkspacePath:    job.WorkspacePath,
		ErrorMessage:     job.ErrorMessage,
		StartedAt:        job.StartedAt,
		CompletedAt:      job.CompletedAt,
		UpdatedAt:        job.UpdatedAt,
	}); err != nil {
		zap.L().Error("persist generation job state failed", zap.Error(err), zap.String("job_id", job.ID))
	}
}

func (u *projectUseCase) persistProjectStatus(ctx context.Context, ownerID, projectID, status string) {
	if u.repo == nil {
		return
	}
	if err := u.repo.UpdateProjectStatus(ctx, ownerID, projectID, status, u.now().UTC()); err != nil {
		zap.L().Warn("persist project status failed", zap.Error(err), zap.String("project_id", projectID), zap.String("status", status))
	}
}

func (u *projectUseCase) persistGenerationAssistantMessage(ctx context.Context, state *generationRunState) error {
	if u.repo == nil || strings.TrimSpace(state.assistantMessageID) == "" {
		return nil
	}
	return u.repo.UpdateProjectChatMessageContent(ctx, state.ownerID, state.projectID, state.assistantMessageID, strings.TrimSpace(state.assistantText.String()))
}

func (u *projectUseCase) persistGenerationChatHistory(ctx context.Context, state *generationRunState, completedAt time.Time) error {
	if u.repo == nil {
		return nil
	}

	agentSessionID := strings.TrimSpace(state.job.AgentSessionID)
	if agentSessionID == "" {
		agentSessionID = token.NewID("chat")
	}
	workspacePath := strings.TrimSpace(state.job.WorkspacePath)
	if workspacePath == "" {
		workspacePath = defaultGenerationWorkspace
	}

	if _, err := u.repo.UpsertProjectChatSession(ctx, &models.UpsertProjectChatSessionInput{
		ProjectID:          state.projectID,
		OwnerID:            state.ownerID,
		GatewaySessionID:   strings.TrimSpace(state.job.GatewaySessionID),
		AgentChatSessionID: agentSessionID,
		WorkspacePath:      workspacePath,
		Now:                completedAt,
	}); err != nil {
		return err
	}

	if err := u.persistGenerationAssistantMessage(ctx, state); err != nil {
		return err
	}

	return nil
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

func buildGenerationResult(state *generationRunState, terminalPayload map[string]any) map[string]any {
	result := map[string]any{
		"project_id":         state.projectID,
		"gateway_session_id": state.job.GatewaySessionID,
		"agent_session_id":   state.job.AgentSessionID,
		"workspace_path":     state.job.WorkspacePath,
		"route_intent":       state.routeIntent,
		"assistant_text":     strings.TrimSpace(state.assistantText.String()),
	}
	if terminalPayload != nil {
		result["terminal_event_data"] = terminalPayload
	}
	return result
}

func buildGenerationFailureResult(state *generationRunState) map[string]any {
	result := buildGenerationResult(state, nil)
	result["error"] = state.job.ErrorMessage
	return result
}

func appendJobLog(logs []string, message string) []string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return logs
	}
	updated := append(append([]string{}, logs...), trimmed)
	if len(updated) <= maxPersistedGenerationJobLogs {
		return updated
	}
	return updated[len(updated)-maxPersistedGenerationJobLogs:]
}

func buildGenerationMessage(prompt string, attachments []models.AttachmentRef) string {
	prompt = strings.TrimSpace(prompt)
	if len(attachments) == 0 {
		return prompt
	}
	var builder strings.Builder
	builder.WriteString(prompt)
	builder.WriteString("\n\nReferenced attachments:\n")
	for _, attachment := range attachments {
		name := strings.TrimSpace(attachment.Name)
		if name == "" {
			name = attachment.FileID
		}
		builder.WriteString("- ")
		builder.WriteString(name)
		if strings.TrimSpace(attachment.FileID) != "" {
			builder.WriteString(" (")
			builder.WriteString(strings.TrimSpace(attachment.FileID))
			builder.WriteString(")")
		}
		builder.WriteString("\n")
	}
	builder.WriteString("If attachment contents are unavailable in the workspace, proceed using the prompt and state any assumptions in generated output.")
	return builder.String()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func (u *projectUseCase) apiError(err error) *response.APIError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, autherr.ErrProjectNotFound):
		return response.NotFoundError()
	case errors.Is(err, autherr.ErrProjectRuntimeUnavailable):
		return response.RuntimeUnavailableError()
	case errors.Is(err, autherr.ErrUserNotFound):
		return response.UnauthorizedError()
	default:
		return response.InternalError()
	}
}
