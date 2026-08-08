package data

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/configs"
	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/spf13/viper"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const agentlandSessionHeader = "x-agentland-session"

var imageDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

type gatewayEnvelope struct {
	Msg  string          `json:"msg"`
	Code int             `json:"code"`
	Data json.RawMessage `json:"data"`
}

type agentlandGatewayClient struct {
	baseURL                       string
	httpClient                    *http.Client
	streamClient                  *http.Client
	maxSnapshotBytes              int64
	runtimeName, runtimeNamespace string
	previewPublicURLTemplate      string
	publisherToken                string
}

func NewAgentlandGatewayClient() biz.AgentlandGateway {
	transport := otelhttp.NewTransport(http.DefaultTransport)
	return &agentlandGatewayClient{
		baseURL:                  strings.TrimRight(strings.TrimSpace(viper.GetString("agentland-gateway.url")), "/"),
		httpClient:               &http.Client{Transport: transport, Timeout: 65 * time.Second},
		streamClient:             &http.Client{Transport: transport},
		maxSnapshotBytes:         viper.GetInt64("storage.s3.max_snapshot_bytes"),
		runtimeName:              strings.TrimSpace(viper.GetString("agentland-gateway.runtime.name")),
		runtimeNamespace:         strings.TrimSpace(viper.GetString("agentland-gateway.runtime.namespace")),
		previewPublicURLTemplate: strings.TrimSpace(viper.GetString("preview.public_url_template")),
		publisherToken:           strings.TrimSpace(viper.GetString("agentland-gateway.publisher_token")),
	}
}

func (c *agentlandGatewayClient) EnsureRuntime(ctx context.Context, sessionID string) (result string, err error) {
	ctx, span := startGatewaySpan(ctx, "gateway.ensure_runtime", attribute.String("gateway.session_id", sessionID))
	defer finishGatewaySpan(span, &err)
	query := url.Values{}
	query.Set("runtime", c.runtimeName)
	query.Set("runtime_namespace", c.runtimeNamespace)
	requestPath := "/api/agent-sessions/invocations/health?" + query.Encode()
	var lastErr error
	for {
		resp, err := c.do(ctx, http.MethodGet, requestPath, sessionID, nil)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			actualSession := strings.TrimSpace(resp.Header.Get(agentlandSessionHeader))
			if actualSession == "" {
				actualSession = strings.TrimSpace(sessionID)
			}
			if strings.TrimSpace(sessionID) != "" && actualSession != "" && actualSession != strings.TrimSpace(sessionID) {
				return "", &models.GatewayResponseError{StatusCode: http.StatusConflict, Code: "PROJECT_RUNTIME_EXPIRED", Message: "project runtime session was replaced"}
			}
			if strings.TrimSpace(sessionID) == "" && actualSession != "" {
				sessionID = actualSession
			}
			if readErr == nil && resp.StatusCode == http.StatusOK && actualSession != "" {
				var health struct {
					Status string `json:"status"`
				}
				if json.Unmarshal(body, &health) == nil && strings.EqualFold(health.Status, "ok") {
					return actualSession, nil
				}
			}
			if readErr != nil {
				lastErr = readErr
			} else {
				lastErr = decodeGatewayError(resp.StatusCode, body)
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("ensure project runtime: %w", errors.Join(lastErr, ctx.Err()))
		case <-time.After(2 * time.Second):
		}
	}
}

func (c *agentlandGatewayClient) StreamChat(ctx context.Context, sessionID, conversationID, message string, onEvent func(*models.AgentEvent) error) (err error) {
	ctx, span := startGatewaySpan(ctx, "gateway.stream_chat", attribute.String("gateway.session_id", sessionID), attribute.String("agent.conversation_id", conversationID))
	defer finishGatewaySpan(span, &err)
	payload, err := json.Marshal(map[string]any{"conversation_id": conversationID, "message": message, "capture_trajectory": true})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/agent-sessions/invocations/api/chat", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(agentlandSessionHeader, strings.TrimSpace(sessionID))
	resp, err := c.streamClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if actual := strings.TrimSpace(resp.Header.Get(agentlandSessionHeader)); actual != "" && actual != strings.TrimSpace(sessionID) {
		return &models.GatewayResponseError{StatusCode: http.StatusConflict, Code: "PROJECT_RUNTIME_EXPIRED", Message: "project runtime session was replaced"}
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return decodeGatewayError(resp.StatusCode, body)
	}
	return parseAgentEvents(resp.Body, onEvent)
}

