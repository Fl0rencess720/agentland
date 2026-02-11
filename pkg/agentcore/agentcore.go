package agentcore

import (
	"context"
	"fmt"
	"time"

	"github.com/Fl0rencess720/agentland/api/v1alpha1"
	pb "github.com/Fl0rencess720/agentland/pb/codeinterpreter"
	"github.com/Fl0rencess720/agentland/pkg/agentcore/pkgs/db"
	"github.com/Fl0rencess720/agentland/pkg/common/consts"
	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/rand"
)

var KorokdPort = ":1883"

var codeInterpreterGVR = schema.GroupVersionResource{
	Group:    "agentland.fl0rencess720.app",
	Version:  "v1alpha1",
	Resource: "codeinterpreters",
}

func (s *Server) CreateSandbox(ctx context.Context, req *pb.CreateSandboxRequest) (*pb.CreateSandboxResponse, error) {
	cr := &v1alpha1.CodeInterpreter{
		TypeMeta: metav1.TypeMeta{
			APIVersion: codeInterpreterGVR.GroupVersion().String(),
			Kind:       "CodeInterpreter",
		},
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "session-",
			Namespace:    consts.AgentLandSandboxesNamespace,
		},
		Spec: v1alpha1.CodeInterpreterSpec{
			Template: &v1alpha1.SandboxTemplate{
				Image:   "korokd:latest",
				Command: []string{},
				Args:    []string{},
			},
		},
	}

	if s.warmPoolEnabled {
		mode := v1alpha1.ProvisioningModePoolPreferred
		switch s.warmPoolDefaultMode {
		case string(v1alpha1.ProvisioningModePoolRequired):
			mode = v1alpha1.ProvisioningModePoolRequired
		case string(v1alpha1.ProvisioningModePoolPreferred):
			mode = v1alpha1.ProvisioningModePoolPreferred
		case string(v1alpha1.ProvisioningModeDirect):
			mode = v1alpha1.ProvisioningModeDirect
		}

		cr.Spec.Provisioning = &v1alpha1.CodeInterpreterProvisioningSpec{
			Mode:    mode,
			PoolRef: s.warmPoolPoolRef,
			Profile: s.warmPoolProfile,
		}
	}

	objMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cr)
	if err != nil {
		zap.L().Error("Failed to convert CodeInterpreter to unstructured", zap.Error(err))
		return nil, fmt.Errorf("failed to convert CR to unstructured: %w", err)
	}
	uObj := &unstructured.Unstructured{Object: objMap}

	result, err := s.k8sClient.Resource(codeInterpreterGVR).Namespace(cr.Namespace).Create(ctx, uObj, metav1.CreateOptions{})
	if err != nil {
		zap.L().Error("Failed to create CodeInterpreter in k8s", zap.Error(err))
		return nil, fmt.Errorf("failed to create codeinterpreter in k8s: %w", err)
	}

	sandboxID := result.GetName()
	if sandboxID == "" && cr.GenerateName != "" {
		sandboxID = cr.GenerateName + rand.String(8)
	}

	watcher, err := s.k8sClient.Resource(codeInterpreterGVR).Namespace(cr.Namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + sandboxID,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to watch resource: %w", err)
	}
	defer watcher.Stop()

	timeoutCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	for {
		select {
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return nil, fmt.Errorf("watch channel closed")
			}

			unstructuredObj, ok := event.Object.(*unstructured.Unstructured)
			if !ok {
				continue
			}

			status, found, nestedErr := unstructured.NestedMap(unstructuredObj.Object, "status")
			if nestedErr != nil || !found {
				continue
			}

			phase, _, _ := unstructured.NestedString(status, "phase")
			podIP, _, _ := unstructured.NestedString(status, "podIP")
			if phase == "Running" && podIP != "" {
				if s.sessionStore == nil {
					return nil, fmt.Errorf("session store is nil")
				}

				now := time.Now()
				sessionInfo := &db.SandboxInfo{
					SandboxID:    sandboxID,
					GrpcEndpoint: podIP + KorokdPort,
					CreatedAt:    now,
					ExpiresAt:    now.Add(db.MaxSessionDuration),
				}

				if err := s.sessionStore.CreateSession(ctx, sessionInfo); err != nil {
					return nil, fmt.Errorf("create session failed: %w", err)
				}

				return &pb.CreateSandboxResponse{
					SandboxId:    sandboxID,
					GrpcEndpoint: podIP + KorokdPort,
				}, nil
			}
		case <-timeoutCtx.Done():
			return nil, fmt.Errorf("timeout waiting for sandbox to be ready")
		}
	}
}
