package biz

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/autherr"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/response"
	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/token"
)

const (
	defaultWorkspaceRoot         = "/workspace"
	defaultWorkspaceTreeDepth    = 3
	maxWorkspaceTreeDepth        = 20
	defaultPreviewPort           = 3000
	previewReadinessTimeout      = 20 * time.Second
	previewReadinessPollInterval = 2 * time.Second
	previewContextLanguage       = "bash"
	previewExecTimeoutMs         = 120000
)

type workspaceTreeNode struct {
	Path     string
	Name     string
	Type     string
	Size     int64
	Children []*workspaceTreeNode
}

type previewPackageJSON struct {
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

func (u *projectUseCase) FileTree(ctx context.Context, principal models.AuthPrincipal, projectID string, req *models.FileTreeReq) (*models.FileTreeResp, *response.APIError) {
	depth := req.Depth
	if depth <= 0 {
		depth = defaultWorkspaceTreeDepth
	}
	if depth > maxWorkspaceTreeDepth {
		depth = maxWorkspaceTreeDepth
	}
	rootPath := normalizedWorkspacePath(req.Path)
	return withWorkspaceSessionRetry(u, ctx, principal, projectID, func(project *models.Project, session *models.ProjectChatSession) (*models.FileTreeResp, error) {
		_ = project
		fsTree, err := u.gateway.GetFSTree(ctx, session.GatewaySessionID, rootPath, depth)
		if err != nil {
			return nil, err
		}
		return &models.FileTreeResp{
			Root:  firstNonEmpty(strings.TrimSpace(fsTree.Root), rootPath),
			Nodes: buildWorkspaceFileNodes(firstNonEmpty(strings.TrimSpace(fsTree.Root), rootPath), fsTree.Nodes),
		}, nil
	})
}

func (u *projectUseCase) FileContent(ctx context.Context, principal models.AuthPrincipal, projectID string, req *models.FileContentReq) (*models.FileContentResp, *response.APIError) {
	targetPath := normalizedWorkspacePath(req.Path)
	return withWorkspaceSessionRetry(u, ctx, principal, projectID, func(project *models.Project, session *models.ProjectChatSession) (*models.FileContentResp, error) {
		_ = project
		fileResp, err := u.gateway.GetFSFile(ctx, session.GatewaySessionID, targetPath, "utf8")
		if err != nil {
			return nil, err
		}
		return &models.FileContentResp{
			Path:     strings.TrimSpace(fileResp.Path),
			Language: detectFileLanguage(fileResp.Path),
			Content:  fileResp.Content,
			SHA:      "",
		}, nil
	})
}

func (u *projectUseCase) Download(ctx context.Context, principal models.AuthPrincipal, projectID string) (*models.WorkspaceArchive, *response.APIError) {
	return withWorkspaceSessionRetry(u, ctx, principal, projectID, func(project *models.Project, session *models.ProjectChatSession) (*models.WorkspaceArchive, error) {
		fsTree, err := u.gateway.GetFSTree(ctx, session.GatewaySessionID, defaultWorkspaceRoot, maxWorkspaceTreeDepth)
		if err != nil {
			return nil, err
		}
		root := firstNonEmpty(strings.TrimSpace(fsTree.Root), defaultWorkspaceRoot)
		filePaths := collectWorkspaceFilePaths(root, fsTree.Nodes)

		buffer := bytes.NewBuffer(nil)
		zipWriter := zip.NewWriter(buffer)
		for _, filePath := range filePaths {
			fileResp, readErr := u.gateway.GetFSFile(ctx, session.GatewaySessionID, filePath, "base64")
			if readErr != nil {
				return nil, readErr
			}
			payload, decodeErr := base64.StdEncoding.DecodeString(fileResp.Content)
			if decodeErr != nil {
				return nil, response.InternalError()
			}
			entryName := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(fileResp.Path), root), "/")
			if entryName == "" {
				entryName = path.Base(strings.TrimSpace(fileResp.Path))
			}
			entryWriter, createErr := zipWriter.Create(entryName)
			if createErr != nil {
				return nil, response.InternalError()
			}
			if _, writeErr := entryWriter.Write(payload); writeErr != nil {
				return nil, response.InternalError()
			}
		}
		if err := zipWriter.Close(); err != nil {
			return nil, response.InternalError()
		}
		return &models.WorkspaceArchive{
			FileName:    buildArchiveFileName(project.Name, project.ID),
			ContentType: "application/zip",
			Content:     buffer.Bytes(),
		}, nil
	})
}

