package handlers

import (
	"fmt"
	"sync"
	"time"

	pb "github.com/Fl0rencess720/agentland/rpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

type SandboxConn struct {
	Client pb.SandboxServiceClient
	Conn   *grpc.ClientConn
}

type SandboxClientManager struct {
	mu        sync.RWMutex
	sandboxes map[string]*SandboxConn
}

func NewSandboxClientManager() *SandboxClientManager {
	return &SandboxClientManager{
		sandboxes: make(map[string]*SandboxConn),
	}
}

func (sm *SandboxClientManager) Get(sandboxID string) (pb.SandboxServiceClient, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	wrapper, exists := sm.sandboxes[sandboxID]
	if !exists {
		return nil, false
	}
	return wrapper.Client, true
}

func (sm *SandboxClientManager) Add(sandboxID string, grpcEndpoint string) (pb.SandboxServiceClient, error) {
	sm.mu.RLock()
	if wrapper, exists := sm.sandboxes[sandboxID]; exists {
		sm.mu.RUnlock()
		return wrapper.Client, nil
	}
	sm.mu.RUnlock()

	kacp := keepalive.ClientParameters{
		Time:                10 * time.Second,
		Timeout:             time.Second,
		PermitWithoutStream: true,
	}

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithKeepaliveParams(kacp),
	}

	conn, err := grpc.NewClient(grpcEndpoint, opts...)
	if err != nil {
		return nil, err
	}

	client := pb.NewSandboxServiceClient(conn)

	sm.mu.Lock()
	defer sm.mu.Unlock()

	if wrapper, exists := sm.sandboxes[sandboxID]; exists {
		conn.Close()
		return wrapper.Client, nil
	}

	sm.sandboxes[sandboxID] = &SandboxConn{
		Client: client,
		Conn:   conn,
	}

	return client, nil
}

func (sm *SandboxClientManager) Remove(sandboxID string) error {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	wrapper, exists := sm.sandboxes[sandboxID]
	if !exists {
		return fmt.Errorf("sandbox with ID %s not found", sandboxID)
	}

	if err := wrapper.Conn.Close(); err != nil {
		fmt.Printf("Warning: failed to close connection for %s: %v\n", sandboxID, err)
	}

	delete(sm.sandboxes, sandboxID)
	return nil
}
