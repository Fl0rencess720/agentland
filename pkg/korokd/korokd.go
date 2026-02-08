package korokd

import (
	"bytes"
	"context"
	"os/exec"

	pb "github.com/Fl0rencess720/agentland/pb/codeinterpreter"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (s *Server) ExecuteCode(ctx context.Context, req *pb.ExecuteCodeRequest) (*pb.ExecuteCodeResponse, error) {
	// TODO: 支持更多语言，目前仅支持 Python
	cmd := exec.CommandContext(ctx, "python3", "-c", req.Code)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return &pb.ExecuteCodeResponse{
				ExitCode: int32(exitErr.ExitCode()),
				Stdout:   stdout.String(),
				Stderr:   stderr.String(),
			}, nil
		}
		if ctx.Err() != nil {
			return nil, status.Error(codes.DeadlineExceeded, ctx.Err().Error())
		}
		return nil, status.Errorf(codes.Internal, "execute command failed: %v", err)
	}

	return &pb.ExecuteCodeResponse{
		ExitCode: 0,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
	}, nil
}