func (c *agentlandGatewayClient) GetWorkspaceSnapshot(ctx context.Context, sessionID string) (result []byte, err error) {
	ctx, span := startGatewaySpan(ctx, "gateway.workspace_snapshot", attribute.String("gateway.session_id", sessionID))
	defer finishGatewaySpan(span, &err)
	resp, err := c.do(ctx, http.MethodGet, "/api/agent-sessions/invocations/api/workspace/snapshot", sessionID, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	limit := c.snapshotLimit()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, decodeGatewayError(resp.StatusCode, data)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("workspace snapshot exceeds %d bytes", limit)
	}
	return data, nil
}

func (c *agentlandGatewayClient) RestoreWorkspaceSnapshot(ctx context.Context, sessionID string, snapshot []byte) (err error) {
	if int64(len(snapshot)) > c.snapshotLimit() {
		return fmt.Errorf("workspace snapshot exceeds %d bytes", c.snapshotLimit())
	}
	ctx, span := startGatewaySpan(ctx, "gateway.workspace_restore", attribute.String("gateway.session_id", sessionID), attribute.Int("workspace.snapshot_bytes", len(snapshot)))
	defer finishGatewaySpan(span, &err)
	resp, err := c.do(ctx, http.MethodPost, "/api/agent-sessions/invocations/api/workspace/snapshot", sessionID, bytes.NewReader(snapshot))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return decodeGatewayError(resp.StatusCode, body)
	}
	return nil
}

func (c *agentlandGatewayClient) snapshotLimit() int64 {
	if c.maxSnapshotBytes > 0 {
		return c.maxSnapshotBytes
	}
	return 8 << 20
}

func (c *agentlandGatewayClient) ReplayDecisions(ctx context.Context, sessionID string, records []models.RunTrajectoryRecord) (result *models.ReplayRunResp, err error) {
	return c.replay(ctx, sessionID, "decision", records)
}

func (c *agentlandGatewayClient) ReplayLive(ctx context.Context, sessionID string, records []models.RunTrajectoryRecord) (result *models.ReplayRunResp, err error) {
	return c.replay(ctx, sessionID, "live", records)
}

func (c *agentlandGatewayClient) replay(ctx context.Context, sessionID, mode string, records []models.RunTrajectoryRecord) (result *models.ReplayRunResp, err error) {
	ctx, span := startGatewaySpan(ctx, "gateway.replay_decisions", attribute.String("gateway.session_id", sessionID), attribute.Int("agent.trajectory_records", len(records)))
	defer finishGatewaySpan(span, &err)
	data, err := c.invocationJSON(ctx, http.MethodPost, "/api/agent-sessions/invocations/api/replays/"+url.PathEscape(mode), sessionID, map[string]any{"records": records})
	if err != nil {
		return nil, err
	}
	var report models.ReplayRunResp
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, err
	}
	return &report, nil
}

func (c *agentlandGatewayClient) CancelRun(ctx context.Context, sessionID, agentRunID string) (err error) {
	ctx, span := startGatewaySpan(ctx, "gateway.cancel_run", attribute.String("gateway.session_id", sessionID), attribute.String("agent.run_id", agentRunID))
	defer finishGatewaySpan(span, &err)
	requestPath := "/api/agent-sessions/invocations/api/runs/" + url.PathEscape(strings.TrimSpace(agentRunID)) + "/cancel"
	resp, err := c.do(ctx, http.MethodPost, requestPath, sessionID, bytes.NewReader([]byte("{}")))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if actual := strings.TrimSpace(resp.Header.Get(agentlandSessionHeader)); actual != "" && actual != strings.TrimSpace(sessionID) {
		return &models.GatewayResponseError{StatusCode: http.StatusConflict, Code: "PROJECT_RUNTIME_EXPIRED", Message: "project runtime session was replaced"}
	}
	if resp.StatusCode != http.StatusAccepted && resp.StatusCode != http.StatusOK {
		return decodeGatewayError(resp.StatusCode, body)
	}
	return nil
}

