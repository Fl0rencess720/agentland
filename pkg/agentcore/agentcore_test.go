package agentcore

import (
	"context"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/api/v1alpha1"
	pb "github.com/Fl0rencess720/agentland/pb/codeinterpreter"
	"github.com/Fl0rencess720/agentland/pkg/common/consts"
	"github.com/stretchr/testify/suite"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
)

func TestAgentCoreSuite(t *testing.T) {
	suite.Run(t, &AgentCoreSuite{})
}

type AgentCoreSuite struct {
	suite.Suite
}

func (s *AgentCoreSuite) TestCreateSandbox() {
	scheme := runtime.NewScheme()
	s.NoError(v1alpha1.AddToScheme(scheme))
	fakeDynamicClient := fake.NewSimpleDynamicClient(scheme)

	server := &Server{k8sClient: fakeDynamicClient}

	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				list, err := fakeDynamicClient.Resource(codeInterpreterGVR).Namespace(consts.AgentLandSandboxesNamespace).List(context.Background(), metav1.ListOptions{})
				if err != nil || len(list.Items) == 0 {
					continue
				}

				obj := list.Items[0].DeepCopy()
				status := map[string]interface{}{
					"phase": "Running",
					"podIP": "10.42.0.10",
				}
				_ = unstructured.SetNestedMap(obj.Object, status, "status")
				_, _ = fakeDynamicClient.Resource(codeInterpreterGVR).Namespace(consts.AgentLandSandboxesNamespace).Update(context.Background(), obj, metav1.UpdateOptions{})
			}
		}
	}()
	defer close(done)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := server.CreateSandbox(ctx, &pb.CreateSandboxRequest{Language: "go"})

	s.NoError(err)
	s.NotNil(resp)
	s.Contains(resp.SandboxId, "sandbox-")
	s.Equal("10.42.0.10:1883", resp.GrpcEndpoint)

	list, err := fakeDynamicClient.Resource(codeInterpreterGVR).Namespace(consts.AgentLandSandboxesNamespace).List(context.Background(), metav1.ListOptions{})
	s.NoError(err)
	s.Len(list.Items, 1, "Expected one CodeInterpreter to be created")
}
