package agentcore

import (
	"context"
	"fmt"
	"time"

	"github.com/Fl0rencess720/agentland/api/v1alpha1"
	pb "github.com/Fl0rencess720/agentland/pb/agentcore"
	"github.com/Fl0rencess720/agentland/pkg/agentcore/pkgs/db"
	"github.com/Fl0rencess720/agentland/pkg/common/consts"
	"github.com/Fl0rencess720/agentland/pkg/common/observability"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"google.golang.org/grpc/metadata"
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
	ctx = withIncomingRequestID(ctx)
	tracer := otel.Tracer("agentcore.service")
	ctx, span := tracer.Start(ctx, "agentcore.create_codeinterpreter", trace.WithSpanKind(trace.SpanKindServer))
	defer span.End()

	requestID := observability.RequestIDFromContext(ctx)
	span.SetAttributes(
		attribute.String("request.id", requestID),
		attribute.String("sandbox.language", req.GetLanguage()),
	)

	cr := &v1alpha1.CodeInterpreter{
		TypeMeta: metav1.TypeMeta{
			APIVersion: codeInterpreterGVR.GroupVersion().String(),
			Kind:       "CodeInterpreter",
		},
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "session-",
			Namespace:    consts.AgentLandSandboxesNamespace,
			Annotations:  observability.InjectContextToAnnotations(ctx, nil),
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
		span.RecordError(err)
		span.SetStatus(codes.Error, "convert codeinterpreter failed")
		return nil, fmt.Errorf("failed to convert CR to unstructured: %w", err)
	}
	uObj := &unstructured.Unstructured{Object: objMap}

	result, err := s.k8sClient.Resource(codeInterpreterGVR).Namespace(cr.Namespace).Create(ctx, uObj, metav1.CreateOptions{})
	if err != nil {
		zap.L().Error("Failed to create CodeInterpreter in k8s", zap.Error(err))
		span.RecordError(err)
		span.SetStatus(codes.Error, "create codeinterpreter CR failed")
		return nil, fmt.Errorf("failed to create codeinterpreter in k8s: %w", err)
	}

	sandboxID := result.GetName()
	if sandboxID == "" && cr.GenerateName != "" {
		sandboxID = cr.GenerateName + rand.String(8)
	}
	span.SetAttributes(attribute.String("agentland.session_id", sandboxID))

	grpcEndpoint, err := s.waitSessionReady(ctx, codeInterpreterGVR, cr.Namespace, sandboxID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "wait session ready failed")
		return nil, err
	}

	return &pb.CreateSandboxResponse{
		SandboxId:    sandboxID,
		GrpcEndpoint: grpcEndpoint,
	}, nil
}

func (s *Server) CreateAgentSession(ctx context.Context, req *pb.CreateAgentSessionRequest) (*pb.CreateAgentSessionResponse, error) {
	ctx = withIncomingRequestID(ctx)
	tracer := otel.Tracer("agentcore.service")
	ctx, span := tracer.Start(ctx, "agentcore.create_agentsession", trace.WithSpanKind(trace.SpanKindServer))
	defer span.End()

	span.SetAttributes(attribute.String("request.id", observability.RequestIDFromContext(ctx)))

	if req.GetRuntimeName() == "" {
		span.SetStatus(codes.Error, "runtime_name is required")
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
			Annotations:  observability.InjectContextToAnnotations(ctx, nil),
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
		span.RecordError(err)
		span.SetStatus(codes.Error, "convert agentsession failed")
		return nil, fmt.Errorf("failed to convert CR to unstructured: %w", err)
	}
	uObj := &unstructured.Unstructured{Object: objMap}

	result, err := s.k8sClient.Resource(agentSessionGVR).Namespace(cr.Namespace).Create(ctx, uObj, metav1.CreateOptions{})
	if err != nil {
		zap.L().Error("Failed to create AgentSession in k8s", zap.Error(err))
		span.RecordError(err)
		span.SetStatus(codes.Error, "create agentsession CR failed")
		return nil, fmt.Errorf("failed to create agentsession in k8s: %w", err)
	}

	sessionID := result.GetName()
	if sessionID == "" && cr.GenerateName != "" {
		sessionID = cr.GenerateName + rand.String(8)
	}
	span.SetAttributes(attribute.String("agentland.session_id", sessionID))

	grpcEndpoint, err := s.waitSessionReady(ctx, agentSessionGVR, cr.Namespace, sessionID)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "wait session ready failed")
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
	tracer := otel.Tracer("agentcore.service")
	ctx, span := tracer.Start(ctx, "agentcore.wait_session_ready")
	defer span.End()
	span.SetAttributes(
		attribute.String("request.id", observability.RequestIDFromContext(ctx)),
		attribute.String("agentland.session_id", sessionID),
		attribute.String("k8s.namespace", namespace),
		attribute.String("k8s.resource", gvr.Resource),
	)

	watcher, err := s.k8sClient.Resource(gvr).Namespace(namespace).Watch(ctx, metav1.ListOptions{
		FieldSelector: "metadata.name=" + sessionID,
	})
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "watch resource failed")
		return "", fmt.Errorf("failed to watch resource: %w", err)
	}
	defer watcher.Stop()

	timeoutCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	for {
		select {
		case event, ok := <-watcher.ResultChan():
			if !ok {
				span.SetStatus(codes.Error, "watch channel closed")
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
				span.AddEvent("sandbox.running", trace.WithAttributes(attribute.String("sandbox.pod_ip", podIP)))
				if s.sessionStore == nil {
					span.SetStatus(codes.Error, "session store is nil")
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
					span.RecordError(err)
					span.SetStatus(codes.Error, "create session failed")
					return "", fmt.Errorf("create session failed: %w", err)
				}
				span.SetAttributes(attribute.String("sandbox.pod_ip", podIP))
				return podIP + KorokdPort, nil
			}
			if phase == "Failed" {
				reason, message := extractCondition(status, "Accepted")
				if reason != "" || message != "" {
					span.SetStatus(codes.Error, "session provisioning failed")
					return "", fmt.Errorf("session provisioning failed: reason=%s message=%s", reason, message)
				}
				span.SetStatus(codes.Error, "session provisioning failed")
				return "", fmt.Errorf("session provisioning failed: phase=Failed")
			}
		case <-timeoutCtx.Done():
			span.RecordError(timeoutCtx.Err())
			span.SetStatus(codes.Error, "timeout waiting for sandbox")
			return "", fmt.Errorf("timeout waiting for sandbox to be ready")
		}
	}
}

func withIncomingRequestID(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}
	requestIDs := md.Get(observability.RequestIDHeader)
	if len(requestIDs) == 0 || requestIDs[0] == "" {
		return ctx
	}
	return observability.ContextWithRequestID(ctx, requestIDs[0])
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
