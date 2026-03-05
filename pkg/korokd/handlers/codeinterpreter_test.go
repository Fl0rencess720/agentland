package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
