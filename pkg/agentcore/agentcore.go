package agentcore

import (
	"context"
	"fmt"
	"time"

	"github.com/Fl0rencess720/agentland/api/v1alpha1"
	pb "github.com/Fl0rencess720/agentland/pb/agentcore"
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

var agentSessionGVR = schema.GroupVersionResource{
	Group:    "agentland.fl0rencess720.app",
	Version:  "v1alpha1",
	Resource: "agentsessions",
}

func (s *Server) CreateCodeInterpreter(ctx context.Context, req *pb.CreateSandboxRequest) (*pb.CreateSandboxResponse, error) {
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

		cr.Spec.Provisioning = &v1alpha1.ProvisioningSpec{
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

	grpcEndpoint, err := s.waitSessionReady(ctx, codeInterpreterGVR, cr.Namespace, sandboxID)
	if err != nil {
		return nil, err
	}

	return &pb.CreateSandboxResponse{
		SandboxId:    sandboxID,
		GrpcEndpoint: grpcEndpoint,
	}, nil
}

func (s *Server) CreateAgentSession(ctx context.Context, req *pb.CreateAgentSessionRequest) (*pb.CreateAgentSessionResponse, error) {
	if req.GetRuntimeName() == "" {
		return nil, fmt.Errorf("runtime_name is required")
	}

	runtimeNamespace := req.GetRuntimeNamespace()
	if runtimeNamespace == "" {
		runtimeNamespace = consts.AgentLandSandboxesNamespace
	}

	cr := &v1alpha1.AgentSession{
		TypeMeta: metav1.TypeMeta{
			APIVersion: agentSessionGVR.GroupVersion().String(),
			Kind:       "AgentSession",
		},
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "session-",
			Namespace:    consts.AgentLandSandboxesNamespace,
		},
		Spec: v1alpha1.AgentSessionSpec{
			RuntimeRef: &v1alpha1.RuntimeReference{
				Name:      req.GetRuntimeName(),
				Namespace: runtimeNamespace,
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

		cr.Spec.Provisioning = &v1alpha1.ProvisioningSpec{
			Mode:    mode,
			PoolRef: s.warmPoolPoolRef,
			Profile: s.warmPoolProfile,
		}
	}

	objMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(cr)
	if err != nil {
		zap.L().Error("Failed to convert AgentSession to unstructured", zap.Error(err))
		return nil, fmt.Errorf("failed to convert CR to unstructured: %w", err)
	}
	uObj := &unstructured.Unstructured{Object: objMap}

	result, err := s.k8sClient.Resource(agentSessionGVR).Namespace(cr.Namespace).Create(ctx, uObj, metav1.CreateOptions{})
	if err != nil {
		zap.L().Error("Failed to create AgentSession in k8s", zap.Error(err))
		return nil, fmt.Errorf("failed to create agentsession in k8s: %w", err)
	}

	sessionID := result.GetName()
	if sessionID == "" && cr.GenerateName != "" {
		sessionID = cr.GenerateName + rand.String(8)
	}

	grpcEndpoint, err := s.waitSessionReady(ctx, agentSessionGVR, cr.Namespace, sessionID)
	if err != nil {
		return nil, err
	}

	return &pb.CreateAgentSessionResponse{
		SessionId:    sessionID,
		GrpcEndpoint: grpcEndpoint,
	}, nil
}

func (s *Server) GetAgentSession(ctx context.Context, req *pb.GetAgentSessionRequest) (*pb.GetAgentSessionResponse, error) {
	if req.GetSessionId() == "" {
		return nil, fmt.Errorf("session_id is required")
	}
	if s.sessionStore == nil {
		return nil, fmt.Errorf("session store is nil")
	}

	sessionInfo, err := s.sessionStore.GetSession(ctx, req.GetSessionId())
	if err != nil {
		return nil, fmt.Errorf("get session failed: %w", err)
	}

	return &pb.GetAgentSessionResponse{
		SessionId:    sessionInfo.SandboxID,
		GrpcEndpoint: sessionInfo.GrpcEndpoint,
	}, nil
}

func (s *Server) DeleteAgentSession(ctx context.Context, req *pb.DeleteAgentSessionRequest) (*pb.DeleteAgentSessionResponse, error) {
	if req.GetSessionId() == "" {
		return nil, fmt.Errorf("session_id is required")
	}

	if err := s.deleteSessionCR(ctx, req.GetSessionId()); err != nil {
		return nil, fmt.Errorf("delete session CR failed: %w", err)
	}
	if s.sessionStore != nil {
		if err := s.sessionStore.DeleteSession(ctx, req.GetSessionId()); err != nil {
			return nil, fmt.Errorf("delete session from store failed: %w", err)
		}
	}

	return &pb.DeleteAgentSessionResponse{}, nil
}

func (s *Server) waitSessionReady(ctx context.Context, gvr schema.GroupVersionResource, namespace, sessionID string) (string, error) {
	watcher, err := s.k8sClient.Resource(gvr).Namespace(namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + sessionID,
	})
	if err != nil {
		return "", fmt.Errorf("failed to watch resource: %w", err)
	}
	defer watcher.Stop()

	timeoutCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	for {
		select {
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return "", fmt.Errorf("watch channel closed")
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
					return "", fmt.Errorf("session store is nil")
				}

				now := time.Now()
				sessionInfo := &db.SandboxInfo{
					SandboxID:    sessionID,
					GrpcEndpoint: podIP + KorokdPort,
					CreatedAt:    now,
					ExpiresAt:    now.Add(db.MaxSessionDuration),
				}

				if err := s.sessionStore.CreateSession(ctx, sessionInfo); err != nil {
					return "", fmt.Errorf("create session failed: %w", err)
				}
				return podIP + KorokdPort, nil
			}
			if phase == "Failed" {
				reason, message := extractCondition(status, "Accepted")
				if reason != "" || message != "" {
					return "", fmt.Errorf("session provisioning failed: reason=%s message=%s", reason, message)
				}
				return "", fmt.Errorf("session provisioning failed: phase=Failed")
			}
		case <-timeoutCtx.Done():
			return "", fmt.Errorf("timeout waiting for sandbox to be ready")
		}
	}
}

func extractCondition(status map[string]interface{}, conditionType string) (string, string) {
	conditionsRaw, ok := status["conditions"]
	if !ok {
		return "", ""
	}

	conditions, ok := conditionsRaw.([]interface{})
	if !ok {
		return "", ""
	}

	for _, item := range conditions {
		conditionMap, ok := item.(map[string]interface{})
		if !ok {
			continue
		}

		conditionName, _ := conditionMap["type"].(string)
		if conditionType != "" && conditionName != conditionType {
			continue
		}

		reason, _ := conditionMap["reason"].(string)
		message, _ := conditionMap["message"].(string)
		return reason, message
	}

	return "", ""
}
