package agentcore

import (
	"context"
	"net"
	"time"

	pb "github.com/Fl0rencess720/agentland/pb/codeinterpreter"
	"github.com/Fl0rencess720/agentland/pkg/agentcore/config"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
	"k8s.io/client-go/dynamic"
)

type Server struct {
	pb.UnimplementedAgentCoreServiceServer

	grpcServer *grpc.Server
	listener   net.Listener
	k8sClient  dynamic.Interface
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
		grpcServer: server,
		listener:   lis,
		k8sClient:  cfg.K8sClient,
	}
	pb.RegisterAgentCoreServiceServer(server, s)

	return s, nil
}

func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		s.grpcServer.GracefulStop()
	}()

	zap.S().Infof("AgentCore server listening on %s", s.listener.Addr())

	return s.grpcServer.Serve(s.listener)
}
