package agentcore

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Fl0rencess720/agentland/api/v1alpha1"
	pb "github.com/Fl0rencess720/agentland/pb/agentcore"
	"github.com/Fl0rencess720/agentland/pkg/agentcore/pkgs/db"
	"github.com/Fl0rencess720/agentland/pkg/common/consts"
	"github.com/stretchr/testify/suite"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestAgentCoreSuite(t *testing.T) {
	suite.Run(t, &AgentCoreSuite{})
}

type AgentCoreSuite struct {
	suite.Suite
}

func installGenerateNameReactor(client *fake.FakeDynamicClient) {
	seed := 0
	reaction := func(action k8stesting.Action) (bool, runtime.Object, error) {
		createAction, ok := action.(k8stesting.CreateAction)
		if !ok {
			return false, nil, nil
		}
		obj, ok := createAction.GetObject().(*unstructured.Unstructured)
		if !ok {
			return false, nil, nil
		}
		if obj.GetName() == "" && obj.GetGenerateName() != "" {
			seed++
			obj.SetName(fmt.Sprintf("%sut-%d", obj.GetGenerateName(), seed))
		}
		return false, nil, nil
	}

	client.PrependReactor("create", codeInterpreterGVR.Resource, reaction)
	client.PrependReactor("create", agentSessionGVR.Resource, reaction)
}

func upsertSandboxStatus(client *fake.FakeDynamicClient, sandboxName, phase, podIP string) {
	ctx := context.Background()
	resource := client.Resource(sandboxGVR).Namespace(consts.AgentLandSandboxesNamespace)

	obj, err := resource.Get(ctx, sandboxName, metav1.GetOptions{})
	if err != nil {
		if !k8serrors.IsNotFound(err) {
			return
		}
		obj = &unstructured.Unstructured{
			Object: map[string]interface{}{
				"apiVersion": v1alpha1.GroupVersion.String(),
				"kind":       "Sandbox",
				"metadata": map[string]interface{}{
					"name":      sandboxName,
					"namespace": consts.AgentLandSandboxesNamespace,
				},
				"spec": map[string]interface{}{
					"sandboxTemplate": map[string]interface{}{"image": "korokd:latest"},
				},
			},
		}
	}

	status := map[string]interface{}{"phase": phase}
	if podIP != "" {
		status["podIP"] = podIP
	}
	_ = unstructured.SetNestedMap(obj.Object, status, "status")

	if obj.GetResourceVersion() == "" {
		_, _ = resource.Create(ctx, obj, metav1.CreateOptions{})
		return
	}
	_, _ = resource.Update(ctx, obj, metav1.UpdateOptions{})
}

func (s *AgentCoreSuite) TestCreateSandbox() {
	scheme := runtime.NewScheme()
	s.NoError(v1alpha1.AddToScheme(scheme))
	fakeDynamicClient := fake.NewSimpleDynamicClient(scheme)
	installGenerateNameReactor(fakeDynamicClient)
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
				upsertSandboxStatus(fakeDynamicClient, list.Items[0].GetName(), "Running", "10.42.0.10")
			}
		}
	}()
	defer close(done)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := server.CreateCodeInterpreter(ctx, &pb.CreateSandboxRequest{Language: "go"})

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
	installGenerateNameReactor(fakeDynamicClient)
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
				upsertSandboxStatus(fakeDynamicClient, list.Items[0].GetName(), "Running", "10.42.0.11")
			}
		}
	}()
	defer close(done)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := server.CreateCodeInterpreter(ctx, &pb.CreateSandboxRequest{Language: "python"})
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

