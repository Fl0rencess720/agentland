package data

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/spf13/viper"
	"go.uber.org/zap"
)

type kafkaRunEventStore struct {
	notifyMu sync.Mutex
	waiters  map[string]map[chan struct{}]struct{}
	repo     *runRepo
}

func newKafkaRunEventStore() *kafkaRunEventStore {
	return &kafkaRunEventStore{waiters: make(map[string]map[chan struct{}]struct{}), repo: sharedRunRepo}
}

func (s *kafkaRunEventStore) runRepo() *runRepo {
	if s.repo != nil {
		return s.repo
	}
	return sharedRunRepo
}

func (s *kafkaRunEventStore) Read(ctx context.Context, runID, after string, block time.Duration) ([]*models.StoredRunEvent, error) {
	sequence, err := parseEventCursor(after)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(block)
	for {
		events, err := s.readAvailable(ctx, runID, sequence)
		if err != nil || len(events) != 0 || block <= 0 {
			return events, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, nil
		}
		wake, unsubscribe := s.subscribe(runID)
		// This second read closes the query-to-subscribe race.
		events, err = s.readAvailable(ctx, runID, sequence)
		if err != nil || len(events) != 0 {
			unsubscribe()
			return events, err
		}
		wait := min(remaining, time.Second)
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			unsubscribe()
			return nil, ctx.Err()
		case <-wake:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
		}
		unsubscribe()
	}
}

func (s *kafkaRunEventStore) readAvailable(ctx context.Context, runID string, after int64) ([]*models.StoredRunEvent, error) {
	pool, err := s.runRepo().ready(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `select sequence,event_type,data from run_events where run_id=$1 and sequence>$2 order by sequence limit 200`, runID, after)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]*models.StoredRunEvent, 0)
	for rows.Next() {
		var sequence int64
		var eventType string
		var data []byte
		if err = rows.Scan(&sequence, &eventType, &data); err != nil {
			return nil, err
		}
		events = append(events, &models.StoredRunEvent{ID: strconv.FormatInt(sequence, 10), Type: eventType, Data: json.RawMessage(data)})
	}
	return events, rows.Err()
}

func parseEventCursor(value string) (int64, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "0-0" {
		return 0, nil
	}
	if strings.Contains(value, "-") {
		// Redis stream IDs have no relation to agent sequence; replay the projected Kafka history.
		return 0, nil
	}
	sequence, err := strconv.ParseInt(value, 10, 64)
	if err != nil || sequence < 0 {
		return 0, errors.New("invalid Last-Event-ID")
	}
	return sequence, nil
}

func (s *kafkaRunEventStore) project(ctx context.Context, payload []byte) error {
	var envelope runEventEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	if envelope.Event == nil || envelope.RunID == "" || envelope.Event.RunID != envelope.RunID || envelope.Event.Sequence <= 0 {
		return errors.New("invalid run event envelope")
	}
	data, err := json.Marshal(envelope.Event)
	if err != nil {
		return err
	}
	pool, err := s.runRepo().ready(ctx)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `insert into run_events(run_id,sequence,event_type,data,created_at,expires_at)
		values($1,$2,$3,$4,$5,null) on conflict(run_id,sequence) do nothing`, envelope.RunID, envelope.Event.Sequence, envelope.Event.Type, data, envelope.Event.Timestamp)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var storedType string
		var storedData []byte
		if err = tx.QueryRow(ctx, `select event_type,data from run_events where run_id=$1 and sequence=$2`, envelope.RunID, envelope.Event.Sequence).Scan(&storedType, &storedData); err != nil {
			return err
		}
		if storedType != envelope.Event.Type || !bytes.Equal(storedData, data) {
			return fmt.Errorf("run event conflict at %s sequence %d", envelope.RunID, envelope.Event.Sequence)
		}
	}
	if isTerminalKafkaEvent(envelope.Event.Type) {
		if _, err = tx.Exec(ctx, `update run_events set expires_at=coalesce(expires_at,now()+interval '24 hours') where run_id=$1`, envelope.RunID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `select pg_notify('agentland_run_event',$1)`, envelope.RunID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *kafkaRunEventStore) deleteExpired(ctx context.Context) {
	pool, err := s.runRepo().ready(ctx)
	if err == nil {
		_, err = pool.Exec(ctx, `delete from run_events where expires_at<now()`)
	}
	if err != nil && ctx.Err() == nil {
		zap.L().Warn("delete expired run events failed", zap.Error(err))
	}
}

func isTerminalKafkaEvent(eventType string) bool {
	return eventType == "run.completed" || eventType == "run.failed" || eventType == "run.cancelled"
}

func (s *kafkaRunEventStore) subscribe(runID string) (<-chan struct{}, func()) {
	channel := make(chan struct{}, 1)
	s.notifyMu.Lock()
	if s.waiters[runID] == nil {
		s.waiters[runID] = make(map[chan struct{}]struct{})
	}
	s.waiters[runID][channel] = struct{}{}
	s.notifyMu.Unlock()
	return channel, func() {
		s.notifyMu.Lock()
		delete(s.waiters[runID], channel)
		if len(s.waiters[runID]) == 0 {
			delete(s.waiters, runID)
		}
		s.notifyMu.Unlock()
	}
}

func (s *kafkaRunEventStore) notify(runID string) {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	for channel := range s.waiters[runID] {
		select {
		case channel <- struct{}{}:
		default:
		}
	}
}

func (s *kafkaRunEventStore) runNotifier(ctx context.Context) {
	dsn := strings.TrimSpace(viper.GetString("database.url"))
	for ctx.Err() == nil {
		conn, err := pgx.Connect(ctx, dsn)
		if err != nil {
			zap.L().Warn("connect postgres event notifier failed", zap.Error(err))
			waitContext(ctx, time.Second)
			continue
		}
		if _, err = conn.Exec(ctx, `listen agentland_run_event`); err == nil {
			for ctx.Err() == nil {
				notification, waitErr := conn.WaitForNotification(ctx)
				if waitErr != nil {
					err = waitErr
					break
				}
				s.notify(notification.Payload)
			}
		}
		_ = conn.Close(context.Background())
		if ctx.Err() == nil {
			zap.L().Warn("postgres event notifier disconnected", zap.Error(err))
			waitContext(ctx, time.Second)
		}
	}
}

func waitContext(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
