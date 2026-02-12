package agentcore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/agentcore/pkgs/db"
	"github.com/Fl0rencess720/agentland/pkg/common/consts"
	"go.uber.org/zap"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	sessionGCInterval    = 30 * time.Second
	sessionGCBatchLimit  = int64(100)
	sessionGCOnceTimeout = 20 * time.Second
)

func (s *Server) runSessionGC(ctx context.Context) {
	ticker := time.NewTicker(sessionGCInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.gcOnce(ctx); err != nil {
				zap.L().Error("session GC cycle failed", zap.Error(err))
			}
		}
	}
}

func (s *Server) gcOnce(parent context.Context) error {
	ctx, cancel := context.WithTimeout(parent, sessionGCOnceTimeout)
	defer cancel()

	now := time.Now()
	inactiveDeadline := now.Add(-db.MaxIdleDuration)

	inactiveIDs, err := s.sessionStore.ListInactiveSessions(ctx, inactiveDeadline, sessionGCBatchLimit)
	if err != nil {
		return fmt.Errorf("list inactive sessions failed: %w", err)
	}

	expiredIDs, err := s.sessionStore.ListExpiredSessions(ctx, now, sessionGCBatchLimit)
	if err != nil {
		return fmt.Errorf("list expired sessions failed: %w", err)
	}

	candidates := make(map[string]struct{}, len(inactiveIDs)+len(expiredIDs))
	for _, id := range inactiveIDs {
		if id != "" {
			candidates[id] = struct{}{}
		}
	}
	for _, id := range expiredIDs {
		if id != "" {
			candidates[id] = struct{}{}
		}
	}

	if len(candidates) == 0 {
		return nil
	}

	var errs []error
	for sessionID := range candidates {
		if err := s.deleteSessionCR(ctx, sessionID); err != nil {
			errs = append(errs, fmt.Errorf("delete session CR %s failed: %w", sessionID, err))
			continue
		}

		if err := s.sessionStore.DeleteSession(ctx, sessionID); err != nil {
			errs = append(errs, fmt.Errorf("delete session %s failed: %w", sessionID, err))
			continue
		}

		zap.L().Info("session GC cleaned sandbox", zap.String("sessionID", sessionID))
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

func (s *Server) deleteSessionCR(ctx context.Context, sessionID string) error {
	if err := s.deleteSessionCRByGVR(ctx, codeInterpreterGVR, sessionID); err != nil {
		return err
	}
	if err := s.deleteSessionCRByGVR(ctx, agentSessionGVR, sessionID); err != nil {
		return err
	}
	return nil
}

func (s *Server) deleteSessionCRByGVR(ctx context.Context, gvr schema.GroupVersionResource, sessionID string) error {
	err := s.k8sClient.Resource(gvr).
		Namespace(consts.AgentLandSandboxesNamespace).
		Delete(ctx, sessionID, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
