package data

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseEventCursor(t *testing.T) {
	sequence, err := parseEventCursor("42")
	require.NoError(t, err)
	require.Equal(t, int64(42), sequence)

	sequence, err = parseEventCursor("1730000000000-0")
	require.NoError(t, err)
	require.Zero(t, sequence)

	_, err = parseEventCursor("invalid")
	require.Error(t, err)
}

func TestRunEventNotifierWakesOnlyMatchingRun(t *testing.T) {
	store := newKafkaRunEventStore()
	matching, unsubscribeMatching := store.subscribe("run-1")
	defer unsubscribeMatching()
	other, unsubscribeOther := store.subscribe("run-2")
	defer unsubscribeOther()

	store.notify("run-1")
	select {
	case <-matching:
	case <-time.After(time.Second):
		t.Fatal("matching waiter was not notified")
	}
	select {
	case <-other:
		t.Fatal("unrelated waiter was notified")
	default:
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	waitContext(ctx, time.Second)
}
