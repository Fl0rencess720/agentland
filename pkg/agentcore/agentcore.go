package agentcore

import (
	"context"
	"fmt"

	"github.com/Fl0rencess720/agentland/api/v1alpha1"
	"github.com/Fl0rencess720/agentland/pkg/common/consts"
	pb "github.com/Fl0rencess720/agentland/rpc"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/rand"
)

var codeRunnerGVR = schema.GroupVersionResource{
	Group:    "agentland.fl0rencess720.app",
	Version:  "v1alpha1",
	Resource: "coderunners",
}

func (s *Server) CreateSandbox(ctx context.Context, req *pb.CreateSandboxRequest) (*pb.CreateSandboxResponse, error) {
	cr := &v1alpha1.CodeRunner{
		TypeMeta: metav1.TypeMeta{
			APIVersion: codeRunnerGVR.GroupVersion().String(),
			Kind:       "CodeRunner",
		},
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "sandbox-",
			Namespace:    consts.AgentLandSandboxesNamespace,
		},
		Spec: v1alpha1.CodeRunnerSpec{
			Template: &v1alpha1.CodeRunnerSandboxTemplate{
				Image:   "TODO",
				Command: []string{},
				Args:    []string{},
			},
		},
	}

	objMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cr)
	if err != nil {
		zap.L().Error("Failed to convert CodeRunner to unstructured", zap.Error(err))
		return nil, fmt.Errorf("failed to convert CR to unstructured: %w", err)
	}
	uObj := &unstructured.Unstructured{Object: objMap}

	result, err := s.k8sClient.Resource(codeRunnerGVR).Namespace(cr.Namespace).Create(ctx, uObj, metav1.CreateOptions{})
	if err != nil {
		zap.L().Error("Failed to create CodeRunner in k8s", zap.Error(err))
		return nil, fmt.Errorf("failed to create coderunner in k8s: %w", err)
	}

	name := result.GetName()
	if name == "" && cr.GenerateName != "" {
		name = cr.GenerateName + rand.String(8)
	}

	return &pb.CreateSandboxResponse{SandboxId: name, GrpcEndpoint: "sandbox:1883"}, nil
}
