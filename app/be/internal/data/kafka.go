package data

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Fl0rencess720/agentland/app/be/internal/biz"
	"github.com/Fl0rencess720/agentland/app/be/internal/models"
	"github.com/spf13/viper"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"
	"github.com/twmb/franz-go/pkg/sasl/scram"
	"go.uber.org/zap"
)

type KafkaPipeline struct {
	repo                         *runRepo
	producer                     *kgo.Client
	runQueue                     *kafkaTaskQueue
	pubQueue                     *kafkaTaskQueue
	projector                    *kgo.Client
	publicationPreparation       *kgo.Client
	events                       *kafkaRunEventStore
	publicationPreparationHandle func(context.Context, *models.AgentEvent) error
}

func NewKafkaPipeline(ctx context.Context) (*KafkaPipeline, error) {
	return newKafkaPipeline(ctx, NewRunRepo().(*runRepo))
}

func newKafkaPipeline(ctx context.Context, repo *runRepo) (*KafkaPipeline, error) {
	base, err := kafkaOptions()
	if err != nil {
		return nil, err
	}
	producer, err := kgo.NewClient(append(base,
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProducerBatchCompression(kgo.SnappyCompression()),
		kgo.ProducerBatchMaxBytes(kafkaMaxEventBytes()),
		kgo.ProducerLinger(5*time.Millisecond),
	)...)
	if err != nil {
		return nil, err
	}
	pipeline := &KafkaPipeline{repo: repo, producer: producer}
	cleanup := func(err error) (*KafkaPipeline, error) {
		pipeline.Close()
		return nil, err
	}
	if err = pipeline.ensureTopics(ctx); err != nil {
		return cleanup(err)
	}
	pipeline.runQueue, err = newKafkaTaskQueue(base, kafkaRunTopic(), viper.GetString("kafka.run_consumer_group"))
	if err != nil {
		return cleanup(err)
	}
	pipeline.pubQueue, err = newKafkaTaskQueue(base, kafkaPublicationTopic(), viper.GetString("kafka.publication_consumer_group"))
	if err != nil {
		return cleanup(err)
	}
	pipeline.projector, err = kgo.NewClient(append(base, kgo.ConsumerGroup(viper.GetString("kafka.event_projector_group")), kgo.ConsumeTopics(kafkaEventTopic()), kgo.DisableAutoCommit())...)
	if err != nil {
		return cleanup(err)
	}
	pipeline.publicationPreparation, err = kgo.NewClient(append(base, kgo.ConsumerGroup(viper.GetString("kafka.publication_preparation_group")), kgo.ConsumeTopics(kafkaEventTopic()), kgo.DisableAutoCommit())...)
	if err != nil {
		return cleanup(err)
	}
	pipeline.events = newKafkaRunEventStore()
	pipeline.events.repo = repo
	return pipeline, nil
}

func (p *KafkaPipeline) RunQueue() biz.TaskQueue         { return p.runQueue }
func (p *KafkaPipeline) PublicationQueue() biz.TaskQueue { return p.pubQueue }
func (p *KafkaPipeline) EventStore() biz.RunEventStore   { return p.events }

func (p *KafkaPipeline) SetPublicationPreparationHandler(handler func(context.Context, *models.AgentEvent) error) {
	p.publicationPreparationHandle = handler
}

func (p *KafkaPipeline) Close() {
	if p.runQueue != nil {
		p.runQueue.client.Close()
	}
	if p.pubQueue != nil {
		p.pubQueue.client.Close()
	}
	if p.projector != nil {
		p.projector.Close()
	}
	if p.publicationPreparation != nil {
		p.publicationPreparation.Close()
	}
	if p.producer != nil {
		p.producer.Close()
	}
}

func (p *KafkaPipeline) ensureTopics(ctx context.Context) error {
	if err := p.producer.Ping(ctx); err != nil {
		return fmt.Errorf("connect kafka: %w", err)
	}
	admin := kadm.NewClient(p.producer)
	replication := int16(viper.GetInt("kafka.replication_factor"))
	if replication <= 0 {
		replication = 1
	}
	taskPartitions := int32(viper.GetInt("kafka.task_partitions"))
	eventPartitions := int32(viper.GetInt("kafka.event_partitions"))
	if taskPartitions <= 0 || eventPartitions <= 0 {
		return errors.New("kafka partition counts must be positive")
	}
	if err := createTopics(ctx, admin, taskPartitions, replication, nil, kafkaRunTopic(), kafkaPublicationTopic()); err != nil {
		return err
	}
	retention := viper.GetDuration("kafka.event_retention")
	if retention < 24*time.Hour {
		return errors.New("kafka.event_retention must be at least 24h")
	}
	retentionMS := strconv.FormatInt(retention.Milliseconds(), 10)
	maxEventBytes := strconv.Itoa(int(kafkaMaxEventBytes()))
	return createTopics(ctx, admin, eventPartitions, replication, map[string]*string{
		"retention.ms": &retentionMS, "max.message.bytes": &maxEventBytes,
	}, kafkaEventTopic())
}

