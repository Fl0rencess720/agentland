package handlers

import (
	"context"
	"fmt"
	"sync"
	"time"

	pb "github.com/Fl0rencess720/agentland/pb/codeinterpreter"
	"github.com/Fl0rencess720/agentland/pkg/gateway/pkgs/db"
	"go.uber.org/zap"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

var GCInterval = 2 * time.Minute

type SandboxConn struct {
	Client pb.SandboxServiceClient
	Conn   *grpc.ClientConn
}

type SandboxClientManager struct {
	mu        sync.RWMutex
	sandboxes map[string]*SandboxConn

	sessionStore *db.SessionStore

	// 控制 GC 停止
	stopGC chan struct{}
	wg     sync.WaitGroup
}

func NewSandboxClientManager() *SandboxClientManager {
	return &SandboxClientManager{
		sandboxes:    make(map[string]*SandboxConn),
		sessionStore: db.NewSessionStore(),
	}
}

// Add 添加一个新的 Sandbox 客户端连接
func (scm *SandboxClientManager) Add(sandboxID string, grpcEndpoint string) (pb.SandboxServiceClient, error) {
	ctx := context.Background()

	// 1. 先检查是否已存在
	scm.mu.RLock()
	if wrapper, exists := scm.sandboxes[sandboxID]; exists {
		scm.mu.RUnlock()
		return wrapper.Client, nil
	}
	scm.mu.RUnlock()

	// 2. 创建连接
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
		return nil, fmt.Errorf("create grpc client failed: %w", err)
	}

	client := pb.NewSandboxServiceClient(conn)

	// 3. 加写锁，检查是否在此期间被其他协程添加
	scm.mu.Lock()
	defer scm.mu.Unlock()

	if wrapper, exists := scm.sandboxes[sandboxID]; exists {
		// 被其他协程抢先添加，关闭当前连接，返回已有连接
		conn.Close()
		return wrapper.Client, nil
	}

	// 4. 先写入 Redis，成功后再写入内存
	if err := scm.sessionStore.CreateSession(ctx, &db.SessionInfo{
		SandboxID:    sandboxID,
		GrpcEndpoint: grpcEndpoint,
		CreatedAt:    time.Now(),
		ExpiresAt:    time.Now().Add(db.MaxSessionDuration),
	}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("create session failed: %w", err)
	}

	scm.sandboxes[sandboxID] = &SandboxConn{
		Client: client,
		Conn:   conn,
	}

	return client, nil
}

// Get 获取已有的 Sandbox 客户端连接
func (scm *SandboxClientManager) Get(sandboxID string) (pb.SandboxServiceClient, bool) {
	// 1. 检查是否存在
	scm.mu.RLock()
	wrapper, exists := scm.sandboxes[sandboxID]
	scm.mu.RUnlock()

	if !exists {
		return nil, false
	}

	// 2. 异步更新活跃时间
	// 注意：如果 GC 在此时删除了该 session，UpdateLatestActivity 会返回 session not found，这是可以接受的
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := scm.sessionStore.UpdateLatestActivity(ctx, sandboxID); err != nil {
			zap.L().Warn("Update latest activity failed",
				zap.String("sandboxID", sandboxID),
				zap.Error(err))
		}
	}()

	return wrapper.Client, true
}

// Remove 删除一个 Sandbox 客户端连接
func (scm *SandboxClientManager) Remove(sandboxID string) error {
	scm.mu.Lock()
	wrapper, exists := scm.sandboxes[sandboxID]
	if !exists {
		scm.mu.Unlock()
		return fmt.Errorf("sandbox with ID %s not found", sandboxID)
	}

	// 1. 先从内存删除，避免其他 Get 拿到已关闭的连接
	delete(scm.sandboxes, sandboxID)
	scm.mu.Unlock()

	// 2. 关闭连接
	if err := wrapper.Conn.Close(); err != nil {
		zap.L().Error("Close sandbox connection failed", zap.Error(err))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := scm.sessionStore.DeleteSession(ctx, sandboxID); err != nil {
		return fmt.Errorf("delete session failed: %w", err)
	}

	return nil
}

func (scm *SandboxClientManager) GarbageCollect() {
	ticker := time.NewTicker(GCInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(context.Background(), GCInterval)
			defer cancel()

			now := time.Now()

			// 1. 获取已过期（超过最大会话时间）的 session
			expiredSessions, err := scm.sessionStore.ListExpiredSessions(ctx, now, 100)
			if err != nil {
				zap.L().Error("List expired sessions failed", zap.Error(err))
				return
			}

			// 2. 获取空闲超时（超过最大空闲时间）的 session
			idleDeadline := now.Add(-db.MaxIdleDuration)
			idleSessions, err := scm.sessionStore.ListInactiveSessions(ctx, idleDeadline, 100)
			if err != nil {
				zap.L().Error("List inactive sessions failed", zap.Error(err))
				return
			}

			// 3. 合并去重
			toDelete := make(map[string]struct{})
			for _, id := range expiredSessions {
				toDelete[id] = struct{}{}
			}
			for _, id := range idleSessions {
				toDelete[id] = struct{}{}
			}

			if len(toDelete) == 0 {
				continue
			}

			zap.L().Info("GC starting cleanup",
				zap.Int("expired", len(expiredSessions)),
				zap.Int("idle", len(idleSessions)),
				zap.Int("total_unique", len(toDelete)))

			// 4. 逐个清理
			for sandboxID := range toDelete {
				scm.cleanupSingleSession(ctx, sandboxID)
			}

		case <-scm.stopGC:
			zap.L().Info("GC stopped")
			return
		}
	}
}

// cleanupSingleSession 清理单个 session
func (scm *SandboxClientManager) cleanupSingleSession(ctx context.Context, sandboxID string) {
	// 1. 先确认 Redis 中该 session 是否真的存在
	_, err := scm.sessionStore.GetSession(ctx, sandboxID)
	if err != nil {
		// Session 已不存在，可能已被其他协程清理，尝试清理残留索引即可
		scm.sessionStore.DeleteSession(ctx, sandboxID)
		return
	}

	// 2. 关闭内存中的连接
	scm.mu.Lock()
	wrapper, exists := scm.sandboxes[sandboxID]
	if exists {
		delete(scm.sandboxes, sandboxID)

		go func(conn *grpc.ClientConn) {
			if err := conn.Close(); err != nil {
				zap.L().Error("Close connection in GC failed",
					zap.String("sandboxID", sandboxID),
					zap.Error(err))
			}
		}(wrapper.Conn)
	}
	scm.mu.Unlock()

	// 3. 删除 Redis 数据
	if err := scm.sessionStore.DeleteSession(ctx, sandboxID); err != nil {
		zap.L().Error("Delete session in GC failed",
			zap.String("sandboxID", sandboxID),
			zap.Error(err))
	}
}
