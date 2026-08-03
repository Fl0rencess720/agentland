package agentd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

type echoParams struct {
	Text string `json:"text"`
}

func echoMCP(_ context.Context, _ *mcp.CallToolRequest, input echoParams) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: input.Text}}}, nil, nil
}

func TestLoadStreamableHTTPMCPTools(t *testing.T) {
	server := newEchoMCPServer()
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "token", r.Header.Get("X-Test-Token"))
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()

	configPath := writeMCPConfig(t, `{"servers":[{"name":"remote","transport":"streamable_http","url":"`+httpServer.URL+`","headers":{"X-Test-Token":"token"}}]}`)
	tools, manager, err := LoadMCPTools(context.Background(), []string{configPath})
	require.NoError(t, err)
	defer manager.Close()
	require.Len(t, tools, 1)
	info, err := tools[0].Info(context.Background())
	require.NoError(t, err)
	require.Equal(t, "mcp__remote__echo", info.Name)
	output, err := tools[0].(tool.InvokableTool).InvokableRun(context.Background(), `{"text":"hello"}`)
	require.NoError(t, err)
	require.Contains(t, output, "hello")
}

func TestLoadStdioMCPTools(t *testing.T) {
	t.Setenv("AL_AGENT_MODEL_API_KEY", "model-secret")
	t.Setenv("OPENAI_API_KEY", "provider-secret")
	configPath := writeMCPConfig(t, `{"servers":[{"name":"local","transport":"stdio","command":"`+os.Args[0]+`","args":["-test.run=TestMCPHelperProcess"],"env":{"GO_WANT_MCP_HELPER":"1"}}]}`)
	tools, manager, err := LoadMCPTools(context.Background(), []string{configPath})
	require.NoError(t, err)
	defer manager.Close()
	require.Len(t, tools, 1)
	output, err := tools[0].(tool.InvokableTool).InvokableRun(context.Background(), `{"text":"stdio"}`)
	require.NoError(t, err)
	require.Contains(t, output, "stdio")
}

func TestMCPHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_MCP_HELPER") != "1" {
		return
	}
	if os.Getenv("AL_AGENT_MODEL_API_KEY") != "" || os.Getenv("OPENAI_API_KEY") != "" {
		os.Exit(3)
	}
	if err := newEchoMCPServer().Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func newEchoMCPServer() *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "echo", Version: "v1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo", Description: "echo text"}, echoMCP)
	return server
}

func writeMCPConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mcp.json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}
