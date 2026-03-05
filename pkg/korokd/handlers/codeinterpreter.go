package handlers

import (
	"strings"
	"sync"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/common/models"
	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/response"
	"github.com/Fl0rencess720/agentland/pkg/korokd/pkgs/utils"
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

// CreateContext 创建代码执行上下文
func (h *CodeInterpreterHandler) CreateContext(c *gin.Context) {
	var req models.CreateContextReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorResponse(c, response.FormError)
		return
	}

	kernelCtx, err := h.contexts.create(req.Language, req.CWD)
	if err != nil {
		response.ErrorResponse(c, response.ServerError)
		return
	}

	response.SuccessResponse(c, models.CreateContextResp{
		ContextID: kernelCtx.ID,
		Language:  kernelCtx.Language,
		CWD:       kernelCtx.CWD,
		State:     "ready",
		CreatedAt: kernelCtx.createdAt.Format(time.RFC3339),
	})
}

// ExecuteInContext 在上下文中执行代码
func (h *CodeInterpreterHandler) ExecuteInContext(c *gin.Context) {
	contextID := c.Param("contextId")

	var req models.ExecuteContextReq
	if err := c.ShouldBindJSON(&req); err != nil {
		response.ErrorResponse(c, response.FormError)
		return
	}

	if strings.TrimSpace(req.Code) == "" {
		response.ErrorResponse(c, response.FormError)
		return
	}
	if req.TimeoutMs != 0 && (req.TimeoutMs < contextMinTimeoutMs || req.TimeoutMs > contextMaxTimeoutMs) {
		response.ErrorResponse(c, response.FormError)
		return
	}

	utils.SetupSSEResponse(c)

	var mu sync.Mutex
	emit := func(evt models.ExecuteStreamEvent) bool {
		if evt.Timestamp == 0 {
			evt.Timestamp = time.Now().UnixMilli()
		}
		if evt.ContextID == "" {
			evt.ContextID = contextID
		}
		return utils.WriteSSE(c, &mu, evt)
	}

	_ = emit(models.ExecuteStreamEvent{Type: "init"})

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

	hookSet := executeStreamHooks{
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
	}

	resp, err := h.contexts.executeWithHooks(
		c.Request.Context(),
		contextID,
		req.Code,
		req.TimeoutMs,
		&hookSet,
	)
	if err != nil {
		_ = emit(models.ExecuteStreamEvent{Type: "error", Error: err.Error()})
		return
	}

	// 执行结束发送 execution_time 与 exit_code，stdout/stderr 由流式帧增量传输
	_ = emit(models.ExecuteStreamEvent{
		Type:          "execution_complete",
		ExecutionTime: resp.DurationMs,
		ExitCode:      resp.ExitCode,
	})

	// 在 handler 返回前给客户端一个很短的窗口读取最后一帧，避免尾帧丢失
	if req.TimeoutMs > 0 {
		sleepMs := 50
		if req.TimeoutMs < 50 {
			sleepMs = req.TimeoutMs
		}
		time.Sleep(time.Duration(sleepMs) * time.Millisecond)
	} else {
		time.Sleep(50 * time.Millisecond)
	}
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