func createTopics(ctx context.Context, admin *kadm.Client, partitions int32, replication int16, configs map[string]*string, topics ...string) error {
	responses, err := admin.CreateTopics(ctx, partitions, replication, configs, topics...)
	if err != nil {
		return err
	}
	for topic, response := range responses {
		if response.Err != nil && !errors.Is(response.Err, kerr.TopicAlreadyExists) {
			return fmt.Errorf("create kafka topic %s: %w", topic, response.Err)
		}
	}
	return nil
}

type kafkaTaskQueue struct {
	client     *kgo.Client
	pending    *kgo.Record
	taskID     string
	pendingErr error
}

func newKafkaTaskQueue(base []kgo.Opt, topic, group string) (*kafkaTaskQueue, error) {
	client, err := kgo.NewClient(append(base, kgo.ConsumerGroup(strings.TrimSpace(group)), kgo.ConsumeTopics(topic), kgo.DisableAutoCommit())...)
	if err != nil {
		return nil, err
	}
	return &kafkaTaskQueue{client: client}, nil
}

func (q *kafkaTaskQueue) Receive(ctx context.Context) (biz.TaskDelivery, error) {
	for {
		if q.pending != nil {
			if q.pendingErr != nil {
				return nil, q.pendingErr
			}
			return &kafkaTaskDelivery{id: q.taskID, queue: q, record: q.pending}, nil
		}
		fetches := q.client.PollRecords(ctx, 1)
		if err := fetches.Err(); err != nil {
			return nil, err
		}
		var record *kgo.Record
		fetches.EachRecord(func(item *kgo.Record) { record = item })
		if record == nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			continue
		}
		q.pending = record
		var payload taskEnvelope
		if err := json.Unmarshal(record.Value, &payload); err != nil || strings.TrimSpace(payload.ID) == "" {
			q.pendingErr = fmt.Errorf("decode kafka task at %s/%d/%d: %w", record.Topic, record.Partition, record.Offset, errors.Join(err, errors.New("task id is required")))
			return nil, q.pendingErr
		}
		q.taskID = payload.ID
		return &kafkaTaskDelivery{id: payload.ID, queue: q, record: record}, nil
	}
}

type kafkaTaskDelivery struct {
	id     string
	queue  *kafkaTaskQueue
	record *kgo.Record
}

func (d *kafkaTaskDelivery) ID() string { return d.id }
func (d *kafkaTaskDelivery) Ack(ctx context.Context) error {
	if d.queue.pending == d.record {
		d.queue.pending, d.queue.taskID, d.queue.pendingErr = nil, "", nil
	}
	return d.queue.client.CommitRecords(ctx, d.record)
}

func kafkaOptions() ([]kgo.Opt, error) {
	brokers := viper.GetStringSlice("kafka.brokers")
	if len(brokers) == 0 {
		return nil, errors.New("kafka.brokers is required")
	}
	maxEventBytes := kafkaMaxEventBytes()
	options := []kgo.Opt{
		kgo.SeedBrokers(brokers...), kgo.ClientID(viper.GetString("kafka.client_id")),
		kgo.FetchMaxPartitionBytes(maxEventBytes), kgo.FetchMaxBytes(maxEventBytes * 2),
	}
	protocol := strings.ToLower(strings.TrimSpace(viper.GetString("kafka.security_protocol")))
	if protocol == "" {
		protocol = "plaintext"
	}
	if protocol == "tls" || protocol == "sasl_tls" {
		config, err := kafkaTLSConfig()
		if err != nil {
			return nil, err
		}
		options = append(options, kgo.DialTLSConfig(config))
	} else if protocol != "plaintext" && protocol != "sasl_plaintext" {
		return nil, fmt.Errorf("unsupported kafka.security_protocol %q", protocol)
	}
	if protocol == "sasl_tls" || protocol == "sasl_plaintext" {
		username, password := viper.GetString("kafka.sasl.username"), viper.GetString("kafka.sasl.password")
		switch strings.ToLower(strings.TrimSpace(viper.GetString("kafka.sasl.mechanism"))) {
		case "plain":
			options = append(options, kgo.SASL(plain.Auth{User: username, Pass: password}.AsMechanism()))
		case "scram-sha-256":
			options = append(options, kgo.SASL(scram.Auth{User: username, Pass: password}.AsSha256Mechanism()))
		case "scram-sha-512":
			options = append(options, kgo.SASL(scram.Auth{User: username, Pass: password}.AsSha512Mechanism()))
		default:
			return nil, errors.New("unsupported kafka.sasl.mechanism")
		}
	}
	return options, nil
}

