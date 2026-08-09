package data

import (
	"context"
	"fmt"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/pkgs/token"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

const (
	runLeaseKind         = "run"
	publicationLeaseKind = "publication"

	acquireLeaseScript = `
if redis.call("exists", KEYS[1]) == 1 then
  return 0
end
local now = redis.call("time")
local deadline = now[1] * 1000 + math.floor(now[2] / 1000) + tonumber(ARGV[2])
redis.call("set", KEYS[1], ARGV[1])
redis.call("zadd", KEYS[2], deadline, ARGV[3])
return 1`
	renewLeaseScript = `
if redis.call("get", KEYS[1]) == ARGV[1] then
	local now = redis.call("time")
	local deadline = now[1] * 1000 + math.floor(now[2] / 1000) + tonumber(ARGV[2])
	redis.call("zadd", KEYS[2], deadline, ARGV[3])
	return 1
end
return 0`
	releaseLeaseScript = `
if redis.call("get", KEYS[1]) == ARGV[1] then
	redis.call("del", KEYS[1])
	redis.call("zrem", KEYS[2], ARGV[2])
	return 1
end
return 0`
	takeoverLeaseScript = `
local deadline = redis.call("zscore", KEYS[2], ARGV[1])
local current = redis.call("get", KEYS[1])
local now = redis.call("time")
local now_ms = now[1] * 1000 + math.floor(now[2] / 1000)
if not current or (current == ARGV[2] and deadline and tonumber(deadline) <= now_ms) then
	redis.call("set", KEYS[1], ARGV[3])
	redis.call("zadd", KEYS[2], now_ms + tonumber(ARGV[4]), ARGV[1])
	return 1
end
return 0`
)

type leaseCandidate struct {
	ID      string
	OwnerID string
}

type workerLeaseStore interface {
	Acquire(context.Context, string, string, string, time.Duration) (bool, error)
	Renew(context.Context, string, string, string, time.Duration) (bool, error)
	Release(context.Context, string, string, string) (bool, error)
	AcquireRecovery(context.Context, string, []leaseCandidate, string, time.Duration) (map[string]bool, error)
	Expired(context.Context, string, time.Time, int64) ([]leaseCandidate, error)
}

type redisWorkerLeaseStore struct {
	client *redis.Client
}

func (r *runRepo) leases() (workerLeaseStore, error) {
	r.leaseOnce.Do(func() {
		if r.leaseStore != nil {
			return
		}
		client, err := sharedStore().ensureRedis()
		if err != nil {
			r.leaseErr = err
			return
		}
		r.leaseStore = &redisWorkerLeaseStore{client: client}
	})
	return r.leaseStore, r.leaseErr
}

func workerLeaseTTL(kind string) time.Duration {
	key, fallback := "worker.orphan_timeout", 6*time.Second
	if kind == publicationLeaseKind {
		key, fallback = "publication.worker.orphan_timeout", 30*time.Second
	}
	if ttl := viper.GetDuration(key); ttl > 0 {
		return ttl
	}
	return fallback
}

func recoveryLeaseOwner(kind string) string {
	return "recovery:" + token.NewID(kind)
}

func releaseLeaseBestEffort(ctx context.Context, leases workerLeaseStore, kind, id, owner string) {
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	if _, err := leases.Release(releaseCtx, kind, id, owner); err != nil {
		zap.L().Warn("release worker lease failed", zap.String("kind", kind), zap.String("id", id), zap.Error(err))
	}
}

func workerLeaseKey(kind, id string) string {
	return "app:worker:{" + kind + "}:owner:" + id
}

func workerDeadlineKey(kind string) string {
	return "app:worker:{" + kind + "}:deadlines"
}

func (s *redisWorkerLeaseStore) Acquire(ctx context.Context, kind, id, owner string, ttl time.Duration) (bool, error) {
	if err := validateLeaseArguments(kind, id, owner, ttl); err != nil {
		return false, err
	}
	result, err := s.client.Eval(ctx, acquireLeaseScript, []string{workerLeaseKey(kind, id), workerDeadlineKey(kind)}, owner, ttl.Milliseconds(), id).Int64()
	return result == 1, err
}

func (s *redisWorkerLeaseStore) Renew(ctx context.Context, kind, id, owner string, ttl time.Duration) (bool, error) {
	if err := validateLeaseArguments(kind, id, owner, ttl); err != nil {
		return false, err
	}
	result, err := s.client.Eval(ctx, renewLeaseScript, []string{workerLeaseKey(kind, id), workerDeadlineKey(kind)}, owner, ttl.Milliseconds(), id).Int64()
	return result == 1, err
}

func (s *redisWorkerLeaseStore) Release(ctx context.Context, kind, id, owner string) (bool, error) {
	if err := validateLeaseArguments(kind, id, owner, time.Millisecond); err != nil {
		return false, err
	}
	result, err := s.client.Eval(ctx, releaseLeaseScript, []string{workerLeaseKey(kind, id), workerDeadlineKey(kind)}, owner, id).Int64()
	return result == 1, err
}

func (s *redisWorkerLeaseStore) AcquireRecovery(ctx context.Context, kind string, candidates []leaseCandidate, owner string, ttl time.Duration) (map[string]bool, error) {
	results := make(map[string]bool, len(candidates))
	if len(candidates) == 0 {
		return results, nil
	}
	if err := validateLeaseArguments(kind, candidates[0].ID, owner, ttl); err != nil {
		return nil, err
	}
	commands := make([]*redis.Cmd, len(candidates))
	_, err := s.client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for index, candidate := range candidates {
			if candidate.ID == "" {
				return fmt.Errorf("lease id is required")
			}
			commands[index] = pipe.Eval(ctx, takeoverLeaseScript, []string{workerLeaseKey(kind, candidate.ID), workerDeadlineKey(kind)}, candidate.ID, candidate.OwnerID, owner, ttl.Milliseconds())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for index, candidate := range candidates {
		value, resultErr := commands[index].Int64()
		if resultErr != nil {
			return nil, resultErr
		}
		results[candidate.ID] = value == 1
	}
	return results, nil
}

func (s *redisWorkerLeaseStore) Expired(ctx context.Context, kind string, _ time.Time, limit int64) ([]leaseCandidate, error) {
	if kind != runLeaseKind && kind != publicationLeaseKind {
		return nil, fmt.Errorf("unsupported lease kind %q", kind)
	}
	if limit <= 0 {
		limit = 100
	}
	now, err := s.client.Time(ctx).Result()
	if err != nil {
		return nil, err
	}
	ids, err := s.client.ZRangeByScore(ctx, workerDeadlineKey(kind), &redis.ZRangeBy{
		Min: "-inf", Max: fmt.Sprintf("%d", now.UnixMilli()), Offset: 0, Count: limit,
	}).Result()
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	keys := make([]string, len(ids))
	for index, id := range ids {
		keys[index] = workerLeaseKey(kind, id)
	}
	owners, err := s.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	result := make([]leaseCandidate, 0, len(ids))
	stale := make([]any, 0)
	for index, value := range owners {
		owner, _ := value.(string)
		if owner != "" {
			result = append(result, leaseCandidate{ID: ids[index], OwnerID: owner})
		} else {
			stale = append(stale, ids[index])
		}
	}
	if len(stale) != 0 {
		if err = s.client.ZRem(ctx, workerDeadlineKey(kind), stale...).Err(); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func validateLeaseArguments(kind, id, owner string, ttl time.Duration) error {
	if kind != runLeaseKind && kind != publicationLeaseKind {
		return fmt.Errorf("unsupported lease kind %q", kind)
	}
	if id == "" || owner == "" {
		return fmt.Errorf("lease id and owner are required")
	}
	if ttl < time.Millisecond {
		return fmt.Errorf("lease TTL must be at least one millisecond")
	}
	return nil
}
