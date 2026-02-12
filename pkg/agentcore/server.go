package agentcore

import (
	"context"
	"net"
	"time"

	pb "github.com/Fl0rencess720/agentland/pb/agentcore"
	"github.com/Fl0rencess720/agentland/pkg/agentcore/config"
	"github.com/Fl0rencess720/agentland/pkg/agentcore/pkgs/db"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"k8s.io/client-go/dynamic"
)

type sessionStore interface {
	CreateSession(ctx context.Context, info *db.SandboxInfo) error
	GetSession(ctx context.Context, sandboxID string) (*db.SandboxInfo, error)
	DeleteSession(ctx context.Context, sandboxID string) error
	ListInactiveSessions(ctx context.Context, before time.Time, limit int64) ([]string, error)
	ListExpiredSessions(ctx context.Context, now time.Time, limit int64) ([]string, error)
}

type Server struct {
	pb.UnimplementedAgentCoreServiceServer

	grpcServer *grpc.Server
	listener   net.Listener
	k8sClient  dynamic.Interface

	sessionStore sessionStore

	warmPoolEnabled     bool
	warmPoolDefaultMode string
	warmPoolPoolRef     string
	warmPoolProfile     string
}

func NewServer(cfg *config.Config) (*Server, error) {
	lis, err := net.Listen("tcp", ":"+cfg.Port)
	if err != nil {
		return nil, err
	}

	kaep := keepalive.EnforcementPolicy{
		MinTime:             5 * time.Second,
		PermitWithoutStream: false,
	}

	kasp := keepalive.ServerParameters{
		Time:    15 * time.Second,
		Timeout: 5 * time.Second,
	}

	server := grpc.NewServer(
		grpc.KeepaliveEnforcementPolicy(kaep),
		grpc.KeepaliveParams(kasp),
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
	)

	s := &Server{
		grpcServer:   server,
		listener:     lis,
		k8sClient:    cfg.K8sClient,
		sessionStore: db.NewSessionStore(),

		warmPoolEnabled:     cfg.WarmPoolEnabled,
		warmPoolDefaultMode: cfg.WarmPoolDefaultMode,
		warmPoolPoolRef:     cfg.WarmPoolPoolRef,
		warmPoolProfile:     cfg.WarmPoolProfile,
	}

	pb.RegisterAgentCoreServiceServer(server, s)

	return s, nil
}

func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		s.grpcServer.GracefulStop()
	}()

	go s.runSessionGC(ctx)

	zap.S().Infof("AgentCore server listening on %s", s.listener.Addr())

	return s.grpcServer.Serve(s.listener)
}
