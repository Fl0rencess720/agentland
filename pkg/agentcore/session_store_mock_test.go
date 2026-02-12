package agentcore

import (
	"context"
	"errors"
	"time"

	"github.com/Fl0rencess720/agentland/pkg/agentcore/pkgs/db"
)

type mockSessionStore struct {
	createErr       error
	listInactiveErr error
	listExpiredErr  error
	deleteErr       map[string]error

	inactive []string
	expired  []string
	created  []*db.SandboxInfo
	deleted  []string
}

func (m *mockSessionStore) CreateSession(ctx context.Context, info *db.SandboxInfo) error {
	if m.createErr != nil {
		return m.createErr
	}
	if info != nil {
		cloned := *info
		m.created = append(m.created, &cloned)
	}
	return nil
}

func (m *mockSessionStore) GetSession(ctx context.Context, sandboxID string) (*db.SandboxInfo, error) {
	for _, item := range m.created {
		if item != nil && item.SandboxID == sandboxID {
			cloned := *item
			return &cloned, nil
		}
	}
	return nil, errors.New("session not found")
}

func (m *mockSessionStore) DeleteSession(ctx context.Context, sandboxID string) error {
	if m.deleteErr != nil {
		if err, ok := m.deleteErr[sandboxID]; ok {
			return err
		}
	}
	m.deleted = append(m.deleted, sandboxID)
	return nil
}

func (m *mockSessionStore) ListInactiveSessions(ctx context.Context, before time.Time, limit int64) ([]string, error) {
	if m.listInactiveErr != nil {
		return nil, m.listInactiveErr
	}
	result := make([]string, len(m.inactive))
	copy(result, m.inactive)
	return result, nil
}

func (m *mockSessionStore) ListExpiredSessions(ctx context.Context, now time.Time, limit int64) ([]string, error) {
	if m.listExpiredErr != nil {
		return nil, m.listExpiredErr
	}
	result := make([]string, len(m.expired))
	copy(result, m.expired)
	return result, nil
}