func (u *projectUseCase) StartPreview(ctx context.Context, principal models.AuthPrincipal, projectID string, req *models.PreviewStartReq) (*models.PreviewStartResp, *response.APIError) {
	port := req.Port
	if port <= 0 || port > 65535 {
		port = defaultPreviewPort
	}
	return withWorkspaceSessionRetry(u, ctx, principal, projectID, func(project *models.Project, session *models.ProjectChatSession) (*models.PreviewStartResp, error) {
		status, previewInfo, err := u.ensureProjectPreview(ctx, project, session, port)
		if err != nil {
			return nil, err
		}
		resp := &models.PreviewStartResp{Status: status}
		if previewInfo != nil {
			resp.PreviewID = previewInfo.PreviewToken
			resp.PreviewURL = previewInfo.PreviewURL
		}
		return resp, nil
	})
}

func (u *projectUseCase) PreviewStatus(ctx context.Context, principal models.AuthPrincipal, projectID string) (*models.PreviewStatusResp, *response.APIError) {
	return withWorkspaceSessionRetry(u, ctx, principal, projectID, func(project *models.Project, session *models.ProjectChatSession) (*models.PreviewStatusResp, error) {
		status, previewInfo, err := u.ensureProjectPreview(ctx, project, session, defaultPreviewPort)
		if err != nil {
			return nil, err
		}
		resp := &models.PreviewStatusResp{Status: status}
		if previewInfo != nil {
			resp.PreviewID = previewInfo.PreviewToken
			resp.PreviewURL = previewInfo.PreviewURL
			if !previewInfo.ExpiresAt.IsZero() {
				resp.LastHeartbeatAt = previewInfo.ExpiresAt.UTC().Format(time.RFC3339)
			}
		}
		return resp, nil
	})
}

func (u *projectUseCase) projectSessionMutex(ownerID, projectID string) *sync.Mutex {
	key := strings.TrimSpace(ownerID) + ":" + strings.TrimSpace(projectID)
	mutex, _ := u.sessionLocks.LoadOrStore(key, &sync.Mutex{})
	return mutex.(*sync.Mutex)
}

func (u *projectUseCase) loadProjectRuntime(ctx context.Context, principal models.AuthPrincipal, projectID string) (*models.Project, *models.ProjectChatSession, *response.APIError) {
	if strings.TrimSpace(principal.UserID) == "" {
		return nil, nil, response.UnauthorizedError()
	}
	project, err := u.repo.GetProjectByID(ctx, principal.UserID, strings.TrimSpace(projectID))
	if err != nil {
		return nil, nil, u.apiError(err)
	}
	session, err := u.ensureProjectChatSession(ctx, principal.UserID, project)
	if err != nil {
		return nil, nil, gatewayAPIError(err)
	}
	return project, session, nil
}

func withWorkspaceSessionRetry[T any](u *projectUseCase, ctx context.Context, principal models.AuthPrincipal, projectID string, operation func(project *models.Project, session *models.ProjectChatSession) (T, error)) (T, *response.APIError) {
	var zero T
	project, session, apiErr := u.loadProjectRuntime(ctx, principal, projectID)
	if apiErr != nil {
		return zero, apiErr
	}
	result, err := operation(project, session)
	if err == nil {
		return result, nil
	}
	if !shouldRecoverWorkspaceSession(err) {
		return zero, gatewayAPIError(err)
	}
	refreshedSession, refreshErr := u.refreshProjectChatSession(ctx, principal.UserID, project, session)
	if refreshErr != nil {
		return zero, gatewayAPIError(refreshErr)
	}
	result, err = operation(project, refreshedSession)
	if err != nil {
		return zero, gatewayAPIError(err)
	}
	return result, nil
}

func shouldRecoverWorkspaceSession(err error) bool {
	var gatewayErr *models.GatewayResponseError
	if !errors.As(err, &gatewayErr) {
		return false
	}
	return gatewayErr.StatusCode == 401 || gatewayErr.StatusCode == 404
}

