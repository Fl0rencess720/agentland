package data

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	commonmodels "github.com/Fl0rencess720/agentland/pkg/common/models"
	"github.com/spf13/viper"
)

const (
	agentlandSessionHeader        = "x-agentland-session"
	agentlandAgentRuntime         = "agentland-agent"
	agentlandAgentRuntimeNS       = "agentland-sandboxes"
	agentlandKorokdPort           = 1883
	agentlandHealthPollInterval   = 2 * time.Second
	agentlandHealthCheckPath      = "/api/agent-sessions/invocations/health?runtime=" + agentlandAgentRuntime + "&runtime_namespace=" + agentlandAgentRuntimeNS
	agentlandStreamChatPathFormat = "/api/agent-sessions/%s/endpoints/by-port/8000/v1/chat/stream"
)

type gatewayEnvelope[T any] struct {
	Msg  string `json:"msg"`
	Code int    `json:"code"`
	Data T      `json:"data"`
}

type agentlandGatewayClient struct {
	baseURL          string
	httpClient       *http.Client
	streamHTTPClient *http.Client
}

func NewAgentlandGatewayClient() biz.AgentlandGateway {
	return &agentlandGatewayClient{
		baseURL: strings.TrimRight(strings.TrimSpace(viper.GetString("agentland-gateway.url")), "/"),
		httpClient: &http.Client{
			Timeout: 65 * time.Second,
		},
		streamHTTPClient: &http.Client{},
	}
}

func (c *agentlandGatewayClient) EnsureSessionReady(ctx context.Context) (*models.AgentSessionInfo, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("agentland-gateway.url is required")
	}
	var sessionID string
	var lastErr error
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+agentlandHealthCheckPath, nil)
		if err != nil {
			return nil, err
		}
		if sessionID != "" {
			req.Header.Set(agentlandSessionHeader, sessionID)
		}
		resp, err := c.httpClient.Do(req)
		if err == nil {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			_ = resp.Body.Close()
			if readErr != nil {
				lastErr = readErr
			} else {
				if headerSessionID := strings.TrimSpace(resp.Header.Get(agentlandSessionHeader)); headerSessionID != "" {
					sessionID = headerSessionID
				}
				if resp.StatusCode == http.StatusOK {
					var payload struct {
						Status string `json:"status"`
					}
					if json.Unmarshal(body, &payload) == nil && strings.EqualFold(payload.Status, "ok") && sessionID != "" {
						return &models.AgentSessionInfo{GatewaySessionID: sessionID}, nil
					}
					lastErr = fmt.Errorf("gateway health check missing session or status: %s", strings.TrimSpace(string(body)))
				} else {
					lastErr = fmt.Errorf("gateway health status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
				}
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return nil, fmt.Errorf("ensure sandbox ready: %w", lastErr)
			}
			return nil, ctx.Err()
		case <-time.After(agentlandHealthPollInterval):
		}
	}
}

func (c *agentlandGatewayClient) StreamChat(ctx context.Context, gatewaySessionID string, reqBody *models.AgentChatStreamReq, onEvent func(*models.AgentSSEEvent) error) error {
	if c.baseURL == "" {
		return fmt.Errorf("agentland-gateway.url is required")
	}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	url := c.baseURL + fmt.Sprintf(agentlandStreamChatPathFormat, strings.TrimSpace(gatewaySessionID))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.streamClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("stream chat status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return parseAgentSSE(resp.Body, onEvent)
}

func (c *agentlandGatewayClient) streamClient() *http.Client {
	if c.streamHTTPClient != nil {
		return c.streamHTTPClient
	}
	if c.httpClient == nil {
		return &http.Client{}
	}
	return &http.Client{
		Transport:     c.httpClient.Transport,
		CheckRedirect: c.httpClient.CheckRedirect,
		Jar:           c.httpClient.Jar,
	}
}

func (c *agentlandGatewayClient) CreatePreview(ctx context.Context, gatewaySessionID string, port int) (*models.GatewayPreviewInfo, error) {
	payload := map[string]any{"port": port}
	data, err := c.doJSON(ctx, http.MethodPost, "/api/previews", map[string]string{agentlandSessionHeader: strings.TrimSpace(gatewaySessionID)}, payload)
	if err != nil {
		return nil, err
	}
	var resp struct {
		SessionID    string    `json:"session_id"`
		Port         int       `json:"port"`
		PreviewToken string    `json:"preview_token"`
		PreviewURL   string    `json:"preview_url"`
		ExpiresAt    time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &models.GatewayPreviewInfo{
		SessionID:    strings.TrimSpace(resp.SessionID),
		Port:         resp.Port,
		PreviewToken: strings.TrimSpace(resp.PreviewToken),
		PreviewURL:   normalizePreviewURL(resp.PreviewURL),
		ExpiresAt:    resp.ExpiresAt,
	}, nil
}

func normalizePreviewURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return trimmed
	}
	if !strings.HasPrefix(parsed.Path, "/p/") {
		return trimmed
	}
	result := parsed.Path
	if parsed.RawQuery != "" {
		result += "?" + parsed.RawQuery
	}
	if parsed.Fragment != "" {
		result += "#" + parsed.Fragment
	}
	return result
}

