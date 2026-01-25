package response

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/suite"
	"go.uber.org/zap"
)

func TestResponseSuite(t *testing.T) {
	suite.Run(t, &ResponseSuite{})
}

type ResponseSuite struct {
	suite.Suite
	recorder *httptest.ResponseRecorder
	ctx      *gin.Context
}

func (s *ResponseSuite) SetupSuite() {
	gin.SetMode(gin.ReleaseMode)
	zap.ReplaceGlobals(zap.NewNop())
}

func (s *ResponseSuite) SetupTest() {
	s.recorder = httptest.NewRecorder()
	s.ctx, _ = gin.CreateTestContext(s.recorder)
}

// 测试 SuccessResponse 的响应体格式
func (s *ResponseSuite) TestSuccessResponse() {
	data := gin.H{
		"1868": 812,
		"2021": "0626",
	}

	SuccessResponse(s.ctx, data)

	s.Equal(200, s.recorder.Code)

	expectedBody := gin.H{
		"code": 200,
		"msg":  "success",
		"data": data,
	}

	expectedJSON, _ := json.Marshal(expectedBody)
	s.JSONEq(string(expectedJSON), s.recorder.Body.String())
}

// 测试已知错误
func (s *ResponseSuite) TestErrorResponse_ServerError() {
	// 测试 ServerError
	ErrorResponse(s.ctx, ServerError)

	s.Equal(500, s.recorder.Code)

	expectedBody := gin.H{
		"code": 0,
		"msg":  "Server Error",
	}

	expectedJSON, _ := json.Marshal(expectedBody)
	s.JSONEq(string(expectedJSON), s.recorder.Body.String())
}

// 测试未定义的错误
func (s *ResponseSuite) TestErrorResponse_Unknown() {
	var unknownCode ErrorCode = 999
	ErrorResponse(s.ctx, unknownCode)

	s.Equal(403, s.recorder.Code)

	expectedBody := gin.H{
		"code": 999,
		"msg":  "Unknown Error",
	}

	expectedJSON, _ := json.Marshal(expectedBody)
	s.JSONEq(string(expectedJSON), s.recorder.Body.String())
}
