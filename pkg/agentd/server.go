package agentd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/common/utils"
	korokhandlers "github.com/Fl0rencess720/agentland/pkg/korokd/handlers"
	korokmiddleware "github.com/Fl0rencess720/agentland/pkg/korokd/middleware"
	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type Server struct {
	httpServer *http.Server
	agent      *Agent
	runs       *RunManager
	memory     *MemoryStore
	mcp        *MCPManager
}

type chatRequest struct {
	RunID             string `json:"run_id,omitempty"`
	ConversationID    string `json:"conversation_id"`
	Message           string `json:"message"`
	CaptureTrajectory bool   `json:"capture_trajectory,omitempty"`
}

const (
	maxChatRequestBodyBytes = 2 << 20
	maxChatMessageBytes     = 256 << 10
)

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
	tools, local, err := newLocalTools(cfg.WorkspaceRoot, skills)
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
	memory := NewMemoryStore(cfg.WorkspaceRoot, cfg.ContextTokens, cfg.SummaryModel)
	agent, err := NewAgent(ctx, chatModel, tools, memory, prompt)
	if err != nil {
		manager.Close()
		return nil, err
	}
	agent.modelName = cfg.Model
	runs, err := NewRunManager(agent, memory.root)
	if err != nil {
		manager.Close()
		return nil, err
	}

	router := gin.New()
	router.Use(gin.Recovery())
	server := &Server{agent: agent, runs: runs, memory: memory, mcp: manager}
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
	api.POST("/runs", server.startRun)
	api.GET("/runs/:run_id", server.getRun)
	api.GET("/runs/:run_id/events", server.runEvents)
	api.POST("/runs/:run_id/cancel", server.cancel)
	api.GET("/runs/:run_id/trajectory", server.trajectory)
	api.POST("/replays/decision", server.replayDecision)
	api.POST("/replays/live", server.replayLive)
	api.GET("/conversations/:conversation_id/messages", server.messages)
	workspace := &workspaceHandler{tools: local}
	api.GET("/workspace/tree", workspace.tree)
	api.GET("/workspace/file", workspace.readFile)
	api.POST("/workspace/file", workspace.writeFile)
	snapshot := &workspaceSnapshotHandler{tools: local}
	api.GET("/workspace/snapshot", snapshot.download)
	api.POST("/workspace/snapshot", snapshot.restore)
	korokhandlers.InitProxyApi(api, korokhandlers.ProxyOptions{})

	server.httpServer = &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           otelhttp.NewHandler(router, "agentd.http"),
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
	s.runs.Close()
	return s.mcp.Close()
}

func (s *Server) health(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (s *Server) chat(c *gin.Context) {
	request, ok := decodeChatRequest(c)
	if !ok {
		return
	}
	state, _, err := s.runs.Start(request.RunID, request.ConversationID, request.Message, request.CaptureTrajectory)
	if err != nil {
		if errors.Is(err, ErrConversationBusy) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	s.streamRun(c, state.RunID, 0)
}

func (s *Server) startRun(c *gin.Context) {
	request, ok := decodeChatRequest(c)
	if !ok {
		return
	}
	if strings.TrimSpace(request.RunID) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "run_id is required"})
		return
	}
	state, created, err := s.runs.Start(request.RunID, request.ConversationID, request.Message, request.CaptureTrajectory)
	if err != nil {
		if errors.Is(err, ErrConversationBusy) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusAccepted
	}
	c.JSON(status, state)
}

func (s *Server) getRun(c *gin.Context) {
	state, ok := s.runs.Get(c.Param("run_id"))
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
		return
	}
	c.JSON(http.StatusOK, state)
}

func (s *Server) runEvents(c *gin.Context) {
	after := strings.TrimSpace(c.Query("after"))
	if after == "" {
		after = strings.TrimSpace(c.GetHeader("Last-Event-ID"))
	}
	sequence := int64(0)
	if after != "" {
		parsed, err := strconv.ParseInt(after, 10, 64)
		if err != nil || parsed < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid event sequence"})
			return
		}
		sequence = parsed
	}
	s.streamRun(c, c.Param("run_id"), sequence)
}

func (s *Server) streamRun(c *gin.Context, runID string, after int64) {
	if _, ok := s.runs.Get(runID); !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "run not found"})
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
	c.Status(http.StatusOK)
	flusher.Flush()
	_ = s.runs.Subscribe(c.Request.Context(), runID, after, func(event Event) error {
		data, err := json.Marshal(event)
		if err != nil {
			return err
		}
		if _, err = fmt.Fprintf(c.Writer, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, data); err != nil {
			return err
		}
		flusher.Flush()
		return nil
	}, func() error {
		_, err := fmt.Fprint(c.Writer, ": ping\n\n")
		flusher.Flush()
		return err
	})
}

func decodeChatRequest(c *gin.Context) (chatRequest, bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxChatRequestBodyBytes)
	var request chatRequest
	decoder := json.NewDecoder(c.Request.Body)
	if err := decoder.Decode(&request); err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body is too large"})
			return chatRequest{}, false
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return chatRequest{}, false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body is too large"})
			return chatRequest{}, false
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return chatRequest{}, false
	}
	request.ConversationID = strings.TrimSpace(request.ConversationID)
	if request.ConversationID == "" {
		request.ConversationID = "default"
	}
	if !validID(request.ConversationID) || strings.TrimSpace(request.Message) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "conversation_id and message are invalid"})
		return chatRequest{}, false
	}
	if len(request.Message) > maxChatMessageBytes {
		c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("message exceeds %d bytes", maxChatMessageBytes)})
		return chatRequest{}, false
	}
	request.RunID = strings.TrimSpace(request.RunID)
	return request, true
}

func (s *Server) trajectory(c *gin.Context) {
	records, err := s.agent.trajectory.Records(c.Param("run_id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(records) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "trajectory not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"records": records})
}

func (s *Server) replayDecision(c *gin.Context) {
	s.replay(c, func(ctx context.Context, records []TrajectoryRecord) (*DecisionReplayReport, error) {
		return replayDecisions(ctx, s.agent.model, s.agent.prompt, records)
	})
}

func (s *Server) replayLive(c *gin.Context) {
	s.replay(c, func(ctx context.Context, records []TrajectoryRecord) (*DecisionReplayReport, error) {
		return replayLive(ctx, s.agent, records)
	})
}

func (s *Server) replay(c *gin.Context, execute func(context.Context, []TrajectoryRecord) (*DecisionReplayReport, error)) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxReplayRequestBytes)
	records, err := parseReplayRecords(c.Request.Body)
	if err != nil {
		var sizeErr *http.MaxBytesError
		if errors.As(err, &sizeErr) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "replay request is too large"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	report, err := execute(c.Request.Context(), records)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, report)
}

func (s *Server) cancel(c *gin.Context) {
	if !s.runs.Cancel(c.Param("run_id")) {
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
