package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/common/utils"
	korokmiddleware "github.com/Fl0rencess720/agentland/pkg/korokd/middleware"
	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/gin-gonic/gin"
)

type Server struct {
	httpServer *http.Server
	agent      *Agent
	memory     *MemoryStore
	mcp        *MCPManager
}

type chatRequest struct {
	ConversationID string `json:"conversation_id"`
	Message        string `json:"message"`
}

func NewServer(ctx context.Context, cfg *Config) (*Server, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, fmt.Errorf("model is required")
	}
	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  cfg.ModelAPIKey,
		BaseURL: cfg.ModelBaseURL,
		Model:   cfg.Model,
	})
	if err != nil {
		return nil, fmt.Errorf("create chat model: %w", err)
	}
	return newServer(ctx, cfg, chatModel)
}

func newServer(ctx context.Context, cfg *Config, chatModel model.ToolCallingChatModel) (*Server, error) {
	skills, err := LoadSkills(cfg.BuiltinSkillsDir, cfg.WorkspaceRoot)
	if err != nil {
		return nil, err
	}
	tools, err := NewLocalTools(cfg.WorkspaceRoot, skills)
	if err != nil {
		return nil, err
	}
	mcpTools, manager, err := LoadMCPTools(ctx, cfg.MCPConfigPaths)
	if err != nil {
		return nil, err
	}
	tools = append(tools, mcpTools...)
	prompt := strings.TrimSpace(cfg.SystemPrompt)
	if prompt == "" {
		prompt = DefaultSystemPrompt
	}
	if index := skills.Index(); index != "" {
		prompt += "\n\n" + index
	}
	memory := NewMemoryStore(cfg.WorkspaceRoot, cfg.ContextTokens)
	agent, err := NewAgent(ctx, chatModel, tools, memory, prompt)
	if err != nil {
		manager.Close()
		return nil, err
	}

	router := gin.New()
	router.Use(gin.Recovery())
	server := &Server{agent: agent, memory: memory, mcp: manager}
	router.GET("/health", server.health)
	api := router.Group("/api")
	if cfg.AuthEnabled {
		verifier, err := utils.NewVerifierFromConfig(utils.VerifierConfig{
			PublicKeyPath: cfg.SandboxJWTPublicPath,
			Issuer:        cfg.SandboxJWTIssuer,
			Audience:      cfg.SandboxJWTAudience,
			ClockSkew:     cfg.SandboxJWTClockSkew,
		})
		if err != nil {
			manager.Close()
			return nil, fmt.Errorf("create sandbox token verifier: %w", err)
		}
		api.Use(korokmiddleware.SandboxAuth(verifier))
	}
	api.POST("/chat", server.chat)
	api.POST("/runs/:run_id/cancel", server.cancel)
	api.GET("/conversations/:conversation_id/messages", server.messages)

	server.httpServer = &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return server, nil
}

func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.httpServer.Shutdown(shutdownCtx)
	}()
	return s.httpServer.ListenAndServe()
}

func (s *Server) Close() error {
	return s.mcp.Close()
}

func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (s *Server) chat(c *gin.Context) {
	var request chatRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	request.ConversationID = strings.TrimSpace(request.ConversationID)
	if request.ConversationID == "" {
		request.ConversationID = "default"
	}
	if !validID(request.ConversationID) || request.Message == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "conversation_id and message are invalid"})
		return
	}

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "streaming is unsupported"})
		return
	}
	emit := func(event Event) error {
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(c.Writer, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, data); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}
	_, err := s.agent.Run(c.Request.Context(), request.ConversationID, request.Message, emit)
	if errors.Is(err, ErrConversationBusy) && !c.Writer.Written() {
		c.JSON(http.StatusConflict, gin.H{"error": ErrConversationBusy.Error()})
	}
}

func (s *Server) cancel(c *gin.Context) {
	if !s.agent.Cancel(c.Param("run_id")) {
		c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
		return
	}
	c.Status(http.StatusAccepted)
}

func (s *Server) messages(c *gin.Context) {
	messages, err := s.memory.Messages(c.Param("conversation_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"messages": messages})
}
