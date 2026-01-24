package mqx

import (
	"context"
	"errors"
	"time"

	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"swarm-agv-arm-scheduling-system/shared/config"
)

type Producer struct {
	writer *kafka.Writer
}

func NewProducer(cfg config.Config) (*Producer, error) {
	if len(cfg.KafkaBrokers) == 0 {
		return nil, errors.New("KAFKA_BROKERS is required")
	}
	w := &kafka.Writer{
		Addr:         kafka.TCP(cfg.KafkaBrokers...),
		Balancer:     &kafka.LeastBytes{},
		RequiredAcks: kafka.RequireAll,
		Async:        false,
		MaxAttempts:  maxInt(cfg.KafkaRetryMax, 1),
		BatchTimeout: time.Duration(cfg.KafkaWriteMS) * time.Millisecond,
		Transport: &kafka.Transport{
			ClientID: cfg.KafkaClientID,
		},
	}
	return &Producer{writer: w}, nil
}

func (p *Producer) Publish(ctx context.Context, topic string, key []byte, value []byte, headers map[string]string) error {
	if p == nil || p.writer == nil {
		return errors.New("producer not initialized")
	}
	ctx, span := otel.Tracer("mqx").Start(ctx, "kafka.produce")
	span.SetAttributes(
		attribute.String("messaging.system", "kafka"),
		attribute.String("messaging.destination", topic),
	)
	defer span.End()
	msg := kafka.Message{
		Topic: topic,
		Key:   key,
		Value: value,
	}
	if len(headers) > 0 {
		msg.Headers = make([]kafka.Header, 0, len(headers))
		for k, v := range headers {
			msg.Headers = append(msg.Headers, kafka.Header{Key: k, Value: []byte(v)})
		}
	}
	return p.writer.WriteMessages(ctx, msg)
}

func (p *Producer) Close() error {
	if p == nil || p.writer == nil {
		return nil
	}
	return p.writer.Close()
}

func NewConsumer(cfg config.Config, topic string, groupID string) (*kafka.Reader, error) {
	if len(cfg.KafkaBrokers) == 0 {
		return nil, errors.New("KAFKA_BROKERS is required")
	}
	if groupID == "" {
		groupID = cfg.KafkaGroupID
	}
	if groupID == "" {
		return nil, errors.New("KAFKA_CONSUMER_GROUP is required")
	}
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:  cfg.KafkaBrokers,
		GroupID:  groupID,
		Topic:    topic,
		MinBytes: 1e3,
		MaxBytes: 10e6,
	})
	return reader, nil
}

func maxInt(a int, b int) int {
	if a > b {
		return a
	}
	return b
}
