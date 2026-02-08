package agentcore

import (
	"context"
	"sort"
	"testing"

	"github.com/Fl0rencess720/agentland/api/v1alpha1"
	"github.com/Fl0rencess720/agentland/pkg/common/consts"
	"github.com/stretchr/testify/suite"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
)

func TestAgentCoreGCSuite(t *testing.T) {
	suite.Run(t, &AgentCoreGCSuite{})
}

type AgentCoreGCSuite struct {
	suite.Suite
}

func (s *AgentCoreGCSuite) TestGCOnceDeletesInactiveAndExpiredSessions() {
	scheme := runtime.NewScheme()
	s.NoError(v1alpha1.AddToScheme(scheme))

	objA := &v1alpha1.CodeInterpreter{
		TypeMeta: metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "CodeInterpreter"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-a",
			Namespace: consts.AgentLandSandboxesNamespace,
		},
		Spec: v1alpha1.CodeInterpreterSpec{
			Template: &v1alpha1.CodeInterpreterSandboxTemplate{Image: "korokd:latest"},
		},
	}
	objB := &v1alpha1.CodeInterpreter{
		TypeMeta: metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "CodeInterpreter"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "session-b",
			Namespace: consts.AgentLandSandboxesNamespace,
		},
		Spec: v1alpha1.CodeInterpreterSpec{
			Template: &v1alpha1.CodeInterpreterSandboxTemplate{Image: "korokd:latest"},
		},
	}
	objKeep := &v1alpha1.CodeInterpreter{
		TypeMeta: metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "CodeInterpreter"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "keep-alive",
			Namespace: consts.AgentLandSandboxesNamespace,
		},
		Spec: v1alpha1.CodeInterpreterSpec{
			Template: &v1alpha1.CodeInterpreterSandboxTemplate{Image: "korokd:latest"},
		},
	}

	fakeDynamicClient := fake.NewSimpleDynamicClient(scheme, objA, objB, objKeep)
	mockStore := &mockSessionStore{
		inactive: []string{"session-a"},
		expired:  []string{"session-b", "session-a"},
	}

	server := &Server{
		k8sClient:    fakeDynamicClient,
		sessionStore: mockStore,
	}

	err := server.gcOnce(context.Background())
	s.NoError(err)

	list, err := fakeDynamicClient.Resource(codeInterpreterGVR).Namespace(consts.AgentLandSandboxesNamespace).List(context.Background(), metav1.ListOptions{})
	s.NoError(err)
	s.Len(list.Items, 1)
	s.Equal("keep-alive", list.Items[0].GetName())

	sort.Strings(mockStore.deleted)
	s.Equal([]string{"session-a", "session-b"}, mockStore.deleted)
}
