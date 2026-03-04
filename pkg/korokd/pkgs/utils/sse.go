package utils

import (
	"encoding/json"
	"net/http"
	"sync"

	"github.com/Fl0rencess720/agentland/pkg/common/models"
	"github.com/gin-gonic/gin"
)

func SetupSSEResponse(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	if flusher, ok := c.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

func WriteSSE(c *gin.Context, mu *sync.Mutex, evt models.ExecuteStreamEvent) bool {
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
