package data

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/jackc/pgx/v5"
	"github.com/spf13/viper"
)

const (
	outboxKindRunTask         = "run.task"
	outboxKindPublicationTask = "publication.task"
	outboxKindRunEvent        = "run.event"
)

type taskEnvelope struct {
	ID string `json:"id"`
}

type runEventEnvelope struct {
	RunID string             `json:"run_id"`
	Event *models.AgentEvent `json:"event"`
}

func enqueueTask(ctx context.Context, tx pgx.Tx, topic, kind, id, projectID string) error {
	payload, err := json.Marshal(taskEnvelope{ID: id})
	if err != nil {
		return err
	}
	return enqueueOutbox(ctx, tx, topic, projectID, kind, payload, kind+":"+id)
}

func enqueueRunEvent(ctx context.Context, tx pgx.Tx, event *models.AgentEvent) error {
	if event == nil {
		return nil
	}
	if strings.TrimSpace(event.RunID) == "" || event.Sequence <= 0 || strings.TrimSpace(event.Type) == "" {
		return fmt.Errorf("run event identity is invalid")
	}
	payload, err := json.Marshal(runEventEnvelope{RunID: event.RunID, Event: event})
	if err != nil {
		return err
	}
	dedupeKey := fmt.Sprintf("%s:%d", event.RunID, event.Sequence)
	return enqueueOutbox(ctx, tx, kafkaEventTopic(), event.RunID, outboxKindRunEvent, payload, dedupeKey)
}

func enqueueOutbox(ctx context.Context, tx pgx.Tx, topic, key, kind string, payload []byte, dedupeKey string) error {
	tag, err := tx.Exec(ctx, `insert into kafka_outbox(topic,message_key,message_kind,payload,dedupe_key,created_at)
		values($1,$2,$3,$4,$5,now()) on conflict(dedupe_key) do nothing`, topic, key, kind, payload, dedupeKey)
	if err != nil || tag.RowsAffected() == 1 {
		return err
	}
	var storedTopic, storedKey, storedKind string
	var storedPayload []byte
	if err = tx.QueryRow(ctx, `select topic,message_key,message_kind,payload from kafka_outbox where dedupe_key=$1`, dedupeKey).Scan(&storedTopic, &storedKey, &storedKind, &storedPayload); err != nil {
		return err
	}
	if storedTopic != topic || storedKey != key || storedKind != kind || !bytes.Equal(storedPayload, payload) {
		return fmt.Errorf("kafka outbox dedupe conflict for %s", dedupeKey)
	}
	return nil
}

func kafkaRunTopic() string { return strings.TrimSpace(viper.GetString("kafka.run_topic")) }
func kafkaPublicationTopic() string {
	return strings.TrimSpace(viper.GetString("kafka.publication_topic"))
}
func kafkaEventTopic() string { return strings.TrimSpace(viper.GetString("kafka.event_topic")) }
