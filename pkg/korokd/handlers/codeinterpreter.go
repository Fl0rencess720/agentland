package handlers

import (
	"time"

	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/response"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

type CodeInterpreterHandler struct {
	contexts *contextManager
}

type CreateContextReq struct {
	Language string `json:"language"`
	CWD      string `json:"cwd"`
}

type CreateContextResp struct {
	ContextID string `json:"context_id"`
	Language  string `json:"language"`
	CWD       string `json:"cwd"`
	State     string `json:"state"`
	CreatedAt string `json:"created_at"`
}

type ExecuteContextReq struct {
	Code      string `json:"code"`
	TimeoutMs int    `json:"timeout_ms,omitempty"`
}

type ExecuteContextResp struct {
	ContextID      string `json:"context_id"`
	ExecutionCount int64  `json:"execution_count"`
	ExitCode       int32  `json:"exit_code"`
	Stdout         string `json:"stdout"`
	Stderr         string `json:"stderr"`
	DurationMs     int64  `json:"duration_ms"`
}

type DeleteContextResp struct {
	ContextID string `json:"context_id"`
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
	var req CreateContextReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorResponse(c, response.FormError)
		return
	}

	ctx, err := h.contexts.create(req.Language, req.CWD)
	if err != nil {
		response.ErrorResponse(c, response.ServerError)
		return
	}

	response.SuccessResponse(c, CreateContextResp{
		ContextID: ctx.ID,
		Language:  ctx.Language,
		CWD:       ctx.CWD,
		State:     "ready",
		CreatedAt: ctx.createdAt.Format(time.RFC3339),
	})
}

func (h *CodeInterpreterHandler) ExecuteInContext(c *gin.Context) {
	contextID := c.Param("contextId")
	if contextID == "" {
		response.ErrorResponse(c, response.FormError)
		return
	}

	var req ExecuteContextReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorResponse(c, response.FormError)
		return
	}

	resp, err := h.contexts.execute(c.Request.Context(), contextID, req.Code, req.TimeoutMs)
	if err != nil {
		response.ErrorResponse(c, response.ServerError)
		return
	}

	response.SuccessResponse(c, resp)
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

	response.SuccessResponse(c, DeleteContextResp{ContextID: contextID})
}
