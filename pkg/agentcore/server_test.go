package agentcore

import (
	"testing"

	"github.com/Fl0rencess720/agentland/pkg/agentcore/config"
	"github.com/stretchr/testify/suite"
	"go.uber.org/zap"
)

func TestServerSuite(t *testing.T) {
	suite.Run(t, &ServerSuite{})
}

type ServerSuite struct {
	suite.Suite
}

func (s *ServerSuite) SetupSuite() {
	zap.ReplaceGlobals(zap.NewNop())
}

// 测试 NewServer 是否正确初始化
func (s *ServerSuite) TestNewServer() {
	cfg := &config.Config{Port: "0"}
	server, err := NewServer(cfg)

	s.NoError(err)
	s.NotNil(server)
	s.NotNil(server.grpcServer)
	s.NotNil(server.listener)

	s.T().Cleanup(func() {
		_ = server.listener.Close()
	})
}
