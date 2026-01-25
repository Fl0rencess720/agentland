package agentcore

import (
	"context"
	"net"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/agentcore/config"
	pb "github.com/Fl0rencess720/agentland/rpc"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

type Server struct {
	pb.UnimplementedAgentCoreServiceServer

	grpcServer *grpc.Server
	listener   net.Listener
}

func NewServer(cfg *config.Config) *Server {
	lis, err := net.Listen("tcp", ":"+cfg.Port)
	if err != nil {
		panic(err)
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
	}
	pb.RegisterAgentCoreServiceServer(server, s)

	return s
}

func (s *Server) Serve(ctx context.Context) error {
	go func() {
		if err := s.grpcServer.Serve(s.listener); err != nil {
			zap.L().Error("Failed to serve", zap.Error(err))
		}
	}()

	go func() {
		<-ctx.Done()
		s.grpcServer.GracefulStop()
	}()

	zap.S().Infof("AgentCore server listening on %s", s.listener.Addr())

	return s.grpcServer.Serve(s.listener)
}