func (c *agentlandGatewayClient) GetFSTree(ctx context.Context, gatewaySessionID, targetPath string, depth int) (*models.GatewayFSTreeResp, error) {
	query := url.Values{}
	query.Set("path", strings.TrimSpace(targetPath))
	if depth > 0 {
		query.Set("depth", fmt.Sprintf("%d", depth))
	}
	data, err := c.doJSON(ctx, http.MethodGet, "/api/code-runner/fs/tree?"+query.Encode(), map[string]string{agentlandSessionHeader: strings.TrimSpace(gatewaySessionID)}, nil)
	if err != nil {
		return nil, err
	}
	var resp commonmodels.GetFSTreeResp
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	nodes := make([]models.GatewayFSTreeNode, 0, len(resp.Nodes))
	for _, item := range resp.Nodes {
		nodes = append(nodes, models.GatewayFSTreeNode{
			Path:    item.Path,
			Name:    item.Name,
			Type:    item.Type,
			Size:    item.Size,
			ModTime: item.ModTime,
		})
	}
	return &models.GatewayFSTreeResp{Root: resp.Root, Nodes: nodes}, nil
}

func (c *agentlandGatewayClient) GetFSFile(ctx context.Context, gatewaySessionID, targetPath, encoding string) (*models.GatewayFSFileResp, error) {
	query := url.Values{}
	query.Set("path", strings.TrimSpace(targetPath))
	if strings.TrimSpace(encoding) != "" {
		query.Set("encoding", strings.TrimSpace(encoding))
	}
	data, err := c.doJSON(ctx, http.MethodGet, "/api/code-runner/fs/file?"+query.Encode(), map[string]string{agentlandSessionHeader: strings.TrimSpace(gatewaySessionID)}, nil)
	if err != nil {
		return nil, err
	}
	var resp commonmodels.GetFSFileResp
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &models.GatewayFSFileResp{
		Path:     resp.Path,
		Size:     resp.Size,
		Encoding: resp.Encoding,
		Content:  resp.Content,
	}, nil
}

func (c *agentlandGatewayClient) CreateExecContext(ctx context.Context, gatewaySessionID, language, cwd string) (*models.GatewayExecContextInfo, error) {
	payload := commonmodels.CreateContextReq{Language: language, CWD: cwd}
	data, err := c.doJSON(ctx, http.MethodPost, "/api/code-runner/contexts", map[string]string{agentlandSessionHeader: strings.TrimSpace(gatewaySessionID)}, payload)
	if err != nil {
		return nil, err
	}
	var resp commonmodels.CreateContextResp
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &models.GatewayExecContextInfo{
		ContextID: strings.TrimSpace(resp.ContextID),
		Language:  strings.TrimSpace(resp.Language),
		CWD:       strings.TrimSpace(resp.CWD),
		State:     strings.TrimSpace(resp.State),
		CreatedAt: strings.TrimSpace(resp.CreatedAt),
	}, nil
}

func (c *agentlandGatewayClient) ExecuteInContext(ctx context.Context, gatewaySessionID, contextID, code string, timeoutMs int) (*models.GatewayExecutionResult, error) {
	payload := commonmodels.ExecuteContextReq{Code: code, TimeoutMs: timeoutMs}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	resp, err := c.doRaw(ctx, http.MethodPost, "/api/code-runner/contexts/"+url.PathEscape(strings.TrimSpace(contextID))+"/execute", map[string]string{"Content-Type": "application/json", agentlandSessionHeader: strings.TrimSpace(gatewaySessionID)}, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return nil, decodeGatewayError(resp.StatusCode, bodyBytes)
	}
	return parseExecuteSSE(resp.Body)
}

func (c *agentlandGatewayClient) ProbePort(ctx context.Context, gatewaySessionID string, port int, requestPath string) (int, error) {
	if strings.TrimSpace(requestPath) == "" {
		requestPath = "/"
	}
	resp, err := c.doRaw(ctx, http.MethodGet, fmt.Sprintf("/api/agent-sessions/%s/endpoints/by-port/%d%s", url.PathEscape(strings.TrimSpace(gatewaySessionID)), port, ensurePrefixedPath(requestPath)), nil, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 32<<10))
	return resp.StatusCode, nil
}

func (c *agentlandGatewayClient) doJSON(ctx context.Context, method, requestPath string, headers map[string]string, body any) (json.RawMessage, error) {
	var bodyReader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		bodyReader = bytes.NewReader(payload)
		if headers == nil {
			headers = map[string]string{}
		}
		if _, ok := headers["Content-Type"]; !ok {
			headers["Content-Type"] = "application/json"
		}
	}
	resp, err := c.doRaw(ctx, method, requestPath, headers, bodyReader)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, decodeGatewayError(resp.StatusCode, bodyBytes)
	}
	var envelope gatewayEnvelope[json.RawMessage]
	if err := json.Unmarshal(bodyBytes, &envelope); err != nil {
		return nil, err
	}
	if envelope.Code != http.StatusOK {
		return nil, &models.GatewayResponseError{StatusCode: envelope.Code, Message: strings.TrimSpace(envelope.Msg)}
	}
	return envelope.Data, nil
}

