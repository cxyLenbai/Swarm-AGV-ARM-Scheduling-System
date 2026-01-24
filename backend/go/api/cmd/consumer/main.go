package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"swarm-agv-arm-scheduling-system/api/internal/models"
	"swarm-agv-arm-scheduling-system/api/internal/repos"
	"swarm-agv-arm-scheduling-system/shared/config"
	"swarm-agv-arm-scheduling-system/shared/dbx"
	"swarm-agv-arm-scheduling-system/shared/events"
	"swarm-agv-arm-scheduling-system/shared/logx"
	"swarm-agv-arm-scheduling-system/shared/metricsx"
	"swarm-agv-arm-scheduling-system/shared/mqx"
	"swarm-agv-arm-scheduling-system/shared/observability"
)

func main() {
	cfg, problems := config.Load("task-events-consumer", 8082)
	version := strings.TrimSpace(os.Getenv("VERSION"))
	logger := logx.New(cfg.ServiceName, cfg.Env, version, cfg.LogLevel)

	if cfg.DatabaseURL == "" {
		problems = append(problems, config.Problem{Field: "DATABASE_URL", Message: "DATABASE_URL is required"})
	}
	if len(cfg.KafkaBrokers) == 0 {
		problems = append(problems, config.Problem{Field: "KAFKA_BROKERS", Message: "KAFKA_BROKERS is required"})
	}
	if cfg.KafkaGroupID == "" {
		problems = append(problems, config.Problem{Field: "KAFKA_CONSUMER_GROUP", Message: "KAFKA_CONSUMER_GROUP is required"})
	}
	if len(problems) > 0 {
		logger.Error(context.Background(), "config_invalid", "invalid config",
			slog.String("error_code", "FAILED_PRECONDITION"),
			slog.Any("problems", problems),
		)
		os.Exit(1)
	}

	if cfg.OtelEnabled {
		if shutdown, err := observability.InitTracer(context.Background(), observability.TracerConfig{
			ServiceName: cfg.ServiceName,
			Env:         cfg.Env,
			Endpoint:    cfg.OtelEndpoint,
			Insecure:    cfg.OtelInsecure,
			SampleRatio: cfg.OtelSampleRatio,
		}); err == nil {
			defer func() { _ = shutdown(context.Background()) }()
		}
	}

	dbPool, err := dbx.NewPool(cfg)
	if err != nil {
		logger.Error(context.Background(), "db_init_failed", "db init failed",
			slog.String("error_code", "FAILED_PRECONDITION"),
			slog.String("error", err.Error()),
		)
		os.Exit(1)
	}
	defer dbPool.Close()

	reader, err := mqx.NewConsumer(cfg, events.TopicTaskEvents, cfg.KafkaGroupID)
	if err != nil {
		logger.Error(context.Background(), "kafka_init_failed", "kafka reader init failed",
			slog.String("error_code", "FAILED_PRECONDITION"),
			slog.String("error", err.Error()),
		)
		os.Exit(1)
	}
	defer reader.Close()

	tasksRepo := repos.NewTasksRepo(dbPool)

	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	logger.Info(ctx, "consumer_start", "task events consumer started",
		slog.String("topic", events.TopicTaskEvents),
		slog.String("group", cfg.KafkaGroupID),
	)

	for {
		msg, err := reader.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				break
			}
			logger.Error(ctx, "kafka_fetch_failed", "failed to fetch message",
				slog.String("error_code", "INTERNAL_ERROR"),
				slog.String("error", err.Error()),
			)
			time.Sleep(500 * time.Millisecond)
			continue
		}

		spanCtx, span := otel.Tracer("mqx").Start(ctx, "kafka.consume")
		span.SetAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.destination", events.TopicTaskEvents),
		)
		if err := handleTaskEvent(spanCtx, tasksRepo, msg.Value); err != nil {
			span.End()
			logger.Error(ctx, "event_handle_failed", "failed to handle event",
				slog.String("error_code", "INTERNAL_ERROR"),
				slog.String("error", err.Error()),
			)
			continue
		}
		span.End()
		if err := reader.CommitMessages(ctx, msg); err != nil {
			logger.Error(ctx, "kafka_commit_failed", "failed to commit message",
				slog.String("error_code", "INTERNAL_ERROR"),
				slog.String("error", err.Error()),
			)
		}
		stats := reader.Stats()
		metricsx.SetKafkaLag(stats.Topic, cfg.KafkaGroupID, stats.Lag)
	}

	logger.Info(context.Background(), "consumer_stop", "task events consumer stopped")
}

func handleTaskEvent(ctx context.Context, tasksRepo *repos.TasksRepo, payload []byte) error {
	var envelope events.Envelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	if envelope.EventID == uuid.Nil || envelope.TenantID == uuid.Nil || envelope.AggregateID == uuid.Nil {
		return errors.New("missing event_id/tenant_id/aggregate_id")
	}
	event := models.TaskEvent{
		EventID:    envelope.EventID,
		TenantID:   envelope.TenantID,
		TaskID:     envelope.AggregateID,
		EventType:  envelope.EventType,
		OccurredAt: envelope.OccurredAt,
		Payload:    envelope.Payload,
	}
	_, err := tasksRepo.InsertTaskEventFromStream(ctx, event)
	return err
}