func (u *projectUseCase) refreshProjectChatSession(ctx context.Context, ownerID string, project *models.Project, previous *models.ProjectChatSession) (*models.ProjectChatSession, error) {
	mutex := u.projectSessionMutex(ownerID, project.ID)
	mutex.Lock()
	defer mutex.Unlock()

	current, err := u.repo.GetProjectChatSession(ctx, ownerID, project.ID)
	if err != nil {
		return nil, err
	}
	if current != nil && previous != nil && strings.TrimSpace(current.GatewaySessionID) != "" && strings.TrimSpace(current.GatewaySessionID) != strings.TrimSpace(previous.GatewaySessionID) {
		return current, nil
	}

	job, err := u.latestProjectRuntime(ctx, ownerID, project.ID)
	if err != nil {
		return nil, err
	}
	seedGatewaySessionID := strings.TrimSpace(job.GatewaySessionID)
	if seedGatewaySessionID == "" || (previous != nil && seedGatewaySessionID == strings.TrimSpace(previous.GatewaySessionID)) {
		return nil, autherr.ErrProjectRuntimeUnavailable
	}
	seedAgentChatSessionID := strings.TrimSpace(job.AgentSessionID)
	seedWorkspacePath := strings.TrimSpace(job.WorkspacePath)
	if seedAgentChatSessionID == "" {
		if current != nil && strings.TrimSpace(current.AgentChatSessionID) != "" {
			seedAgentChatSessionID = strings.TrimSpace(current.AgentChatSessionID)
		} else if previous != nil && strings.TrimSpace(previous.AgentChatSessionID) != "" {
			seedAgentChatSessionID = strings.TrimSpace(previous.AgentChatSessionID)
		} else {
			seedAgentChatSessionID = token.NewID("chat")
		}
	}
	if seedWorkspacePath == "" {
		if current != nil && strings.TrimSpace(current.WorkspacePath) != "" {
			seedWorkspacePath = strings.TrimSpace(current.WorkspacePath)
		} else if previous != nil && strings.TrimSpace(previous.WorkspacePath) != "" {
			seedWorkspacePath = strings.TrimSpace(previous.WorkspacePath)
		} else {
			seedWorkspacePath = defaultGenerationWorkspace
		}
	}
	return u.repo.UpsertProjectChatSession(ctx, &models.UpsertProjectChatSessionInput{
		ProjectID:          project.ID,
		OwnerID:            ownerID,
		GatewaySessionID:   seedGatewaySessionID,
		AgentChatSessionID: seedAgentChatSessionID,
		WorkspacePath:      seedWorkspacePath,
		Now:                u.now().UTC(),
	})
}

func (u *projectUseCase) ensureProjectPreview(ctx context.Context, project *models.Project, session *models.ProjectChatSession, port int) (string, *models.GatewayPreviewInfo, error) {
	_ = project
	if previewPortReady(ctx, u.gateway, session.GatewaySessionID, port) {
		previewInfo, err := u.gateway.CreatePreview(ctx, session.GatewaySessionID, port)
		if err != nil {
			return "", nil, err
		}
		return "RUNNING", previewInfo, nil
	}

	startCommand, err := u.resolvePreviewStartCommand(ctx, session)
	if err != nil {
		return "", nil, err
	}
	ctxInfo, err := u.gateway.CreateExecContext(ctx, session.GatewaySessionID, previewContextLanguage, firstNonEmpty(strings.TrimSpace(session.WorkspacePath), defaultWorkspaceRoot))
	if err != nil {
		return "", nil, err
	}
	_, err = u.gateway.ExecuteInContext(ctx, session.GatewaySessionID, ctxInfo.ContextID, startCommand, previewExecTimeoutMs)
	if err != nil {
		return "", nil, err
	}

	readyCtx, cancel := context.WithTimeout(ctx, previewReadinessTimeout)
	defer cancel()
	for {
		if previewPortReady(readyCtx, u.gateway, session.GatewaySessionID, port) {
			previewInfo, previewErr := u.gateway.CreatePreview(readyCtx, session.GatewaySessionID, port)
			if previewErr != nil {
				return "", nil, previewErr
			}
			return "RUNNING", previewInfo, nil
		}
		select {
		case <-readyCtx.Done():
			previewInfo, previewErr := u.gateway.CreatePreview(ctx, session.GatewaySessionID, port)
			if previewErr != nil {
				return "STARTING", nil, nil
			}
			return "STARTING", previewInfo, nil
		case <-time.After(previewReadinessPollInterval):
		}
	}
}

