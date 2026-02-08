package korokd

import (
	"context"
	"net"
	"time"

	pb "github.com/Fl0rencess720/agentland/pb/codeinterpreter"
	"github.com/Fl0rencess720/agentland/pkg/korokd/config"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/keepalive"
)

type Server struct {
	pb.UnimplementedSandboxServiceServer

	grpcServer *grpc.Server
	listener   net.Listener
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

	grpcServer := grpc.NewServer(
		grpc.KeepaliveEnforcementPolicy(kaep),
		grpc.KeepaliveParams(kasp),
		grpc.StatsHandler(otelgrpc.NewServerHandler()),
	)

	s := &Server{
		grpcServer: grpcServer,
		listener:   lis,
	}
	pb.RegisterSandboxServiceServer(grpcServer, s)

	return s, nil
}

func (s *Server) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		s.grpcServer.GracefulStop()
	}()

	zap.S().Infof("korokd server listening on %s", s.listener.Addr())

	return s.grpcServer.Serve(s.listener)
}
