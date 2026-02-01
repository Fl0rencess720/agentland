package korokd

import (
	"testing"

	"github.com/Fl0rencess720/agentland/pkg/korokd/config"
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

func (s *ServerSuite) TestNewServer() {
	cfg := &config.Config{Port: "0"}
	server, err := NewServer(cfg)

	s.NoError(err)
	s.NotNil(server)
	s.NotNil(server.listener)
	s.NotNil(server.grpcServer)

	s.T().Cleanup(func() {
		server.grpcServer.Stop()
		_ = server.listener.Close()
	})
}
