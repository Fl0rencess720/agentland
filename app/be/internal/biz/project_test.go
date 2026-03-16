package biz

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/autherr"
	"github.com/stretchr/testify/require"
)

type fakeProjectRepo struct {
	projects          map[string]*models.Project
	plan              string
	statusTransitions []string
	chatSessions      map[string]*models.ProjectChatSession
	chatMessages      map[string][]*models.ProjectChatMessage
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

func (f *fakeProjectRepo) UpdateProjectStatus(_ context.Context, ownerID, projectID, status string, now time.Time) error {
	project, ok := f.projects[projectID]
	if !ok || project.OwnerID != ownerID || project.DeletedAt != nil {
		return autherr.ErrProjectNotFound
	}
	project.Status = status
	project.UpdatedAt = now
	f.statusTransitions = append(f.statusTransitions, status)
	return nil
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

func (f *fakeProjectRepo) GetProjectChatSession(_ context.Context, ownerID, projectID string) (*models.ProjectChatSession, error) {
	if f.chatSessions == nil {
		return nil, nil
	}
	session, ok := f.chatSessions[projectID]
	if !ok || session.OwnerID != ownerID {
		return nil, nil
	}
	return session, nil
}

func (f *fakeProjectRepo) UpsertProjectChatSession(_ context.Context, input *models.UpsertProjectChatSessionInput) (*models.ProjectChatSession, error) {
	if f.chatSessions == nil {
		f.chatSessions = map[string]*models.ProjectChatSession{}
	}
	session := &models.ProjectChatSession{
		ProjectID:          input.ProjectID,
		OwnerID:            input.OwnerID,
		GatewaySessionID:   input.GatewaySessionID,
		AgentChatSessionID: input.AgentChatSessionID,
		WorkspacePath:      input.WorkspacePath,
		CreatedAt:          input.Now,
		UpdatedAt:          input.Now,
		LastMessageAt:      input.Now,
	}
	if existing, ok := f.chatSessions[input.ProjectID]; ok {
		session.CreatedAt = existing.CreatedAt
	}
	f.chatSessions[input.ProjectID] = session
	return session, nil
}

func (f *fakeProjectRepo) ListProjectChatMessages(_ context.Context, ownerID, projectID, _ string, _ int) ([]*models.ProjectChatMessage, *string, error) {
	messages := make([]*models.ProjectChatMessage, 0)
	for _, item := range f.chatMessages[projectID] {
		if item.OwnerID == ownerID {
			messages = append(messages, item)
		}
	}
	return messages, nil, nil
}

func (f *fakeProjectRepo) UpdateProjectChatMessageContent(_ context.Context, ownerID, projectID, messageID, content string) error {
	for _, item := range f.chatMessages[projectID] {
		if item.ID == messageID && item.OwnerID == ownerID {
			item.Content = content
			return nil
		}
	}
	return autherr.ErrProjectNotFound
}

func (f *fakeProjectRepo) CreateProjectChatMessage(_ context.Context, input *models.CreateProjectChatMessageInput) (*models.ProjectChatMessage, error) {
	if f.chatMessages == nil {
		f.chatMessages = map[string][]*models.ProjectChatMessage{}
	}
	message := &models.ProjectChatMessage{
		ID:        input.ID,
		ProjectID: input.ProjectID,
		OwnerID:   input.OwnerID,
		Role:      input.Role,
		Content:   input.Content,
		CreatedAt: input.Now,
	}
	f.chatMessages[input.ProjectID] = append(f.chatMessages[input.ProjectID], message)
	if session, ok := f.chatSessions[input.ProjectID]; ok {
		session.UpdatedAt = input.Now
		session.LastMessageAt = input.Now
	}
	return message, nil
}

type fakeJobRepo struct {
	jobs map[string]*models.Job
}

func (f *fakeJobRepo) CreateJob(_ context.Context, input *models.CreateJobInput) (*models.Job, error) {
	if f.jobs == nil {
		f.jobs = map[string]*models.Job{}
	}
	job := &models.Job{
		ID:               input.ID,
		OwnerID:          input.OwnerID,
		ProjectID:        input.ProjectID,
		Type:             input.Type,
		Status:           input.Status,
		Progress:         input.Progress,
		Logs:             append([]string{}, input.Logs...),
		Result:           input.Result,
		RequestPayload:   input.RequestPayload,
		GatewaySessionID: input.GatewaySessionID,
		AgentSessionID:   input.AgentSessionID,
		WorkspacePath:    input.WorkspacePath,
		ErrorMessage:     input.ErrorMessage,
		CreatedAt:        input.Now,
		UpdatedAt:        input.Now,
	}
	f.jobs[job.ID] = job
	return job, nil
}

func (f *fakeJobRepo) GetJobByID(_ context.Context, ownerID, jobID string) (*models.Job, error) {
	job, ok := f.jobs[jobID]
	if !ok || job.OwnerID != ownerID {
		return nil, autherr.ErrJobNotFound
	}
	return job, nil
}

func (f *fakeJobRepo) GetLatestProjectRuntime(_ context.Context, ownerID, projectID string) (*models.Job, error) {
	priority := map[string]int{
		"RUNNING":  1,
		"STARTING": 2,
		"SUCCESS":  3,
		"FAILED":   4,
	}
	var latest *models.Job
	for _, job := range f.jobs {
		if job.OwnerID != ownerID || job.ProjectID != projectID || job.Type != "APP_GENERATION" || strings.TrimSpace(job.GatewaySessionID) == "" {
			continue
		}
		if latest == nil {
			latest = job
			continue
		}
		left := priority[job.Status]
		if left == 0 {
			left = 99
		}
		right := priority[latest.Status]
		if right == 0 {
			right = 99
		}
		if left < right || (left == right && job.UpdatedAt.After(latest.UpdatedAt)) {
			latest = job
		}
	}
	if latest == nil {
		return nil, autherr.ErrJobNotFound
	}
	return latest, nil
}

func (f *fakeJobRepo) UpdateJob(_ context.Context, input *models.UpdateJobInput) error {
	job, ok := f.jobs[input.JobID]
	if !ok {
		return autherr.ErrJobNotFound
	}
	job.Status = input.Status
	job.Progress = input.Progress
	job.Logs = append([]string{}, input.Logs...)
	job.Result = input.Result
	job.GatewaySessionID = input.GatewaySessionID
	job.AgentSessionID = input.AgentSessionID
	job.WorkspacePath = input.WorkspacePath
	job.ErrorMessage = input.ErrorMessage
	job.StartedAt = input.StartedAt
	job.CompletedAt = input.CompletedAt
	job.UpdatedAt = input.UpdatedAt
	return nil
}

type fakeAgentlandGateway struct {
	sessionID        string
	ensureCalls      int
	ensureErr        error
	streamErr        error
	events           []*models.AgentSSEEvent
	lastStreamReq    *models.AgentChatStreamReq
	previewInfo      *models.GatewayPreviewInfo
	fsTree           *models.GatewayFSTreeResp
	fsFiles          map[string]*models.GatewayFSFileResp
	contextInfo      *models.GatewayExecContextInfo
	executionResult  *models.GatewayExecutionResult
	probeStatuses    []int
	createPreviewErr error
	fsErr            error
	contextErr       error
	executeErr       error
	probeErr         error
	lastExecuteCode  string
}

func (f *fakeAgentlandGateway) EnsureSessionReady(_ context.Context) (*models.AgentSessionInfo, error) {
	f.ensureCalls++
	if f.ensureErr != nil {
		return nil, f.ensureErr
	}
	if f.sessionID == "" {
		f.sessionID = "sess_gateway_1"
	}
	return &models.AgentSessionInfo{GatewaySessionID: f.sessionID}, nil
}

func (f *fakeAgentlandGateway) StreamChat(_ context.Context, _ string, req *models.AgentChatStreamReq, onEvent func(*models.AgentSSEEvent) error) error {
	if req != nil {
		copyReq := *req
		f.lastStreamReq = &copyReq
	}
	if f.streamErr != nil {
		return f.streamErr
	}
	for _, event := range f.events {
		if err := onEvent(event); err != nil {
			return err
		}
	}
	return nil
}

func (f *fakeAgentlandGateway) CreatePreview(_ context.Context, _ string, port int) (*models.GatewayPreviewInfo, error) {
	if f.createPreviewErr != nil {
		return nil, f.createPreviewErr
	}
	if f.previewInfo != nil {
		return f.previewInfo, nil
	}
	return &models.GatewayPreviewInfo{PreviewToken: "pv_123", PreviewURL: "/p/pv_123/", Port: port}, nil
}

func (f *fakeAgentlandGateway) GetFSTree(_ context.Context, _ string, targetPath string, _ int) (*models.GatewayFSTreeResp, error) {
	if f.fsErr != nil {
		return nil, f.fsErr
	}
	if f.fsTree != nil {
		return f.fsTree, nil
	}
	return &models.GatewayFSTreeResp{Root: targetPath}, nil
}

func (f *fakeAgentlandGateway) GetFSFile(_ context.Context, _ string, targetPath, encoding string) (*models.GatewayFSFileResp, error) {
	if f.fsErr != nil {
		return nil, f.fsErr
	}
	if f.fsFiles != nil {
		if item, ok := f.fsFiles[targetPath]; ok {
			return item, nil
		}
	}
	return &models.GatewayFSFileResp{Path: targetPath, Encoding: encoding, Content: ""}, nil
}

func (f *fakeAgentlandGateway) CreateExecContext(_ context.Context, _ string, language, cwd string) (*models.GatewayExecContextInfo, error) {
	if f.contextErr != nil {
		return nil, f.contextErr
	}
	if f.contextInfo != nil {
		return f.contextInfo, nil
	}
	return &models.GatewayExecContextInfo{ContextID: "ctx_123", Language: language, CWD: cwd, State: "ready"}, nil
}

func (f *fakeAgentlandGateway) ExecuteInContext(_ context.Context, _ string, contextID, code string, _ int) (*models.GatewayExecutionResult, error) {
	f.lastExecuteCode = code
	if f.executeErr != nil {
		return nil, f.executeErr
	}
	if f.executionResult != nil {
		return f.executionResult, nil
	}
	return &models.GatewayExecutionResult{ContextID: contextID, ExecutionID: "exec_123", ExitCode: 0}, nil
}

func (f *fakeAgentlandGateway) ProbePort(_ context.Context, _ string, _ int, _ string) (int, error) {
	if f.probeErr != nil {
		return 0, f.probeErr
	}
	if len(f.probeStatuses) == 0 {
		return 200, nil
	}
	status := f.probeStatuses[0]
	f.probeStatuses = f.probeStatuses[1:]
	return status, nil
}

func TestProjectUseCaseCreateAndUsage(t *testing.T) {
	repo := &fakeProjectRepo{plan: "pro"}
	jobRepo := &fakeJobRepo{}
	gateway := &fakeAgentlandGateway{}
	useCase := NewProjectUsecase(repo, jobRepo, gateway).(*projectUseCase)
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
	useCase := NewProjectUsecase(repo, &fakeJobRepo{}, &fakeAgentlandGateway{})
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
	useCase := NewProjectUsecase(repo, &fakeJobRepo{}, &fakeAgentlandGateway{}).(*projectUseCase)
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

func TestProjectUseCaseCreateGenerationSuccess(t *testing.T) {
	now := time.Date(2026, 3, 12, 13, 0, 0, 0, time.UTC)
	repo := &fakeProjectRepo{projects: map[string]*models.Project{
		"p_1": {ID: "p_1", OwnerID: "u_123", Name: "Demo", Status: "DRAFT", CreatedAt: now, UpdatedAt: now},
	}}
	jobRepo := &fakeJobRepo{}
	gateway := &fakeAgentlandGateway{events: []*models.AgentSSEEvent{
		{Event: "route", Data: []byte(`{"intent":"task","reason":"implementation"}`)},
		{Event: "session", Data: []byte(`{"session_id":"task-demo","workspace_path":"/workspace"}`)},
		{Event: "assistant_delta", Data: []byte(`{"content":"building ui"}`)},
		{Event: "done", Data: []byte(`{"session_id":"task-demo","status":"complete","iteration":1}`)},
	}}
	useCase := NewProjectUsecase(repo, jobRepo, gateway).(*projectUseCase)
	useCase.now = func() time.Time { return now }
	useCase.runAsync = func(fn func()) { fn() }
	principal := models.AuthPrincipal{UserID: "u_123"}

	resp, apiErr := useCase.CreateGeneration(context.Background(), principal, "p_1", &models.GenerationCreateReq{Prompt: "Build a dashboard", Deep: true})
	require.Nil(t, apiErr)
	require.Equal(t, "QUEUED", resp.Status)

	job, err := jobRepo.GetJobByID(context.Background(), "u_123", resp.JobID)
	require.NoError(t, err)
	require.Equal(t, "SUCCESS", job.Status)
	require.Equal(t, 100, job.Progress)
	require.Contains(t, job.Logs, "Generation complete")
	result, ok := job.Result.(map[string]any)
	require.True(t, ok)
	require.Equal(t, "p_1", result["project_id"])
	require.Equal(t, "DRAFT", repo.projects["p_1"].Status)
	require.Contains(t, repo.statusTransitions, "BUILDING")
	require.Len(t, repo.chatMessages["p_1"], 2)
	require.Equal(t, "user", repo.chatMessages["p_1"][0].Role)
	require.Equal(t, "Build a dashboard", repo.chatMessages["p_1"][0].Content)
	require.Equal(t, "assistant", repo.chatMessages["p_1"][1].Role)
	require.Equal(t, "building ui", repo.chatMessages["p_1"][1].Content)
	require.True(t, repo.chatMessages["p_1"][1].CreatedAt.After(repo.chatMessages["p_1"][0].CreatedAt))
	require.Equal(t, "sess_gateway_1", repo.chatSessions["p_1"].GatewaySessionID)
	require.Equal(t, "task-demo", repo.chatSessions["p_1"].AgentChatSessionID)
	require.Equal(t, 1, gateway.ensureCalls)
	require.NotNil(t, gateway.lastStreamReq)
	require.True(t, gateway.lastStreamReq.Deep)
}

func TestProjectUseCaseCreateGenerationFailure(t *testing.T) {
	now := time.Date(2026, 3, 12, 13, 30, 0, 0, time.UTC)
	repo := &fakeProjectRepo{projects: map[string]*models.Project{
		"p_1": {ID: "p_1", OwnerID: "u_123", Name: "Demo", Status: "DRAFT", CreatedAt: now, UpdatedAt: now},
	}}
	jobRepo := &fakeJobRepo{}
	gateway := &fakeAgentlandGateway{ensureErr: errors.New("gateway unavailable")}
	useCase := NewProjectUsecase(repo, jobRepo, gateway).(*projectUseCase)
	useCase.now = func() time.Time { return now }
	useCase.runAsync = func(fn func()) { fn() }
	principal := models.AuthPrincipal{UserID: "u_123"}

	resp, apiErr := useCase.CreateGeneration(context.Background(), principal, "p_1", &models.GenerationCreateReq{Prompt: "Build a dashboard"})
	require.Nil(t, apiErr)

	job, err := jobRepo.GetJobByID(context.Background(), "u_123", resp.JobID)
	require.NoError(t, err)
	require.Equal(t, "FAILED", job.Status)
	require.Contains(t, job.ErrorMessage, "gateway unavailable")
	require.Equal(t, "DRAFT", repo.projects["p_1"].Status)
}

func TestProjectUseCaseListMessages(t *testing.T) {
	now := time.Date(2026, 3, 13, 10, 0, 0, 0, time.UTC)
	repo := &fakeProjectRepo{
		projects: map[string]*models.Project{
			"p_1": {ID: "p_1", OwnerID: "u_123", Name: "Demo", Status: "DRAFT", CreatedAt: now, UpdatedAt: now},
		},
		chatMessages: map[string][]*models.ProjectChatMessage{
			"p_1": {
				{ID: "m_1", ProjectID: "p_1", OwnerID: "u_123", Role: "user", Content: "hello", CreatedAt: now},
				{ID: "m_2", ProjectID: "p_1", OwnerID: "u_123", Role: "assistant", Content: "hi", CreatedAt: now.Add(time.Minute)},
			},
		},
	}
	useCase := NewProjectUsecase(repo, &fakeJobRepo{}, &fakeAgentlandGateway{})
	resp, apiErr := useCase.ListMessages(context.Background(), models.AuthPrincipal{UserID: "u_123"}, "p_1", &models.ChatMessagesReq{})
	require.Nil(t, apiErr)
	require.Len(t, resp.Items, 2)
	require.Equal(t, "hello", resp.Items[0].Content)
	require.Equal(t, "assistant", resp.Items[1].Role)
}

func TestProjectUseCaseCreateMessage(t *testing.T) {
	now := time.Date(2026, 3, 13, 11, 0, 0, 0, time.UTC)
	repo := &fakeProjectRepo{projects: map[string]*models.Project{
		"p_1": {ID: "p_1", OwnerID: "u_123", Name: "Demo", Status: "DRAFT", CreatedAt: now, UpdatedAt: now},
	}}
	jobRepo := &fakeJobRepo{jobs: map[string]*models.Job{
		"job_gen_1": {
			ID:               "job_gen_1",
			OwnerID:          "u_123",
			ProjectID:        "p_1",
			Type:             "APP_GENERATION",
			Status:           "SUCCESS",
			GatewaySessionID: "gw_123",
			AgentSessionID:   "agent_123",
			WorkspacePath:    "/workspace",
			UpdatedAt:        now,
		},
	}}
	gateway := &fakeAgentlandGateway{events: []*models.AgentSSEEvent{
		{Event: "assistant_delta", Data: []byte(`{"content":"hello"}`)},
		{Event: "assistant_delta", Data: []byte(`{"content":" world"}`)},
		{Event: "done", Data: []byte(`{"status":"complete"}`)},
	}}
	useCase := NewProjectUsecase(repo, jobRepo, gateway).(*projectUseCase)
	useCase.now = func() time.Time { return now }
	deltas := make([]string, 0)
	resp, err := useCase.CreateMessage(context.Background(), models.AuthPrincipal{UserID: "u_123"}, "p_1", &models.ChatMessageCreateReq{Content: "Say hi", Deep: true}, func(delta string) error {
		deltas = append(deltas, delta)
		return nil
	})
	require.NoError(t, err)
	require.NotEmpty(t, resp.MessageID)
	require.Equal(t, []string{"hello", " world"}, deltas)
	require.Len(t, repo.chatMessages["p_1"], 2)
	require.Equal(t, "user", repo.chatMessages["p_1"][0].Role)
	require.Equal(t, "assistant", repo.chatMessages["p_1"][1].Role)
	require.Equal(t, "hello world", repo.chatMessages["p_1"][1].Content)
	require.Equal(t, "gw_123", repo.chatSessions["p_1"].GatewaySessionID)
	require.Equal(t, "agent_123", repo.chatSessions["p_1"].AgentChatSessionID)
	require.Equal(t, 0, gateway.ensureCalls)
	require.NotNil(t, gateway.lastStreamReq)
	require.True(t, gateway.lastStreamReq.Deep)
}

func TestProjectUseCaseFileTreeAndPreview(t *testing.T) {
	now := time.Date(2026, 3, 13, 12, 0, 0, 0, time.UTC)
	repo := &fakeProjectRepo{
		projects: map[string]*models.Project{
			"p_1": {ID: "p_1", OwnerID: "u_123", Name: "Demo", Status: "DRAFT", CreatedAt: now, UpdatedAt: now},
		},
		chatSessions: map[string]*models.ProjectChatSession{
			"p_1": {
				ProjectID:          "p_1",
				OwnerID:            "u_123",
				GatewaySessionID:   "gw_123",
				AgentChatSessionID: "agent_123",
				WorkspacePath:      "/workspace",
				CreatedAt:          now,
				UpdatedAt:          now,
				LastMessageAt:      now,
			},
		},
	}
	gateway := &fakeAgentlandGateway{
		fsTree: &models.GatewayFSTreeResp{
			Root: "/workspace",
			Nodes: []models.GatewayFSTreeNode{
				{Path: "src", Name: "src", Type: "dir"},
				{Path: "src/main.tsx", Name: "main.tsx", Type: "file", Size: 12},
			},
		},
		fsFiles: map[string]*models.GatewayFSFileResp{
			"/workspace/package.json": {Path: "/workspace/package.json", Encoding: "utf8", Content: `{"scripts":{"dev":"vite"}}`},
		},
		probeStatuses: []int{502, 200},
	}
	useCase := NewProjectUsecase(repo, &fakeJobRepo{}, gateway).(*projectUseCase)
	useCase.now = func() time.Time { return now }
	principal := models.AuthPrincipal{UserID: "u_123"}

	treeResp, apiErr := useCase.FileTree(context.Background(), principal, "p_1", &models.FileTreeReq{Path: "/workspace", Depth: 3})
	require.Nil(t, apiErr)
	require.Equal(t, "/workspace", treeResp.Root)
	require.Len(t, treeResp.Nodes, 1)
	require.Equal(t, "folder", treeResp.Nodes[0].Type)
	require.Len(t, treeResp.Nodes[0].Children, 1)
	require.Equal(t, "/workspace/src/main.tsx", treeResp.Nodes[0].Children[0].Path)

	previewResp, apiErr := useCase.StartPreview(context.Background(), principal, "p_1", &models.PreviewStartReq{Port: 3000})
	require.Nil(t, apiErr)
	require.Equal(t, "RUNNING", previewResp.Status)
	require.Equal(t, "/p/pv_123/", previewResp.PreviewURL)
	require.Equal(t, 0, gateway.ensureCalls)
}

func TestProjectUseCasePreviewSupportsStaticHTML(t *testing.T) {
	now := time.Date(2026, 3, 14, 0, 10, 0, 0, time.UTC)
	repo := &fakeProjectRepo{
		projects: map[string]*models.Project{
			"p_1": {ID: "p_1", OwnerID: "u_123", Name: "Static Demo", Status: "DRAFT", CreatedAt: now, UpdatedAt: now},
		},
		chatSessions: map[string]*models.ProjectChatSession{
			"p_1": {
				ProjectID:          "p_1",
				OwnerID:            "u_123",
				GatewaySessionID:   "gw_123",
				AgentChatSessionID: "agent_123",
				WorkspacePath:      "/workspace",
				CreatedAt:          now,
				UpdatedAt:          now,
				LastMessageAt:      now,
			},
		},
	}
	gateway := &fakeAgentlandGateway{
		fsTree: &models.GatewayFSTreeResp{
			Root: "/workspace",
			Nodes: []models.GatewayFSTreeNode{
				{Path: "README.md", Name: "README.md", Type: "file", Size: 12},
				{Path: "package.json", Name: "package.json", Type: "file", Size: 12},
				{Path: "src", Name: "src", Type: "dir"},
				{Path: "src/index.html", Name: "index.html", Type: "file", Size: 12},
			},
		},
		fsFiles: map[string]*models.GatewayFSFileResp{
			"/workspace/package.json": {Path: "/workspace/package.json", Encoding: "utf8", Content: `{"name":"simple-node-frontend","scripts":{"start":"echo \"No build step required. Open src/index.html in a browser.\""}}`},
		},
		probeStatuses: []int{502, 200},
	}
	useCase := NewProjectUsecase(repo, &fakeJobRepo{}, gateway).(*projectUseCase)
	useCase.now = func() time.Time { return now }

	previewResp, apiErr := useCase.StartPreview(context.Background(), models.AuthPrincipal{UserID: "u_123"}, "p_1", &models.PreviewStartReq{Port: 3000})
	require.Nil(t, apiErr)
	require.Equal(t, "RUNNING", previewResp.Status)
	require.Contains(t, gateway.lastExecuteCode, "python3 -m http.server 3000 --bind 0.0.0.0")
	require.Contains(t, gateway.lastExecuteCode, "cd '/workspace/src'")
}

func TestProjectUseCaseWorkspaceRequiresExistingRuntime(t *testing.T) {
	now := time.Date(2026, 3, 13, 12, 30, 0, 0, time.UTC)
	repo := &fakeProjectRepo{projects: map[string]*models.Project{
		"p_1": {ID: "p_1", OwnerID: "u_123", Name: "Demo", Status: "DRAFT", CreatedAt: now, UpdatedAt: now},
	}}
	gateway := &fakeAgentlandGateway{}
	useCase := NewProjectUsecase(repo, &fakeJobRepo{}, gateway).(*projectUseCase)
	useCase.now = func() time.Time { return now }
	principal := models.AuthPrincipal{UserID: "u_123"}

	_, apiErr := useCase.FileTree(context.Background(), principal, "p_1", &models.FileTreeReq{Path: "/workspace", Depth: 3})
	require.NotNil(t, apiErr)
	require.Equal(t, 409, apiErr.StatusCode)
	require.Equal(t, "runtime_unavailable", apiErr.Msg)
	require.Equal(t, 0, gateway.ensureCalls)

	_, apiErr = useCase.StartPreview(context.Background(), principal, "p_1", &models.PreviewStartReq{Port: 3000})
	require.NotNil(t, apiErr)
	require.Equal(t, 409, apiErr.StatusCode)
	require.Equal(t, "runtime_unavailable", apiErr.Msg)
	require.Equal(t, 0, gateway.ensureCalls)
}

func TestProjectUseCaseCreateMessageRequiresExistingRuntime(t *testing.T) {
	now := time.Date(2026, 3, 13, 12, 45, 0, 0, time.UTC)
	repo := &fakeProjectRepo{projects: map[string]*models.Project{
		"p_1": {ID: "p_1", OwnerID: "u_123", Name: "Demo", Status: "DRAFT", CreatedAt: now, UpdatedAt: now},
	}}
	gateway := &fakeAgentlandGateway{}
	useCase := NewProjectUsecase(repo, &fakeJobRepo{}, gateway).(*projectUseCase)
	useCase.now = func() time.Time { return now }

	_, err := useCase.CreateMessage(context.Background(), models.AuthPrincipal{UserID: "u_123"}, "p_1", &models.ChatMessageCreateReq{Content: "Say hi"}, nil)
	require.Error(t, err)
	require.Equal(t, "runtime_unavailable", err.Error())
	require.Equal(t, 0, gateway.ensureCalls)
	_, hasSession := repo.chatSessions["p_1"]
	require.False(t, hasSession)
}