func (c *agentlandGatewayClient) PublishImage(ctx context.Context, sessionID, projectID, publicationID, buildContext, dockerfile string) (result *models.GatewayPublication, err error) {
	ctx, span := startGatewaySpan(ctx, "gateway.publish_image",
		attribute.String("gateway.session_id", sessionID),
		attribute.String("app.project.id", projectID),
		attribute.String("app.publication.id", publicationID),
	)
	defer finishGatewaySpan(span, &err)
	payload, err := json.Marshal(map[string]string{
		"project_id": projectID, "release_id": publicationID, "context": buildContext, "dockerfile": dockerfile,
	})
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/publications", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(agentlandSessionHeader, strings.TrimSpace(sessionID))
	if c.publisherToken == "" {
		return nil, errors.New("agentland-gateway.publisher_token is required")
	}
	request.Header.Set("Authorization", "Bearer "+c.publisherToken)
	response, err := c.streamClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, decodeGatewayError(response.StatusCode, body)
	}
	var publication models.GatewayPublication
	if err = json.Unmarshal(body, &publication); err != nil {
		return nil, err
	}
	if publication.ImageRef == "" || !imageDigestPattern.MatchString(publication.Digest) {
		return nil, errors.New("gateway returned invalid publication metadata")
	}
	return &publication, nil
}

func (c *agentlandGatewayClient) GetFileTree(ctx context.Context, sessionID, path string) (result *models.GatewayFileTree, err error) {
	ctx, span := startGatewaySpan(ctx, "gateway.workspace_tree", attribute.String("gateway.session_id", sessionID))
	defer finishGatewaySpan(span, &err)
	query := url.Values{}
	if strings.TrimSpace(path) != "" {
		query.Set("path", strings.TrimSpace(path))
	}
	requestPath := "/api/agent-sessions/invocations/api/workspace/tree"
	if len(query) != 0 {
		requestPath += "?" + query.Encode()
	}
	data, err := c.invocationJSON(ctx, http.MethodGet, requestPath, sessionID, nil)
	if err != nil {
		return nil, err
	}
	var response struct {
		Root  string            `json:"root"`
		Nodes []models.FileNode `json:"nodes"`
	}
	if err = json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	return &models.GatewayFileTree{Root: response.Root, Nodes: response.Nodes}, nil
}

func (c *agentlandGatewayClient) GetFile(ctx context.Context, sessionID, path string) (result *models.GatewayFile, err error) {
	ctx, span := startGatewaySpan(ctx, "gateway.workspace_read", attribute.String("gateway.session_id", sessionID))
	defer finishGatewaySpan(span, &err)
	query := url.Values{"path": []string{strings.TrimSpace(path)}}
	data, err := c.invocationJSON(ctx, http.MethodGet, "/api/agent-sessions/invocations/api/workspace/file?"+query.Encode(), sessionID, nil)
	if err != nil {
		return nil, err
	}
	var response models.GatewayFile
	var raw struct {
		Path    string `json:"path"`
		Size    int64  `json:"size"`
		Content string `json:"content"`
		SHA     string `json:"sha"`
	}
	if err = json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	response.Path, response.Size, response.Content, response.SHA = raw.Path, raw.Size, raw.Content, raw.SHA
	return &response, nil
}

func (c *agentlandGatewayClient) PutFile(ctx context.Context, sessionID, path, content, sha string) (result *models.GatewayFileWrite, err error) {
	ctx, span := startGatewaySpan(ctx, "gateway.workspace_write", attribute.String("gateway.session_id", sessionID))
	defer finishGatewaySpan(span, &err)
	query := url.Values{"path": []string{strings.TrimSpace(path)}}
	body := map[string]string{"content": content, "sha": sha}
	data, err := c.invocationJSON(ctx, http.MethodPost, "/api/agent-sessions/invocations/api/workspace/file?"+query.Encode(), sessionID, body)
	if err != nil {
		return nil, err
	}
	var raw struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
		SHA  string `json:"sha"`
	}
	if err = json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	return &models.GatewayFileWrite{Path: raw.Path, Size: raw.Size, SHA: raw.SHA}, nil
}