func (u *projectUseCase) resolvePreviewStartCommand(ctx context.Context, session *models.ProjectChatSession) (string, error) {
	workspacePath := firstNonEmpty(strings.TrimSpace(session.WorkspacePath), defaultWorkspaceRoot)
	packageManager := "npm"
	staticRoot := ""
	if tree, treeErr := u.gateway.GetFSTree(ctx, session.GatewaySessionID, workspacePath, 2); treeErr == nil {
		packageManager = previewPackageManager(tree.Nodes)
		staticRoot = previewStaticRoot(workspacePath, tree.Nodes)
	}

	packageJSON, err := u.gateway.GetFSFile(ctx, session.GatewaySessionID, path.Join(workspacePath, "package.json"), "utf8")
	if err == nil {
		var parsed previewPackageJSON
		if err := json.Unmarshal([]byte(packageJSON.Content), &parsed); err != nil {
			return "", &models.GatewayResponseError{StatusCode: 500, Message: "invalid package.json in workspace"}
		}
		if staticRoot != "" && shouldServeStaticPreview(parsed) {
			return previewStaticServeCommand(staticRoot), nil
		}
		installCommand := previewInstallCommand(packageManager)
		scriptCommand := previewScriptCommand(packageManager, parsed.Scripts)
		if scriptCommand != "" {
			return strings.TrimSpace(strings.Join([]string{
				"set -e",
				"cd " + shellQuote(workspacePath),
				"if command -v lsof >/dev/null 2>&1 && lsof -iTCP:" + shellQuoteInt(defaultPreviewPort) + " -sTCP:LISTEN >/dev/null 2>&1; then exit 0; fi",
				"if [ ! -d node_modules ]; then " + installCommand + "; fi",
				"nohup sh -lc " + shellQuote(scriptCommand) + " >/tmp/agentland-preview.log 2>&1 &",
				"sleep 1",
			}, "\n")), nil
		}
		if staticRoot != "" {
			return previewStaticServeCommand(staticRoot), nil
		}
		return "", &models.GatewayResponseError{StatusCode: 400, Message: "workspace package.json is missing dev/start script"}
	}
	if isGatewayStatus(err, 404) && staticRoot != "" {
		return previewStaticServeCommand(staticRoot), nil
	}
	return "", err
}

func previewPortReady(ctx context.Context, gateway AgentlandGateway, gatewaySessionID string, port int) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	statusCode, err := gateway.ProbePort(probeCtx, gatewaySessionID, port, "/")
	if err != nil {
		return false
	}
	return statusCode > 0 && statusCode != 502 && statusCode != 503 && statusCode != 504
}

func previewPackageManager(nodes []models.GatewayFSTreeNode) string {
	for _, node := range nodes {
		name := strings.ToLower(strings.TrimSpace(node.Name))
		switch name {
		case "pnpm-lock.yaml":
			return "pnpm"
		case "yarn.lock":
			return "yarn"
		}
	}
	return "npm"
}

func previewInstallCommand(packageManager string) string {
	switch packageManager {
	case "pnpm":
		return "corepack enable >/dev/null 2>&1 || true; pnpm install"
	case "yarn":
		return "corepack enable >/dev/null 2>&1 || true; yarn install"
	default:
		return "npm install"
	}
}

func previewScriptCommand(packageManager string, scripts map[string]string) string {
	if len(scripts) == 0 {
		return ""
	}
	hasDev := strings.TrimSpace(scripts["dev"]) != ""
	hasStart := strings.TrimSpace(scripts["start"]) != ""
	switch packageManager {
	case "pnpm":
		if hasDev {
			return "pnpm run dev -- --host 0.0.0.0 --port 3000"
		}
		if hasStart {
			return "pnpm run start -- --host 0.0.0.0 --port 3000"
		}
	case "yarn":
		if hasDev {
			return "yarn dev --host 0.0.0.0 --port 3000"
		}
		if hasStart {
			return "yarn start --host 0.0.0.0 --port 3000"
		}
	default:
		if hasDev {
			return "npm run dev -- --host 0.0.0.0 --port 3000"
		}
		if hasStart {
			return "npm run start -- --host 0.0.0.0 --port 3000"
		}
	}
	return ""
}

