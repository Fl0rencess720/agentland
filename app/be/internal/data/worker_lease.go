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

	renewLeaseScript = `
if redis.call("get", KEYS[1]) == ARGV[1] then
  return redis.call("pexpire", KEYS[1], ARGV[2])
end
return 0`
	releaseLeaseScript = `
if redis.call("get", KEYS[1]) == ARGV[1] then
  return redis.call("del", KEYS[1])
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
	key, fallback := "worker.orphan_timeout", 30*time.Second
	if kind == publicationLeaseKind {
		key = "publication.worker.orphan_timeout"
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
	return "app:worker:lease:" + kind + ":" + id
}

func (s *redisWorkerLeaseStore) Acquire(ctx context.Context, kind, id, owner string, ttl time.Duration) (bool, error) {
	if err := validateLeaseArguments(kind, id, owner, ttl); err != nil {
		return false, err
	}
	return s.client.SetNX(ctx, workerLeaseKey(kind, id), owner, ttl).Result()
}

func (s *redisWorkerLeaseStore) Renew(ctx context.Context, kind, id, owner string, ttl time.Duration) (bool, error) {
	if err := validateLeaseArguments(kind, id, owner, ttl); err != nil {
		return false, err
	}
	result, err := s.client.Eval(ctx, renewLeaseScript, []string{workerLeaseKey(kind, id)}, owner, ttl.Milliseconds()).Int64()
	return result == 1, err
}

func (s *redisWorkerLeaseStore) Release(ctx context.Context, kind, id, owner string) (bool, error) {
	if err := validateLeaseArguments(kind, id, owner, time.Millisecond); err != nil {
		return false, err
	}
	result, err := s.client.Eval(ctx, releaseLeaseScript, []string{workerLeaseKey(kind, id)}, owner).Int64()
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
	commands := make([]*redis.BoolCmd, len(candidates))
	_, err := s.client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		for index, candidate := range candidates {
			if candidate.ID == "" {
				return fmt.Errorf("lease id is required")
			}
			commands[index] = pipe.SetNX(ctx, workerLeaseKey(kind, candidate.ID), owner, ttl)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for index, candidate := range candidates {
		claimed, resultErr := commands[index].Result()
		if resultErr != nil {
			return nil, resultErr
		}
		results[candidate.ID] = claimed
	}
	return results, nil
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
