package agentcore

import (
	"context"

	pb "github.com/Fl0rencess720/agentland/rpc"
)

func (s *Server) CreateSandbox(ctx context.Context, req *pb.CreateSandboxRequest) (*pb.CreateSandboxResponse, error) {
	return &pb.CreateSandboxResponse{SandboxId: "1883", GrpcEndpoint: "sandbox:1883"}, nil
}
