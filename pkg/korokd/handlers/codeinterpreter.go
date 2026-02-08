package handlers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os/exec"

	"github.com/gin-gonic/gin"
)

type ExecuteCodeRequest struct {
	Language string `json:"language"`
	Code     string `json:"code" binding:"required"`
}

type ExecuteCodeResponse struct {
	ExitCode int32  `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

type CodeInterpreterHandler struct{}

func InitCodeInterpreterApi(group *gin.RouterGroup) {
	h := &CodeInterpreterHandler{}
	group.POST("/execute", h.ExecuteCode)
}

func (h *CodeInterpreterHandler) ExecuteCode(c *gin.Context) {
	var req ExecuteCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	resp, err := h.executeCode(c.Request.Context(), req.Code)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			c.JSON(http.StatusGatewayTimeout, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, resp)
}

func (h *CodeInterpreterHandler) executeCode(ctx context.Context, code string) (*ExecuteCodeResponse, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	cmd := exec.CommandContext(ctx, "python3", "-c", code)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled) {
			return nil, ctx.Err()
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return &ExecuteCodeResponse{
				ExitCode: int32(exitErr.ExitCode()),
				Stdout:   stdout.String(),
				Stderr:   stderr.String(),
			}, nil
		}
		return nil, fmt.Errorf("execute command failed: %w", err)
	}

	return &ExecuteCodeResponse{
		ExitCode: 0,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}, nil
}
