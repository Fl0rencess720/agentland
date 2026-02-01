package agentcore

import (
	"context"
	"testing"

	"github.com/Fl0rencess720/agentland/api/v1alpha1"
	"github.com/Fl0rencess720/agentland/pkg/common/consts"
	pb "github.com/Fl0rencess720/agentland/rpc"
	"github.com/stretchr/testify/suite"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
)

func TestAgentCoreSuite(t *testing.T) {
	suite.Run(t, &AgentCoreSuite{})
}

type AgentCoreSuite struct {
	suite.Suite
}

// 测试 CreateSandbox 是否返回固定的响应
func (s *AgentCoreSuite) TestCreateSandbox() {
	scheme := runtime.NewScheme()
	s.NoError(v1alpha1.AddToScheme(scheme))
	fakeDynamicClient := fake.NewSimpleDynamicClient(scheme)

	server := &Server{k8sClient: fakeDynamicClient}

	resp, err := server.CreateSandbox(context.Background(), &pb.CreateSandboxRequest{
		Language: "go",
	})

	s.NoError(err)
	s.NotNil(resp)
	s.Contains(resp.SandboxId, "sandbox-")
	s.Equal("sandbox:1883", resp.GrpcEndpoint)

	gvr := codeRunnerGVR

	list, err := fakeDynamicClient.Resource(gvr).Namespace(consts.AgentLandSandboxesNamespace).List(context.Background(), metav1.ListOptions{})
	s.NoError(err)
	s.Len(list.Items, 1, "Expected one CodeRunner to be created")
}
