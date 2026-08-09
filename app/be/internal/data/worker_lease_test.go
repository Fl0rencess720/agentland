package data

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type memoryWorkerLeaseStore struct {
	mu              sync.Mutex
	owners          map[string]string
	deadlines       map[string]time.Time
	acquireErr      error
	renewErr        error
	recoveryErr     error
	releaseAttempts int
}

func newMemoryWorkerLeaseStore() *memoryWorkerLeaseStore {
	return &memoryWorkerLeaseStore{owners: make(map[string]string), deadlines: make(map[string]time.Time)}
}

func testRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	address := os.Getenv("TEST_REDIS_ADDR")
	if address == "" {
		t.Skip("TEST_REDIS_ADDR is not set")
	}
	client := redis.NewClient(&redis.Options{Addr: address})
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Ping(context.Background()).Err())
	return client
}

func unavailableRedisClient(t *testing.T) *redis.Client {
	t.Helper()
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:0", DialTimeout: 10 * time.Millisecond, ReadTimeout: 10 * time.Millisecond, WriteTimeout: 10 * time.Millisecond, MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func (s *memoryWorkerLeaseStore) Acquire(_ context.Context, kind, id, owner string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.acquireErr != nil {
		return false, s.acquireErr
	}
	key := workerLeaseKey(kind, id)
	if _, exists := s.owners[key]; exists {
		return false, nil
	}
	s.owners[key] = owner
	s.deadlines[key] = time.Now().Add(ttl)
	return true, nil
}

func (s *memoryWorkerLeaseStore) Renew(_ context.Context, kind, id, owner string, ttl time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.renewErr != nil {
		return false, s.renewErr
	}
	key := workerLeaseKey(kind, id)
	if s.owners[key] != owner {
		return false, nil
	}
	s.deadlines[key] = time.Now().Add(ttl)
	return true, nil
}

func (s *memoryWorkerLeaseStore) Release(_ context.Context, kind, id, owner string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseAttempts++
	key := workerLeaseKey(kind, id)
	if s.owners[key] != owner {
		return false, nil
	}
	delete(s.owners, key)
	delete(s.deadlines, key)
	return true, nil
}

func (s *memoryWorkerLeaseStore) AcquireRecovery(_ context.Context, kind string, candidates []leaseCandidate, owner string, ttl time.Duration) (map[string]bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.recoveryErr != nil {
		return nil, s.recoveryErr
	}
	result := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		key := workerLeaseKey(kind, candidate.ID)
		current, exists := s.owners[key]
		if exists && (current != candidate.OwnerID || time.Now().Before(s.deadlines[key])) {
			continue
		}
		s.owners[key] = owner
		s.deadlines[key] = time.Now().Add(ttl)
		result[candidate.ID] = true
	}
	return result, nil
}

func (s *memoryWorkerLeaseStore) Expired(_ context.Context, kind string, before time.Time, limit int64) ([]leaseCandidate, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]leaseCandidate, 0)
	prefix := "app:worker:{" + kind + "}:owner:"
	for key, owner := range s.owners {
		if !strings.HasPrefix(key, prefix) || s.deadlines[key].After(before) {
			continue
		}
		result = append(result, leaseCandidate{ID: strings.TrimPrefix(key, prefix), OwnerID: owner})
		if int64(len(result)) >= limit {
			break
		}
	}
	return result, nil
}

func TestRedisWorkerLeaseLuaOwnershipAndRecoveryRace(t *testing.T) {
	client := testRedisClient(t)
	store := &redisWorkerLeaseStore{client: client}
	ctx := context.Background()
	runID := "lease-lua-" + time.Now().UTC().Format("20060102150405.000000000")
	key := workerLeaseKey(runLeaseKind, runID)
	t.Cleanup(func() {
		_ = client.Del(context.Background(), key).Err()
		_ = client.ZRem(context.Background(), workerDeadlineKey(runLeaseKind), runID).Err()
	})

	acquired, err := store.Acquire(ctx, runLeaseKind, runID, "worker-a", time.Second)
	require.NoError(t, err)
	require.True(t, acquired)

	renewed, err := store.Renew(ctx, runLeaseKind, runID, "worker-b", time.Second)
	require.NoError(t, err)
	require.False(t, renewed)
	renewed, err = store.Renew(ctx, runLeaseKind, runID, "worker-a", time.Second)
	require.NoError(t, err)
	require.True(t, renewed)

	released, err := store.Release(ctx, runLeaseKind, runID, "worker-b")
	require.NoError(t, err)
	require.False(t, released)

	var renewWon bool
	var recovery map[string]bool
	start := make(chan struct{})
	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		<-start
		renewWon, _ = store.Renew(ctx, runLeaseKind, runID, "worker-a", time.Second)
	}()
	go func() {
		defer wait.Done()
		<-start
		recovery, _ = store.AcquireRecovery(ctx, runLeaseKind, []leaseCandidate{{ID: runID, OwnerID: "worker-a"}}, "recovery:test", time.Second)
	}()
	close(start)
	wait.Wait()
	require.True(t, renewWon)
	require.False(t, recovery[runID])

	released, err = store.Release(ctx, runLeaseKind, runID, "worker-a")
	require.NoError(t, err)
	require.True(t, released)
	recovery, err = store.AcquireRecovery(ctx, runLeaseKind, []leaseCandidate{{ID: runID, OwnerID: "worker-a"}}, "recovery:missing", time.Second)
	require.NoError(t, err)
	require.True(t, recovery[runID])
	released, err = store.Release(ctx, runLeaseKind, runID, "recovery:missing")
	require.NoError(t, err)
	require.True(t, released)
	acquired, err = store.Acquire(ctx, runLeaseKind, runID, "worker-a", 30*time.Millisecond)
	require.NoError(t, err)
	require.True(t, acquired)
	require.Eventually(t, func() bool {
		expired, listErr := store.Expired(ctx, runLeaseKind, time.Now(), 10)
		if listErr != nil || len(expired) != 1 || expired[0].ID != runID {
			return false
		}
		recovery, err = store.AcquireRecovery(ctx, runLeaseKind, expired, "recovery:expired", time.Second)
		return err == nil && recovery[runID]
	}, time.Second, 10*time.Millisecond)
	renewed, err = store.Renew(ctx, runLeaseKind, runID, "worker-a", time.Second)
	require.NoError(t, err)
	require.False(t, renewed)
}

func TestRedisWorkerLeaseReturnsRedisFailure(t *testing.T) {
	client := unavailableRedisClient(t)
	store := &redisWorkerLeaseStore{client: client}
	_, err := store.Acquire(context.Background(), runLeaseKind, "run-1", "worker-1", time.Second)
	require.Error(t, err)
}

func TestWorkerLeaseValidation(t *testing.T) {
	store := newMemoryWorkerLeaseStore()
	store.acquireErr = errors.New("redis unavailable")
	_, err := store.Acquire(context.Background(), runLeaseKind, "run-1", "worker-1", time.Second)
	require.ErrorContains(t, err, "redis unavailable")
	require.Equal(t, "app:worker:{publication}:owner:pub-1", workerLeaseKey(publicationLeaseKind, "pub-1"))
}