func (c *agentlandGatewayClient) doRaw(ctx context.Context, method, requestPath string, headers map[string]string, body io.Reader) (*http.Response, error) {
	if c.baseURL == "" {
		return nil, fmt.Errorf("agentland-gateway.url is required")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+requestPath, body)
	if err != nil {
		return nil, err
	}
	for key, value := range headers {
		if strings.TrimSpace(value) == "" {
			continue
		}
		req.Header.Set(key, value)
	}
	return c.httpClient.Do(req)
}

func (c *agentlandGatewayClient) korokdPath(gatewaySessionID, subPath string, query url.Values) string {
	pathValue := fmt.Sprintf("/api/agent-sessions/%s/endpoints/by-port/%d%s", url.PathEscape(strings.TrimSpace(gatewaySessionID)), agentlandKorokdPort, ensurePrefixedPath(subPath))
	if len(query) == 0 {
		return pathValue
	}
	return pathValue + "?" + query.Encode()
}

func ensurePrefixedPath(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "/"
	}
	if strings.HasPrefix(trimmed, "/") {
		return trimmed
	}
	return "/" + trimmed
}

func decodeGatewayError(statusCode int, body []byte) error {
	var envelope gatewayEnvelope[json.RawMessage]
	if json.Unmarshal(body, &envelope) == nil {
		msg := strings.TrimSpace(envelope.Msg)
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		code := statusCode
		if envelope.Code != 0 {
			code = envelope.Code
		}
		return &models.GatewayResponseError{StatusCode: code, Message: msg}
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(statusCode)
	}
	return &models.GatewayResponseError{StatusCode: statusCode, Message: message}
}

func parseAgentSSE(reader io.Reader, onEvent func(*models.AgentSSEEvent) error) error {
	buffered := bufio.NewReader(reader)
	var eventName string
	dataLines := make([]string, 0, 1)
	flush := func() error {
		if strings.TrimSpace(eventName) == "" && len(dataLines) == 0 {
			return nil
		}
		data := strings.TrimSpace(strings.Join(dataLines, "\n"))
		if strings.TrimSpace(eventName) != "" && onEvent != nil {
			raw := json.RawMessage("null")
			if data != "" {
				raw = json.RawMessage(data)
			}
			if err := onEvent(&models.AgentSSEEvent{Event: strings.TrimSpace(eventName), Data: raw}); err != nil {
				return err
			}
		}
		eventName = ""
		dataLines = dataLines[:0]
		return nil
	}
	for {
		line, err := buffered.ReadString('\n')
		if err != nil && err != io.EOF {
			return err
		}
		if err == io.EOF && len(line) == 0 {
			return flush()
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			if flushErr := flush(); flushErr != nil {
				return flushErr
			}
		} else if strings.HasPrefix(trimmed, "event:") {
			eventName = strings.TrimSpace(strings.TrimPrefix(trimmed, "event:"))
		} else if strings.HasPrefix(trimmed, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
		}
		if err == io.EOF {
			return flush()
		}
	}
}

func parseExecuteSSE(reader io.Reader) (*models.GatewayExecutionResult, error) {
	result := &models.GatewayExecutionResult{}
	buffered := bufio.NewReader(reader)
	dataLines := make([]string, 0, 1)
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		payload := strings.TrimSpace(strings.Join(dataLines, "\n"))
		dataLines = dataLines[:0]
		if payload == "" {
			return nil
		}
		var evt commonmodels.ExecuteStreamEvent
		if err := json.Unmarshal([]byte(payload), &evt); err != nil {
			return err
		}
		if strings.TrimSpace(evt.ContextID) != "" {
			result.ContextID = strings.TrimSpace(evt.ContextID)
		}
		if strings.TrimSpace(evt.ExecutionID) != "" {
			result.ExecutionID = strings.TrimSpace(evt.ExecutionID)
		}
		switch strings.TrimSpace(evt.Type) {
		case "stdout":
			result.Stdout += evt.Text
		case "stderr":
			result.Stderr += evt.Text
		case "count":
			result.ExecutionCount = evt.ExecutionCount
		case "execution_complete":
			result.ExitCode = evt.ExitCode
			result.DurationMs = evt.ExecutionTime
		case "error":
			message := strings.TrimSpace(evt.Error)
			if message == "" {
				message = "code execution failed"
			}
			return &models.GatewayResponseError{StatusCode: http.StatusInternalServerError, Message: message}
		}
		return nil
	}
	for {
		line, err := buffered.ReadString('\n')
		if err != nil && err != io.EOF {
			return nil, err
		}
		if err == io.EOF && len(line) == 0 {
			break
		}
		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			if flushErr := flush(); flushErr != nil {
				return nil, flushErr
			}
		} else if strings.HasPrefix(trimmed, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(trimmed, "data:")))
		}
		if err == io.EOF {
			break
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return result, nil
}
