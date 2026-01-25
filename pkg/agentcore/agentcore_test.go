package agentcore

import (
	"context"
	"testing"

	pb "github.com/Fl0rencess720/agentland/rpc"
	"github.com/stretchr/testify/suite"
)

func TestAgentCoreSuite(t *testing.T) {
	suite.Run(t, &AgentCoreSuite{})
}

type AgentCoreSuite struct {
	suite.Suite
}

// 测试 CreateSandbox 是否返回固定的响应
func (s *AgentCoreSuite) TestCreateSandbox() {
	server := &Server{}

	resp, err := server.CreateSandbox(context.Background(), &pb.CreateSandboxRequest{
		Language: "go",
	})

	s.NoError(err)
	s.NotNil(resp)
	s.Equal("1883", resp.SandboxId)
	s.Equal("sandbox:1883", resp.GrpcEndpoint)
}
