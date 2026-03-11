package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/suite"
)

func TestCodeInterpreterSuite(t *testing.T) {
	suite.Run(t, &CodeInterpreterSuite{})
}

type CodeInterpreterSuite struct {
	suite.Suite
	handler  *CodeInterpreterHandler
	recorder *httptest.ResponseRecorder
	ctx      *gin.Context
}

func (s *CodeInterpreterSuite) SetupSuite() { gin.SetMode(gin.ReleaseMode) }

func (s *CodeInterpreterSuite) SetupTest() {
	s.handler = &CodeInterpreterHandler{}
	s.recorder = httptest.NewRecorder()
	s.ctx, _ = gin.CreateTestContext(s.recorder)
}

func (s *CodeInterpreterSuite) TestCreateContext_InvalidBody() {
	req := httptest.NewRequest("POST", "/contexts", nil)
	s.ctx.Request = req

	s.handler.CreateContext(s.ctx)

	s.Equal(400, s.recorder.Code)
	s.Contains(s.recorder.Body.String(), `"msg":"Form Error"`)
}

func (s *CodeInterpreterSuite) TestExecuteInContext_InvalidBody_ReturnsFormErrorJSON() {
	req := httptest.NewRequest(http.MethodPost, "/contexts/ctx-1/execute", strings.NewReader("{invalid"))
	req.Header.Set("Content-Type", "application/json")
	s.ctx.Request = req
	s.ctx.Params = gin.Params{{Key: "contextId", Value: "ctx-1"}}

	s.handler.ExecuteInContext(s.ctx)

	s.Equal(http.StatusBadRequest, s.recorder.Code)
	s.Contains(s.recorder.Body.String(), `"msg":"Form Error"`)
	s.Contains(s.recorder.Header().Get("Content-Type"), "application/json")
}

func (s *CodeInterpreterSuite) TestExecuteInContext_EmptyCode_ReturnsFormErrorJSON() {
	req := httptest.NewRequest(http.MethodPost, "/contexts/ctx-1/execute", strings.NewReader(`{"code":"   "}`))
	req.Header.Set("Content-Type", "application/json")
	s.ctx.Request = req
	s.ctx.Params = gin.Params{{Key: "contextId", Value: "ctx-1"}}

	s.handler.ExecuteInContext(s.ctx)

	s.Equal(http.StatusBadRequest, s.recorder.Code)
	s.Contains(s.recorder.Body.String(), `"msg":"Form Error"`)
	s.Contains(s.recorder.Header().Get("Content-Type"), "application/json")
}

func (s *CodeInterpreterSuite) TestExecuteInContext_InvalidTimeout_ReturnsFormErrorJSON() {
	req := httptest.NewRequest(http.MethodPost, "/contexts/ctx-1/execute", strings.NewReader(`{"code":"print(1)","timeout_ms":99}`))
	req.Header.Set("Content-Type", "application/json")
	s.ctx.Request = req
	s.ctx.Params = gin.Params{{Key: "contextId", Value: "ctx-1"}}

	s.handler.ExecuteInContext(s.ctx)

	s.Equal(http.StatusBadRequest, s.recorder.Code)
	s.Contains(s.recorder.Body.String(), `"msg":"Form Error"`)
}

func (s *CodeInterpreterSuite) TestExecuteInContext_StreamIncludesExecutionID() {
	s.handler.contexts = &contextManager{
		contexts: map[string]*kernelContext{
			"ctx-1": {
				ID:         "ctx-1",
				Language:   "unsupported",
				executions: make(map[string]*executionRecord),
			},
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/contexts/ctx-1/execute", strings.NewReader(`{"code":"print(1)"}`))
	req.Header.Set("Content-Type", "application/json")
	s.ctx.Request = req
	s.ctx.Params = gin.Params{{Key: "contextId", Value: "ctx-1"}}

	s.handler.ExecuteInContext(s.ctx)

	s.Equal(http.StatusOK, s.recorder.Code)
	s.Contains(s.recorder.Header().Get("Content-Type"), "text/event-stream")
	s.Contains(s.recorder.Body.String(), `"type":"init"`)
	s.Contains(s.recorder.Body.String(), `"execution_id":"`)
}

func (s *CodeInterpreterSuite) TestGetExecutionOutput_Success() {
	record := &executionRecord{
		ID:             "exec-1",
		ContextID:      "ctx-1",
		state:          "running",
		executionCount: 3,
		startedAt:      time.Now().Add(-2 * time.Second).UTC(),
	}
	record.stdout.WriteString("line1\n")
	record.stderr.WriteString("warn\n")

	s.handler.contexts = &contextManager{
		contexts: map[string]*kernelContext{
			"ctx-1": {
				ID:         "ctx-1",
				executions: map[string]*executionRecord{"exec-1": record},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/contexts/ctx-1/executions/exec-1/output", nil)
	s.ctx.Request = req
	s.ctx.Params = gin.Params{{Key: "contextId", Value: "ctx-1"}, {Key: "executionId", Value: "exec-1"}}

	s.handler.GetExecutionOutput(s.ctx)

	s.Equal(http.StatusOK, s.recorder.Code)
	s.Contains(s.recorder.Body.String(), `"execution_id":"exec-1"`)
	s.Contains(s.recorder.Body.String(), `"stdout":"line1\n"`)
	s.Contains(s.recorder.Body.String(), `"stderr":"warn\n"`)
	s.Contains(s.recorder.Body.String(), `"state":"running"`)
}
