package korokd

import (
	"context"
	"os/exec"
	"testing"
	"time"

	pb "github.com/Fl0rencess720/agentland/pb/codeinterpreter"
	"github.com/stretchr/testify/suite"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestKorokdSuite(t *testing.T) {
	suite.Run(t, &KorokdSuite{})
}

type KorokdSuite struct {
	suite.Suite
}

func (s *KorokdSuite) SetupSuite() {
	if _, err := exec.LookPath("python3"); err != nil {
		s.T().Skip("python3 not found in PATH")
	}
}

func (s *KorokdSuite) TestExecuteCode_Success() {
	server := &Server{}

	resp, err := server.ExecuteCode(context.Background(), &pb.ExecuteCodeRequest{
		Code: "print('hello')",
	})

	s.NoError(err)
	s.NotNil(resp)
	s.Equal(int32(0), resp.ExitCode)
	s.Contains(resp.Stdout, "hello")
	s.Equal("", resp.Stderr)
}

func (s *KorokdSuite) TestExecuteCode_ExitError() {
	server := &Server{}

	resp, err := server.ExecuteCode(context.Background(), &pb.ExecuteCodeRequest{
		Code: "import sys; sys.stderr.write('boom'); sys.exit(3)",
	})

	s.NoError(err)
	s.NotNil(resp)
	s.Equal(int32(3), resp.ExitCode)
	s.Contains(resp.Stderr, "boom")
}

func (s *KorokdSuite) TestExecuteCode_Timeout() {
	server := &Server{}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	resp, err := server.ExecuteCode(ctx, &pb.ExecuteCodeRequest{
		Code: "import time; time.sleep(2)",
	})

	s.NoError(err)
	s.NotNil(resp)
	s.NotEqual(int32(0), resp.ExitCode)
}

func (s *KorokdSuite) TestExecuteCode_ContextCanceledBeforeStart() {
	server := &Server{}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	resp, err := server.ExecuteCode(ctx, &pb.ExecuteCodeRequest{
		Code: "print('hello')",
	})

	s.Nil(resp)
	s.Error(err)
	s.Equal(codes.DeadlineExceeded, status.Code(err))
}