func (c *agentlandGatewayClient) CreatePreview(ctx context.Context, sessionID string, port int) (result *models.GatewayPreviewInfo, err error) {
	ctx, span := startGatewaySpan(ctx, "gateway.create_preview", attribute.String("gateway.session_id", sessionID), attribute.Int("preview.port", port))
	defer finishGatewaySpan(span, &err)
	payload, _ := json.Marshal(map[string]int{"port": port})
	resp, err := c.do(ctx, http.MethodPost, "/api/previews", sessionID, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusNotFound && strings.TrimSpace(sessionID) != "" {
			return nil, &models.GatewayResponseError{StatusCode: http.StatusConflict, Code: "PROJECT_RUNTIME_EXPIRED", Message: "project runtime session was not found"}
		}
		return nil, decodeGatewayError(resp.StatusCode, body)
	}
	var envelope gatewayEnvelope
	if err = json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	if envelope.Code != http.StatusOK {
		if envelope.Code == http.StatusNotFound && strings.TrimSpace(sessionID) != "" {
			return nil, &models.GatewayResponseError{StatusCode: http.StatusConflict, Code: "PROJECT_RUNTIME_EXPIRED", Message: "project runtime session was not found"}
		}
		return nil, &models.GatewayResponseError{StatusCode: envelope.Code, Message: envelope.Msg}
	}
	var raw struct {
		SessionID    string    `json:"session_id"`
		Port         int       `json:"port"`
		PreviewToken string    `json:"preview_token"`
		PreviewURL   string    `json:"preview_url"`
		ExpiresAt    time.Time `json:"expires_at"`
	}
	if err = json.Unmarshal(envelope.Data, &raw); err != nil {
		return nil, err
	}
	if raw.SessionID != "" && strings.TrimSpace(sessionID) != "" && raw.SessionID != strings.TrimSpace(sessionID) {
		return nil, &models.GatewayResponseError{StatusCode: http.StatusConflict, Code: "PROJECT_RUNTIME_EXPIRED", Message: "project runtime session was replaced"}
	}
	template := c.previewPublicURLTemplate
	if template == "" {
		template = configs.DefaultPreviewPublicURLTemplate
	}
	previewURL, err := configs.PreviewPublicURL(template, raw.PreviewToken)
	if err != nil {
		return nil, fmt.Errorf("build preview public URL: %w", err)
	}
	return &models.GatewayPreviewInfo{SessionID: raw.SessionID, Port: raw.Port, PreviewToken: raw.PreviewToken, PreviewURL: previewURL, ExpiresAt: raw.ExpiresAt}, nil
}

func (c *agentlandGatewayClient) invocationJSON(ctx context.Context, method, path, sessionID string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(payload)
	}
	resp, err := c.do(ctx, method, path, sessionID, reader)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if actual := strings.TrimSpace(resp.Header.Get(agentlandSessionHeader)); actual != "" && actual != strings.TrimSpace(sessionID) {
		return nil, &models.GatewayResponseError{StatusCode: http.StatusConflict, Code: "PROJECT_RUNTIME_EXPIRED", Message: "project runtime session was replaced"}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, decodeGatewayError(resp.StatusCode, payload)
	}
	return payload, nil
}

func (c *agentlandGatewayClient) do(ctx context.Context, method, path, sessionID string, body io.Reader) (*http.Response, error) {
	if c.baseURL == "" {
		return nil, errors.New("agentland-gateway.url is required")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if strings.TrimSpace(sessionID) != "" {
		req.Header.Set(agentlandSessionHeader, strings.TrimSpace(sessionID))
	}
	return c.httpClient.Do(req)
}

func parseAgentEvents(reader io.Reader, onEvent func(*models.AgentEvent) error) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	var eventType string
	var dataLines []string
	flush := func() error {
		if len(dataLines) == 0 {
			eventType = ""
			return nil
		}
		var event models.AgentEvent
		if err := json.Unmarshal([]byte(strings.Join(dataLines, "\n")), &event); err != nil {
			return err
		}
		if event.Type == "" {
			event.Type = eventType
		}
		eventType, dataLines = "", dataLines[:0]
		if onEvent != nil {
			return onEvent(&event)
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case line == "":
			if err := flush(); err != nil {
				return err
			}
		case strings.HasPrefix(line, "event:"):
			eventType = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
		case strings.HasPrefix(line, "data:"):
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}

func decodeGatewayError(status int, body []byte) error {
	var raw struct {
		Code    string `json:"code"`
		Error   string `json:"error"`
		Message string `json:"message"`
		Msg     string `json:"msg"`
		SHA     string `json:"sha"`
		Logs    string `json:"logs"`
	}
	_ = json.Unmarshal(body, &raw)
	message := firstValue(raw.Error, raw.Message, raw.Msg, strings.TrimSpace(string(body)), http.StatusText(status))
	return &models.GatewayResponseError{StatusCode: status, Code: raw.Code, Message: message, SHA: raw.SHA, Logs: raw.Logs}
}

func firstValue(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func startGatewaySpan(ctx context.Context, name string, attributes ...attribute.KeyValue) (context.Context, trace.Span) {
	return otel.Tracer("agentland/app-be/gateway").Start(ctx, name, trace.WithAttributes(attributes...))
}

func finishGatewaySpan(span trace.Span, err *error) {
	if err != nil && *err != nil {
		span.RecordError(*err)
		span.SetStatus(codes.Error, (*err).Error())
	}
	span.End()
}