func shouldServeStaticPreview(pkg previewPackageJSON) bool {
	return strings.TrimSpace(pkg.Scripts["dev"]) == "" && len(pkg.Dependencies) == 0 && len(pkg.DevDependencies) == 0
}

func previewStaticRoot(workspacePath string, nodes []models.GatewayFSTreeNode) string {
	if len(nodes) == 0 {
		return ""
	}
	normalized := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		if mapWorkspaceNodeType(node.Type) != "file" {
			continue
		}
		normalized[normalizeTreeNodePath(workspacePath, node.Path)] = struct{}{}
	}
	candidates := []string{
		path.Join(workspacePath, "index.html"),
		path.Join(workspacePath, "src/index.html"),
		path.Join(workspacePath, "public/index.html"),
	}
	for _, candidate := range candidates {
		if _, ok := normalized[path.Clean(candidate)]; ok {
			return path.Dir(path.Clean(candidate))
		}
	}
	for itemPath := range normalized {
		if strings.HasSuffix(strings.ToLower(itemPath), "/index.html") {
			return path.Dir(itemPath)
		}
	}
	for itemPath := range normalized {
		if path.Ext(strings.ToLower(itemPath)) == ".html" && path.Dir(itemPath) == path.Clean(workspacePath) {
			return path.Clean(workspacePath)
		}
	}
	return ""
}

func previewStaticServeCommand(root string) string {
	return strings.TrimSpace(strings.Join([]string{
		"set -e",
		"cd " + shellQuote(root),
		"if command -v lsof >/dev/null 2>&1 && lsof -iTCP:" + shellQuoteInt(defaultPreviewPort) + " -sTCP:LISTEN >/dev/null 2>&1; then exit 0; fi",
		"nohup sh -lc " + shellQuote("python3 -m http.server 3000 --bind 0.0.0.0") + " >/tmp/agentland-preview.log 2>&1 &",
		"sleep 1",
	}, "\n"))
}

func isGatewayStatus(err error, statusCode int) bool {
	var gatewayErr *models.GatewayResponseError
	return errors.As(err, &gatewayErr) && gatewayErr.StatusCode == statusCode
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func shellQuoteInt(value int) string {
	return strconv.Itoa(value)
}

func normalizedWorkspacePath(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return defaultWorkspaceRoot
	}
	if strings.HasPrefix(trimmed, "/") {
		return path.Clean(trimmed)
	}
	return path.Clean(path.Join(defaultWorkspaceRoot, trimmed))
}

func gatewayAPIError(err error) *response.APIError {
	if err == nil {
		return nil
	}
	if errors.Is(err, autherr.ErrProjectRuntimeUnavailable) {
		return response.RuntimeUnavailableError()
	}
	var gatewayErr *models.GatewayResponseError
	if errorsAs(err, &gatewayErr) {
		switch gatewayErr.StatusCode {
		case 400:
			return response.InvalidArgumentError("request", firstNonEmpty(strings.TrimSpace(gatewayErr.Message), "invalid gateway request"))
		case 401:
			return response.RuntimeUnavailableError()
		case 404:
			return response.NotFoundError()
		}
	}
	return response.InternalError()
}

