package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/common/models"
	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/response"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type CodeInterpreterHandler struct {
	contexts *contextManager
}

func InitCodeInterpreterApi(group *gin.RouterGroup) {
	manager, err := newContextManager()
	if err != nil {
		zap.L().Error("Init context manager failed", zap.Error(err))
		return
	}

	h := &CodeInterpreterHandler{contexts: manager}

	group.POST("/contexts", h.CreateContext)
	group.POST("/contexts/:contextId/execute", h.ExecuteInContext)
	group.DELETE("/contexts/:contextId", h.DeleteContext)
}

func (h *CodeInterpreterHandler) CreateContext(c *gin.Context) {
	var req models.CreateContextReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorResponse(c, response.FormError)
		return
	}

	ctx, err := h.contexts.create(req.Language, req.CWD)
	if err != nil {
		response.ErrorResponse(c, response.ServerError)
		return
	}

	response.SuccessResponse(c, models.CreateContextResp{
		ContextID: ctx.ID,
		Language:  ctx.Language,
		CWD:       ctx.CWD,
		State:     "ready",
		CreatedAt: ctx.createdAt.Format(time.RFC3339),
	})
}

func (h *CodeInterpreterHandler) ExecuteInContext(c *gin.Context) {
	contextID := c.Param("contextId")
	h.executeInContextSSE(c, contextID)
}

func (h *CodeInterpreterHandler) DeleteContext(c *gin.Context) {
	contextID := c.Param("contextId")
	if contextID == "" {
		response.ErrorResponse(c, response.FormError)
		return
	}

	if err := h.contexts.removeContext(contextID, false); err != nil {
		response.ErrorResponse(c, response.ServerError)
		return
	}

	response.SuccessResponse(c, models.DeleteContextResp{ContextID: contextID})
}

func setupSSEResponse(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeSSE(c *gin.Context, mu *sync.Mutex, evt models.ExecuteStreamEvent) bool {
	if c == nil {
		return false
	}
	b, err := json.Marshal(evt)
	if err != nil {
		return false
	}

	mu.Lock()
	defer mu.Unlock()

	select {
	case <-c.Request.Context().Done():
		return false
	default:
	}

	// Standard SSE frame: "data: <json>\n\n"
	if _, err := c.Writer.Write(append(append([]byte("data: "), b...), '\n', '\n')); err != nil {
		return false
	}
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return true
}

func (h *CodeInterpreterHandler) executeInContextSSE(
	c *gin.Context,
	contextID string,
) {
	setupSSEResponse(c)

	var mu sync.Mutex
	emit := func(evt models.ExecuteStreamEvent) bool {
		if evt.Timestamp == 0 {
			evt.Timestamp = time.Now().UnixMilli()
		}
		if evt.ContextID == "" {
			evt.ContextID = contextID
		}
		return writeSSE(c, &mu, evt)
	}

	if strings.TrimSpace(contextID) == "" {
		_ = emit(models.ExecuteStreamEvent{Type: "error", Error: "context_id is required"})
		return
	}

	var req models.ExecuteContextReq
	if err := c.ShouldBindJSON(&req); err != nil {
		_ = emit(models.ExecuteStreamEvent{Type: "error", Error: "invalid request body"})
		return
	}
	if strings.TrimSpace(req.Code) == "" {
		_ = emit(models.ExecuteStreamEvent{Type: "error", Error: "code is required"})
		return
	}

	_ = emit(models.ExecuteStreamEvent{Type: "init"})

	// Keep-alive for long-running/no-output executions.
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-c.Request.Context().Done():
				return
			case <-ticker.C:
				_ = emit(models.ExecuteStreamEvent{Type: "ping", Text: "pong"})
			}
		}
	}()

	resp, err := h.contexts.executeStreaming(
		c.Request.Context(),
		contextID,
		req.Code,
		req.TimeoutMs,
		executeStreamHooks{
			OnStdout: func(text string) {
				if text == "" {
					return
				}
				_ = emit(models.ExecuteStreamEvent{Type: "stdout", Text: text})
			},
			OnStderr: func(text string) {
				if text == "" {
					return
				}
				_ = emit(models.ExecuteStreamEvent{Type: "stderr", Text: text})
			},
			OnStatus: func(state string) {
				if strings.TrimSpace(state) == "" {
					return
				}
				_ = emit(models.ExecuteStreamEvent{Type: "status", Text: state})
			},
			OnExecutionCount: func(count int64) {
				if count <= 0 {
					return
				}
				_ = emit(models.ExecuteStreamEvent{Type: "count", ExecutionCount: count})
			},
		},
	)
	if err != nil {
		_ = emit(models.ExecuteStreamEvent{Type: "error", Error: err.Error()})
		return
	}

	// Also keep compatibility for clients that only care about the final response.
	_ = emit(models.ExecuteStreamEvent{Type: "complete", Result: resp})

	// Give the client a short window to read the last frame before the handler returns.
	if req.TimeoutMs > 0 {
		// Avoid sleeping too long; this is only for graceful close.
		sleepMs := 50
		if req.TimeoutMs < 50 {
			sleepMs = req.TimeoutMs
		}
		time.Sleep(time.Duration(sleepMs) * time.Millisecond)
	} else {
		time.Sleep(50 * time.Millisecond)
	}
}
