package mcp

import (
	"net/http"

	"github.com/Fl0rencess720/agentland/pkg/gateway/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func NewMCPHandler(cfg *config.Config) http.Handler {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "agentland--mcp",
		Version: "v0.1.0",
	}, nil)
	registerCodeInterpreterTools(server, cfg)

	handler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{
			Stateless:    true,
			JSONResponse: true,
		},
	)

	return handler
}
