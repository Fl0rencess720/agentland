package korokd

import (
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/common/testutil"
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
	_, publicPath, err := testutil.WriteTestRSAKeys(s.T().TempDir())
	s.Require().NoError(err)
	cfg := &config.Config{
		Port:                 "1883",
		SandboxJWTPublicPath: publicPath,
		SandboxJWTIssuer:     "agentland-gateway",
		SandboxJWTAudience:   "sandbox",
		SandboxJWTClockSkew:  30 * time.Second,
	}
	server, err := NewServer(cfg)

	s.NoError(err)
	s.NotNil(server)
	s.NotNil(server.httpServer)
	s.Equal(":1883", server.httpServer.Addr)
	s.NotNil(server.httpServer.Handler)
}
