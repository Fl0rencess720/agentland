package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"

	officialmcp "github.com/cloudwego/eino-ext/components/tool/mcp/officialmcp"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type MCPManager struct {
	sessions []*mcp.ClientSession
}

type mcpConfigFile struct {
	Servers []mcpServerConfig `json:"servers"`
}

type mcpServerConfig struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Tools     []string          `json:"tools,omitempty"`
}

func LoadMCPTools(ctx context.Context, paths []string) ([]tool.BaseTool, *MCPManager, error) {
	servers := make(map[string]mcpServerConfig)
	order := make([]string, 0)
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		data, err := os.ReadFile(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read MCP config %s: %w", path, err)
		}
		var config mcpConfigFile
		if err := json.Unmarshal([]byte(os.ExpandEnv(string(data))), &config); err != nil {
			return nil, nil, fmt.Errorf("decode MCP config %s: %w", path, err)
		}
		for _, server := range config.Servers {
			if _, exists := servers[server.Name]; !exists {
				order = append(order, server.Name)
			}
			servers[server.Name] = server
		}
	}

	manager := &MCPManager{}
	var result []tool.BaseTool
	for _, name := range order {
		server := servers[name]
		tools, session, err := connectMCP(ctx, server)
		if err != nil {
			manager.Close()
			return nil, nil, err
		}
		manager.sessions = append(manager.sessions, session)
		result = append(result, tools...)
	}
	return result, manager, nil
}

func (m *MCPManager) Close() error {
	if m == nil {
		return nil
	}
	var errs []error
	for _, session := range m.sessions {
		if err := session.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func connectMCP(ctx context.Context, config mcpServerConfig) ([]tool.BaseTool, *mcp.ClientSession, error) {
	config.Name = sanitizeToolName(config.Name)
	if config.Name == "" {
		return nil, nil, fmt.Errorf("MCP server name is required")
	}

	var transport mcp.Transport
	switch config.Transport {
	case "stdio":
		if strings.TrimSpace(config.Command) == "" {
			return nil, nil, fmt.Errorf("MCP server %s requires command", config.Name)
		}
		cmd := exec.CommandContext(ctx, config.Command, config.Args...)
		cmd.Env = os.Environ()
		for key, value := range config.Env {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
		transport = &mcp.CommandTransport{Command: cmd}
	case "streamable_http":
		if strings.TrimSpace(config.URL) == "" {
			return nil, nil, fmt.Errorf("MCP server %s requires url", config.Name)
		}
		client := &http.Client{Transport: headerTransport{base: http.DefaultTransport, headers: config.Headers}}
		transport = &mcp.StreamableClientTransport{Endpoint: config.URL, HTTPClient: client}
	default:
		return nil, nil, fmt.Errorf("MCP server %s has unsupported transport %q", config.Name, config.Transport)
	}

	client := mcp.NewClient(&mcp.Implementation{Name: "agentland-agentd", Version: "v1"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("connect MCP server %s: %w", config.Name, err)
	}
	mcpTools, err := officialmcp.GetTools(ctx, &officialmcp.Config{Cli: session, ToolNameList: config.Tools})
	if err != nil {
		session.Close()
		return nil, nil, fmt.Errorf("load MCP tools from %s: %w", config.Name, err)
	}
	tools := make([]tool.BaseTool, 0, len(mcpTools))
	for _, item := range mcpTools {
		invokable, ok := item.(tool.InvokableTool)
		if !ok {
			continue
		}
		tools = append(tools, &renamedTool{prefix: "mcp__" + config.Name + "__", tool: invokable})
	}
	return tools, session, nil
}

type renamedTool struct {
	prefix string
	tool   tool.InvokableTool
}

func (t *renamedTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	info, err := t.tool.Info(ctx)
	if err != nil {
		return nil, err
	}
	copy := *info
	copy.Name = t.prefix + sanitizeToolName(info.Name)
	return &copy, nil
}

func (t *renamedTool) InvokableRun(ctx context.Context, arguments string, opts ...tool.Option) (string, error) {
	return t.tool.InvokableRun(ctx, arguments, opts...)
}

type headerTransport struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t headerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	for key, value := range t.headers {
		clone.Header.Set(key, value)
	}
	return t.base.RoundTrip(clone)
}

var invalidToolName = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func sanitizeToolName(name string) string {
	return strings.Trim(invalidToolName.ReplaceAllString(strings.TrimSpace(name), "_"), "_")
}