func (s *AgentCoreSuite) TestCreateAgentSession() {
	scheme := runtime.NewScheme()
	s.NoError(v1alpha1.AddToScheme(scheme))
	fakeDynamicClient := fake.NewSimpleDynamicClient(scheme)
	installGenerateNameReactor(fakeDynamicClient)
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
				list, err := fakeDynamicClient.Resource(agentSessionGVR).Namespace(consts.AgentLandSandboxesNamespace).List(context.Background(), metav1.ListOptions{})
				if err != nil || len(list.Items) == 0 {
					continue
				}
				upsertSandboxStatus(fakeDynamicClient, list.Items[0].GetName(), "Running", "10.42.0.12")
			}
		}
	}()
	defer close(done)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := server.CreateAgentSession(ctx, &pb.CreateAgentSessionRequest{
		RuntimeName:      "default-runtime",
		RuntimeNamespace: consts.AgentLandSandboxesNamespace,
	})
	s.NoError(err)
	s.NotNil(resp)
	s.Contains(resp.SessionId, "session-")
	s.Equal("10.42.0.12:1883", resp.GrpcEndpoint)

	s.Len(mockStore.created, 1)
	s.Equal(resp.SessionId, mockStore.created[0].SandboxID)
	s.Equal(resp.GrpcEndpoint, mockStore.created[0].GrpcEndpoint)

	list, err := fakeDynamicClient.Resource(agentSessionGVR).Namespace(consts.AgentLandSandboxesNamespace).List(context.Background(), metav1.ListOptions{})
	s.NoError(err)
	s.Len(list.Items, 1, "Expected one AgentSession to be created")

	runtimeName, found, err := unstructured.NestedString(list.Items[0].Object, "spec", "runtimeRef", "name")
	s.NoError(err)
	s.True(found)
	s.Equal("default-runtime", runtimeName)
}

func (s *AgentCoreSuite) TestCreateAgentSession_FailedPhaseReturnsDetailedError() {
	scheme := runtime.NewScheme()
	s.NoError(v1alpha1.AddToScheme(scheme))
	fakeDynamicClient := fake.NewSimpleDynamicClient(scheme)
	installGenerateNameReactor(fakeDynamicClient)
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
				list, err := fakeDynamicClient.Resource(agentSessionGVR).Namespace(consts.AgentLandSandboxesNamespace).List(context.Background(), metav1.ListOptions{})
				if err != nil || len(list.Items) == 0 {
					continue
				}

				obj := list.Items[0].DeepCopy()
				status := map[string]interface{}{
					"phase": "Failed",
					"conditions": []interface{}{
						map[string]interface{}{
							"type":    "Accepted",
							"reason":  "RuntimeNotFound",
							"message": "runtimeRef agentland-sandboxes/default-runtime not found",
						},
					},
				}
				_ = unstructured.SetNestedMap(obj.Object, status, "status")
				_, _ = fakeDynamicClient.Resource(agentSessionGVR).Namespace(consts.AgentLandSandboxesNamespace).Update(context.Background(), obj, metav1.UpdateOptions{})
				return
			}
		}
	}()
	defer close(done)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := server.CreateAgentSession(ctx, &pb.CreateAgentSessionRequest{
		RuntimeName:      "default-runtime",
		RuntimeNamespace: consts.AgentLandSandboxesNamespace,
	})
	s.Nil(resp)
	s.Error(err)
	s.True(strings.Contains(err.Error(), "session provisioning failed"))
	s.True(strings.Contains(err.Error(), "RuntimeNotFound"))

	s.Empty(mockStore.created)
}

func (s *AgentCoreSuite) TestGetAndDeleteAgentSession() {
	scheme := runtime.NewScheme()
	s.NoError(v1alpha1.AddToScheme(scheme))

	obj := &v1alpha1.AgentSession{
		TypeMeta: metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "AgentSession"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-to-delete",
			Namespace: consts.AgentLandSandboxesNamespace,
		},
		Spec: v1alpha1.AgentSessionSpec{
			Template: &v1alpha1.SandboxTemplate{Image: "korokd:latest"},
		},
	}

	fakeDynamicClient := fake.NewSimpleDynamicClient(scheme, obj)
	mockStore := &mockSessionStore{
		created: []*db.SandboxInfo{
			{
				SandboxID:    "session-to-delete",
				GrpcEndpoint: "10.42.0.30:1883",
			},
		},
	}

	server := &Server{
		k8sClient:    fakeDynamicClient,
		sessionStore: mockStore,
	}

	getResp, err := server.GetAgentSession(context.Background(), &pb.GetAgentSessionRequest{SessionId: "session-to-delete"})
	s.NoError(err)
	s.Equal("session-to-delete", getResp.SessionId)
	s.Equal("10.42.0.30:1883", getResp.GrpcEndpoint)

	_, err = server.DeleteAgentSession(context.Background(), &pb.DeleteAgentSessionRequest{SessionId: "session-to-delete"})
	s.NoError(err)

	list, err := fakeDynamicClient.Resource(agentSessionGVR).Namespace(consts.AgentLandSandboxesNamespace).List(context.Background(), metav1.ListOptions{})
	s.NoError(err)
	s.Len(list.Items, 0)
	s.Contains(mockStore.deleted, "session-to-delete")
}
