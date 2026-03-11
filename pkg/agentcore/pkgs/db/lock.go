package db

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

var (
	luaAcquireLock = redis.NewScript(`
if redis.call("SET", KEYS[1], ARGV[1], "NX", "PX", ARGV[2]) then
  return 1
end
return 0
`)

	luaRenewLock = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("PEXPIRE", KEYS[1], ARGV[2])
end
return 0
`)

	luaReleaseLock = redis.NewScript(`
if redis.call("GET", KEYS[1]) == ARGV[1] then
  return redis.call("DEL", KEYS[1])
end
return 0
`)
)

type RedisLock struct {
	client *redis.Client
	key    string
	token  string
}

func NewRedisLock(client *redis.Client, key string) (*RedisLock, error) {
	if client == nil {
		return nil, fmt.Errorf("redis client is nil")
	}
	if key == "" {
		return nil, fmt.Errorf("lock key is empty")
	}
	token, err := randomHexToken(16)
	if err != nil {
		return nil, err
	}
	return &RedisLock{
		client: client,
		key:    key,
		token:  token,
	}, nil
}

func (l *RedisLock) Acquire(ctx context.Context, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		return false, fmt.Errorf("lock ttl is invalid: %s", ttl)
	}
	ms := ttl.Milliseconds()
	if ms <= 0 {
		ms = 1
	}
	v, err := luaAcquireLock.Run(ctx, l.client, []string{l.key}, l.token, ms).Int64()
	if err != nil {
		return false, err
	}
	return v == 1, nil
}

func (l *RedisLock) Renew(ctx context.Context, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		return false, fmt.Errorf("lock ttl is invalid: %s", ttl)
	}
	ms := ttl.Milliseconds()
	if ms <= 0 {
		ms = 1
	}
	v, err := luaRenewLock.Run(ctx, l.client, []string{l.key}, l.token, ms).Int64()
	if err != nil {
		return false, err
	}
	return v == 1, nil
}

func (l *RedisLock) Release(ctx context.Context) error {
	_, err := luaReleaseLock.Run(ctx, l.client, []string{l.key}, l.token).Int64()
	return err
}

func (l *RedisLock) Watchdog(ctx context.Context, interval, ttl time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("watchdog interval is invalid: %s", interval)
	}
	if ttl <= 0 {
		return fmt.Errorf("watchdog ttl is invalid: %s", ttl)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if ctx.Err() != nil {
				return nil
			}
			renewCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			ok, err := l.Renew(renewCtx, ttl)
			cancel()
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return nil
				}
				return err
			}
			if !ok {
				return nil
			}
		}
	}
}

func randomHexToken(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("token size is invalid: %d", n)
	}
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}
