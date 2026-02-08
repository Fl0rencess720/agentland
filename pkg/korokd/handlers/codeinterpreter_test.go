package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os/exec"
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

func (s *CodeInterpreterSuite) SetupSuite() {
	if _, err := exec.LookPath("python3"); err != nil {
		s.T().Skip("python3 not found in PATH")
	}
	gin.SetMode(gin.ReleaseMode)
}

func (s *CodeInterpreterSuite) SetupTest() {
	s.handler = &CodeInterpreterHandler{}
	s.recorder = httptest.NewRecorder()
	s.ctx, _ = gin.CreateTestContext(s.recorder)
}

func (s *CodeInterpreterSuite) TestExecuteCode_BindError() {
	req := httptest.NewRequest("POST", "/execute", bytes.NewBufferString("{bad_json"))
	req.Header.Set("Content-Type", "application/json")
	s.ctx.Request = req

	s.handler.ExecuteCode(s.ctx)

	s.Equal(400, s.recorder.Code)
}

func (s *CodeInterpreterSuite) TestExecuteCode_Success() {
	reqBody := ExecuteCodeRequest{Language: "python", Code: "print('hello')"}
	jsonBytes, _ := json.Marshal(reqBody)
	req := httptest.NewRequest("POST", "/execute", bytes.NewBuffer(jsonBytes))
	req.Header.Set("Content-Type", "application/json")
	s.ctx.Request = req

	s.handler.ExecuteCode(s.ctx)

	s.Equal(200, s.recorder.Code)
	var resp ExecuteCodeResponse
	err := json.Unmarshal(s.recorder.Body.Bytes(), &resp)
	s.NoError(err)
	s.Equal(int32(0), resp.ExitCode)
	s.Contains(resp.Stdout, "hello")
}

func (s *CodeInterpreterSuite) TestExecuteCode_TimeoutFunc() {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	resp, err := s.handler.executeCode(ctx, "import time; time.sleep(2)")
	s.Nil(resp)
	s.Error(err)
	s.True(errors.Is(err, context.DeadlineExceeded))
}

func (s *CodeInterpreterSuite) TestExecuteCode_ContextCanceledBeforeStart() {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	resp, err := s.handler.executeCode(ctx, "print('hello')")
	s.Nil(resp)
	s.Error(err)
	s.True(errors.Is(err, context.Canceled))
}
