package handlers

import (
	"net/http/httptest"
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
