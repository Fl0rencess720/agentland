package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/gateway/config"
	"github.com/stretchr/testify/suite"
	"go.uber.org/zap"
)

func TestServerSuite(t *testing.T) {
	suite.Run(t, &ServerSuite{})
}

type ServerSuite struct {
	suite.Suite
	testConfig *config.Config
}

func (s *ServerSuite) SetupSuite() {
	zap.ReplaceGlobals(zap.NewNop())

	s.testConfig = &config.Config{
		Port: "18883",
	}
}

func (s *ServerSuite) SetupTest() {
}

// 测试 NewServer 是否正确初始化
func (s *ServerSuite) TestNewServer() {
	srv, err := NewServer(s.testConfig)

	s.NoError(err)
	s.NotNil(srv)

	// 断言端口号是否正确设置到了 http.Server 中
	s.Equal(":"+s.testConfig.Port, srv.httpServer.Addr)
	s.NotNil(srv.httpServer.Handler)
}

// 测试 HTTP 路由处理
func (s *ServerSuite) TestHandlerLogic() {
	srv, _ := NewServer(s.testConfig)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/not-exist-route", nil)

	srv.httpServer.Handler.ServeHTTP(w, req)

	s.Equal(404, w.Code)
}

// 测试 Serve 方法的生命周期
func (s *ServerSuite) TestServe_Lifecycle() {
	srv, _ := NewServer(s.testConfig)

	// 创建一个带取消功能的 Context
	ctx, cancel := context.WithCancel(context.Background())

	errChan := make(chan error)
	go func() {
		// Serve 是阻塞的，直到 ctx 被 cancel
		err := srv.Serve(ctx)
		errChan <- err
	}()

	time.Sleep(1000 * time.Millisecond)

	// 尝试发送一个 HTTP 请求，证明端口真的打开了
	resp, err := http.Get("http://localhost:" + s.testConfig.Port + "/api/code-runner")
	if err == nil {
		defer resp.Body.Close()
		s.T().Logf("Server responded with status: %d", resp.StatusCode)
	} else {
		s.Fail("Failed to connect to test server", err)
	}

	// 触发关闭信号
	cancel()

	// 等待 Serve 方法返回
	select {
	case err := <-errChan:
		if err != http.ErrServerClosed && err != nil {
			s.NoError(err, "Server should shutdown gracefully")
		}
	case <-time.After(2 * time.Second):
		s.Fail("Server did not shutdown within timeout")
	}
}
