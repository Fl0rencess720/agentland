package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/spf13/viper"
	"github.com/twmb/franz-go/pkg/kgo"
)

type taskEnvelope struct {
	ID string `json:"id"`
}

type runEventEnvelope struct {
	RunID string             `json:"run_id"`
	Event *models.AgentEvent `json:"event"`
}

func (p *KafkaPipeline) PublishRunTask(ctx context.Context, id, projectID string) error {
	return p.publishTask(ctx, kafkaRunTopic(), id, projectID)
}

func (p *KafkaPipeline) PublishPublicationTask(ctx context.Context, id, projectID string) error {
	return p.publishTask(ctx, kafkaPublicationTopic(), id, projectID)
}

func (p *KafkaPipeline) publishTask(ctx context.Context, topic, id, projectID string) error {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(projectID) == "" {
		return errors.New("task id and project id are required")
	}
	payload, err := json.Marshal(taskEnvelope{ID: id})
	if err != nil {
		return err
	}
	return p.produce(ctx, &kgo.Record{Topic: topic, Key: []byte(projectID), Value: payload})
}

func (p *KafkaPipeline) PublishRunEvent(ctx context.Context, event *models.AgentEvent) error {
	if event == nil || strings.TrimSpace(event.RunID) == "" || event.Sequence <= 0 || strings.TrimSpace(event.Type) == "" {
		return errors.New("run event identity is invalid")
	}
	payload, err := json.Marshal(runEventEnvelope{RunID: event.RunID, Event: event})
	if err != nil {
		return err
	}
	return p.produce(ctx, &kgo.Record{Topic: kafkaEventTopic(), Key: []byte(event.RunID), Value: payload})
}

func (p *KafkaPipeline) produce(ctx context.Context, record *kgo.Record) error {
	results := p.producer.ProduceSync(ctx, record)
	if err := results.FirstErr(); err != nil {
		return fmt.Errorf("publish kafka record to %s: %w", record.Topic, err)
	}
	return nil
}

func kafkaRunTopic() string { return strings.TrimSpace(viper.GetString("kafka.run_topic")) }
func kafkaPublicationTopic() string {
	return strings.TrimSpace(viper.GetString("kafka.publication_topic"))
}
func kafkaEventTopic() string { return strings.TrimSpace(viper.GetString("kafka.event_topic")) }