func kafkaMaxEventBytes() int32 {
	value := viper.GetInt("kafka.max_event_bytes")
	if value <= 0 {
		return 20 << 20
	}
	return int32(value)
}

func kafkaTLSConfig() (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: strings.TrimSpace(viper.GetString("kafka.tls.server_name"))}
	if path := strings.TrimSpace(viper.GetString("kafka.tls.ca_file")); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			return nil, errors.New("kafka TLS CA file contains no certificates")
		}
		config.RootCAs = pool
	}
	certFile, keyFile := strings.TrimSpace(viper.GetString("kafka.tls.cert_file")), strings.TrimSpace(viper.GetString("kafka.tls.key_file"))
	if (certFile == "") != (keyFile == "") {
		return nil, errors.New("kafka TLS certificate and key must be configured together")
	}
	if certFile != "" {
		certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		config.Certificates = []tls.Certificate{certificate}
	}
	return config, nil
}

func (p *KafkaPipeline) RunEventProjector(ctx context.Context) {
	cleanup := time.NewTicker(time.Hour)
	defer cleanup.Stop()
	for {
		select {
		case <-cleanup.C:
			p.events.deleteExpired(ctx)
		default:
		}
		if err := p.projectNext(ctx); err != nil && ctx.Err() == nil {
			zap.L().Warn("consume run event failed", zap.Error(err))
			waitContext(ctx, time.Second)
		}
		if ctx.Err() != nil {
			return
		}
	}
}

func (p *KafkaPipeline) projectNext(ctx context.Context) error {
	fetches := p.projector.PollRecords(ctx, 1)
	if err := fetches.Err(); err != nil {
		return err
	}
	var record *kgo.Record
	fetches.EachRecord(func(item *kgo.Record) { record = item })
	if record == nil {
		return nil
	}
	for ctx.Err() == nil {
		if err := p.events.project(ctx, record.Value); err != nil {
			zap.L().Error("project run event failed", zap.Int32("partition", record.Partition), zap.Int64("offset", record.Offset), zap.Error(err))
			waitContext(ctx, time.Second)
			continue
		}
		var commitErr error
		for attempt := 0; attempt < 3 && ctx.Err() == nil; attempt++ {
			if commitErr = p.projector.CommitRecords(ctx, record); commitErr == nil {
				return nil
			}
			zap.L().Warn("commit run event projection failed", zap.Error(commitErr))
			waitContext(ctx, time.Second)
		}
		return commitErr
	}
	return ctx.Err()
}

func (p *KafkaPipeline) RunPublicationPreparation(ctx context.Context) {
	for ctx.Err() == nil {
		if err := p.preparePublicationNext(ctx); err != nil && ctx.Err() == nil {
			zap.L().Warn("consume publication preparation event failed", zap.Error(err))
			waitContext(ctx, time.Second)
		}
	}
}

func (p *KafkaPipeline) preparePublicationNext(ctx context.Context) error {
	if p.publicationPreparationHandle == nil {
		return errors.New("publication preparation handler is not configured")
	}
	fetches := p.publicationPreparation.PollRecords(ctx, 1)
	if err := fetches.Err(); err != nil {
		return err
	}
	var record *kgo.Record
	fetches.EachRecord(func(item *kgo.Record) { record = item })
	if record == nil {
		return nil
	}
	var envelope runEventEnvelope
	if err := json.Unmarshal(record.Value, &envelope); err != nil {
		return err
	}
	if envelope.Event == nil || envelope.RunID == "" || envelope.Event.RunID != envelope.RunID {
		return errors.New("invalid run event envelope")
	}
	for ctx.Err() == nil {
		if err := p.publicationPreparationHandle(ctx, envelope.Event); err != nil {
			zap.L().Warn("handle publication preparation event failed", zap.String("run_id", envelope.RunID), zap.Error(err))
			waitContext(ctx, time.Second)
			continue
		}
		var commitErr error
		for attempt := 0; attempt < 3 && ctx.Err() == nil; attempt++ {
			if commitErr = p.publicationPreparation.CommitRecords(ctx, record); commitErr == nil {
				return nil
			}
			waitContext(ctx, 250*time.Millisecond)
		}
		return commitErr
	}
	return ctx.Err()
}

func (p *KafkaPipeline) RunEventNotifier(ctx context.Context) { p.events.runNotifier(ctx) }