func buildWorkspaceFileNodes(root string, rawNodes []models.GatewayFSTreeNode) []models.FileNode {
	sorted := append([]models.GatewayFSTreeNode{}, rawNodes...)
	sort.Slice(sorted, func(i, j int) bool {
		leftPath := normalizeTreeNodePath(root, sorted[i].Path)
		rightPath := normalizeTreeNodePath(root, sorted[j].Path)
		leftDepth := strings.Count(strings.TrimPrefix(leftPath, root), "/")
		rightDepth := strings.Count(strings.TrimPrefix(rightPath, root), "/")
		if leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return leftPath < rightPath
	})
	index := map[string]*workspaceTreeNode{}
	roots := make([]*workspaceTreeNode, 0)
	for _, item := range sorted {
		nodeType := mapWorkspaceNodeType(item.Type)
		if nodeType == "" {
			continue
		}
		absolutePath := normalizeTreeNodePath(root, item.Path)
		node := &workspaceTreeNode{Path: absolutePath, Name: item.Name, Type: nodeType, Size: item.Size}
		index[absolutePath] = node
		parentPath := path.Dir(absolutePath)
		if parentPath == "." || parentPath == "/" || parentPath == root {
			roots = append(roots, node)
			continue
		}
		parent, ok := index[parentPath]
		if !ok {
			roots = append(roots, node)
			continue
		}
		parent.Children = append(parent.Children, node)
	}
	sortWorkspaceTree(roots)
	result := make([]models.FileNode, 0, len(roots))
	for _, node := range roots {
		result = append(result, convertWorkspaceNode(node))
	}
	return result
}

func sortWorkspaceTree(nodes []*workspaceTreeNode) {
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Type != nodes[j].Type {
			return nodes[i].Type == "folder"
		}
		return strings.ToLower(nodes[i].Name) < strings.ToLower(nodes[j].Name)
	})
	for _, node := range nodes {
		if len(node.Children) > 0 {
			sortWorkspaceTree(node.Children)
		}
	}
}

func convertWorkspaceNode(node *workspaceTreeNode) models.FileNode {
	converted := models.FileNode{
		Path: node.Path,
		Name: node.Name,
		Type: node.Type,
		Size: node.Size,
	}
	if len(node.Children) > 0 {
		converted.Children = make([]models.FileNode, 0, len(node.Children))
		for _, child := range node.Children {
			converted.Children = append(converted.Children, convertWorkspaceNode(child))
		}
	}
	return converted
}

func collectWorkspaceFilePaths(root string, rawNodes []models.GatewayFSTreeNode) []string {
	paths := make([]string, 0)
	for _, item := range rawNodes {
		if mapWorkspaceNodeType(item.Type) != "file" {
			continue
		}
		paths = append(paths, normalizeTreeNodePath(root, item.Path))
	}
	sort.Strings(paths)
	return paths
}

func normalizeTreeNodePath(root, itemPath string) string {
	trimmedRoot := firstNonEmpty(strings.TrimSpace(root), defaultWorkspaceRoot)
	trimmedPath := strings.TrimSpace(itemPath)
	if trimmedPath == "" {
		return trimmedRoot
	}
	if strings.HasPrefix(trimmedPath, "/") {
		return path.Clean(trimmedPath)
	}
	return path.Clean(path.Join(trimmedRoot, trimmedPath))
}

func mapWorkspaceNodeType(nodeType string) string {
	switch strings.ToLower(strings.TrimSpace(nodeType)) {
	case "dir", "folder":
		return "folder"
	case "file":
		return "file"
	default:
		return ""
	}
}

func detectFileLanguage(filePath string) string {
	switch strings.ToLower(path.Ext(strings.TrimSpace(filePath))) {
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx", ".mjs", ".cjs":
		return "javascript"
	case ".json":
		return "json"
	case ".md":
		return "markdown"
	case ".html":
		return "html"
	case ".css":
		return "css"
	case ".go":
		return "go"
	case ".py":
		return "python"
	case ".yml", ".yaml":
		return "yaml"
	case ".sh":
		return "shell"
	default:
		return "plaintext"
	}
}

func buildArchiveFileName(projectName, projectID string) string {
	base := strings.ToLower(strings.TrimSpace(projectName))
	if base == "" {
		base = strings.TrimSpace(projectID)
	}
	base = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '-' || r == '_':
			return r
		default:
			return '-'
		}
	}, base)
	base = strings.Trim(base, "-")
	if base == "" {
		base = strings.TrimSpace(projectID)
	}
	return base + ".zip"
}

func errorsAs(err error, target any) bool {
	switch t := target.(type) {
	case **models.GatewayResponseError:
		gatewayErr, ok := err.(*models.GatewayResponseError)
		if !ok {
			return false
		}
		*t = gatewayErr
		return true
	default:
		return false
	}
}
