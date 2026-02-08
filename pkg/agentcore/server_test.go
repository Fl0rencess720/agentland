package agentcore

import (
	"strings"
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

func (s *ServerSuite) TestNewServer() {
	cfg := &config.Config{Port: "0"}
	server, err := NewServer(cfg)
	if err != nil {
		if strings.Contains(err.Error(), "operation not permitted") {
			s.T().Skip("listen not permitted in current sandbox")
		}
		s.Require().NoError(err)
	}

	s.NotNil(server)
	s.NotNil(server.grpcServer)
	s.NotNil(server.listener)

	s.T().Cleanup(func() {
		server.grpcServer.Stop()
		_ = server.listener.Close()
	})
}
