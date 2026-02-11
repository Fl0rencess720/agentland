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
	"k8s.io/apimachinery/pkg/runtime/schema"
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
	mockStore := &mockSessionStore{}

	server := &Server{
		k8sClient:    fakeDynamicClient,
		sessionStore: mockStore,
	}

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
	s.Contains(resp.SandboxId, "session-")
	s.Equal("10.42.0.10:1883", resp.GrpcEndpoint)

	s.Len(mockStore.created, 1)
	s.Equal(resp.SandboxId, mockStore.created[0].SandboxID)
	s.Equal(resp.GrpcEndpoint, mockStore.created[0].GrpcEndpoint)
	s.False(mockStore.created[0].CreatedAt.IsZero())
	s.False(mockStore.created[0].ExpiresAt.IsZero())
	s.True(mockStore.created[0].ExpiresAt.After(mockStore.created[0].CreatedAt))

	list, err := fakeDynamicClient.Resource(codeInterpreterGVR).Namespace(consts.AgentLandSandboxesNamespace).List(context.Background(), metav1.ListOptions{})
	s.NoError(err)
	s.Len(list.Items, 1, "Expected one CodeInterpreter to be created")

	provisioning, found, err := unstructured.NestedMap(list.Items[0].Object, "spec", "provisioning")
	s.NoError(err)
	s.False(found)
	s.Nil(provisioning)
}

func (s *AgentCoreSuite) TestCreateSandboxWithWarmPoolProvisioning() {
	scheme := runtime.NewScheme()
	s.NoError(v1alpha1.AddToScheme(scheme))
	fakeDynamicClient := fake.NewSimpleDynamicClient(scheme)
	mockStore := &mockSessionStore{}

	server := &Server{
		k8sClient:           fakeDynamicClient,
		sessionStore:        mockStore,
		warmPoolEnabled:     true,
		warmPoolDefaultMode: string(v1alpha1.ProvisioningModePoolRequired),
		warmPoolPoolRef:     "python-pool",
		warmPoolProfile:     "python-default",
	}

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
					"podIP": "10.42.0.11",
				}
				_ = unstructured.SetNestedMap(obj.Object, status, "status")
				_, _ = fakeDynamicClient.Resource(codeInterpreterGVR).Namespace(consts.AgentLandSandboxesNamespace).Update(context.Background(), obj, metav1.UpdateOptions{})
			}
		}
	}()
	defer close(done)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := server.CreateSandbox(ctx, &pb.CreateSandboxRequest{Language: "python"})
	s.NoError(err)
	s.NotNil(resp)

	list, err := fakeDynamicClient.Resource(schema.GroupVersionResource{
		Group:    "agentland.fl0rencess720.app",
		Version:  "v1alpha1",
		Resource: "codeinterpreters",
	}).Namespace(consts.AgentLandSandboxesNamespace).List(context.Background(), metav1.ListOptions{})
	s.NoError(err)
	s.Len(list.Items, 1)

	mode, found, err := unstructured.NestedString(list.Items[0].Object, "spec", "provisioning", "mode")
	s.NoError(err)
	s.True(found)
	s.Equal(string(v1alpha1.ProvisioningModePoolRequired), mode)

	poolRef, found, err := unstructured.NestedString(list.Items[0].Object, "spec", "provisioning", "poolRef")
	s.NoError(err)
	s.True(found)
	s.Equal("python-pool", poolRef)

	profile, found, err := unstructured.NestedString(list.Items[0].Object, "spec", "provisioning", "profile")
	s.NoError(err)
	s.True(found)
	s.Equal("python-default", profile)
}
