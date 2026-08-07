package data

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type captureRedisHook struct{ args []any }

func (h *captureRedisHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *captureRedisHook) ProcessHook(_ redis.ProcessHook) redis.ProcessHook {
	return func(_ context.Context, cmd redis.Cmder) error {
		h.args = append([]any(nil), cmd.Args()...)
		if result, ok := cmd.(*redis.StringCmd); ok {
			result.SetVal("1-0")
		}
		return nil
	}
}

func (h *captureRedisHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestRunEventPublishKeepsTheCompleteActiveStream(t *testing.T) {
	client := redis.NewClient(&redis.Options{Addr: "unused"})
	t.Cleanup(func() { _ = client.Close() })
	hook := &captureRedisHook{}
	client.AddHook(hook)
	store := &redisRunEventStore{client: client}

	_, err := store.Publish(context.Background(), "run-1", &models.AgentEvent{Type: "run.started"})
	require.NoError(t, err)
	arguments := make([]string, 0, len(hook.args))
	for _, argument := range hook.args {
		arguments = append(arguments, strings.ToLower(fmt.Sprint(argument)))
	}
	require.Equal(t, []string{"xadd", runEventKey("run-1")}, arguments[:2])
	require.NotContains(t, arguments, "maxlen")
}
