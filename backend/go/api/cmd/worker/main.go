package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"swarm-agv-arm-scheduling-system/api/internal/repos"
	"swarm-agv-arm-scheduling-system/shared/config"
	"swarm-agv-arm-scheduling-system/shared/dbx"
	"swarm-agv-arm-scheduling-system/shared/logx"
	"swarm-agv-arm-scheduling-system/shared/metricsx"
	"swarm-agv-arm-scheduling-system/shared/mqx"
	"swarm-agv-arm-scheduling-system/shared/observability"
)

const (
	taskOutboxScan     = "outbox.scan"
	taskOutboxDispatch = "outbox.dispatch"
)

type dispatchPayload struct {
	EventID string `json:"event_id"`
}

func main() {
	cfg, problems := config.Load("outbox-worker", 8083)
	version := strings.TrimSpace(os.Getenv("VERSION"))
	logger := logx.New(cfg.ServiceName, cfg.Env, version, cfg.LogLevel)

	if cfg.DatabaseURL == "" {
		problems = append(problems, config.Problem{Field: "DATABASE_URL", Message: "DATABASE_URL is required"})
	}
	if cfg.AsynqRedisAddr == "" {
		problems = append(problems, config.Problem{Field: "ASYNQ_REDIS_ADDR", Message: "ASYNQ_REDIS_ADDR is required"})
	}
	if len(cfg.KafkaBrokers) == 0 {
		problems = append(problems, config.Problem{Field: "KAFKA_BROKERS", Message: "KAFKA_BROKERS is required"})
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

	outboxRepo := repos.NewOutboxRepo(dbPool)
	producer, err := mqx.NewProducer(cfg)
	if err != nil {
		logger.Error(context.Background(), "kafka_init_failed", "kafka producer init failed",
			slog.String("error_code", "FAILED_PRECONDITION"),
			slog.String("error", err.Error()),
		)
		os.Exit(1)
	}
	defer producer.Close()

	redisOpt := asynq.RedisClientOpt{
		Addr:     cfg.AsynqRedisAddr,
		Password: cfg.AsynqRedisPass,
		DB:       cfg.AsynqRedisDB,
	}
	server := asynq.NewServer(redisOpt, asynq.Config{
		Concurrency: cfg.AsynqConcurrency,
		Queues: map[string]int{
			cfg.AsynqQueue: 1,
		},
	})
	defer server.Shutdown()

	mux := asynq.NewServeMux()
	mux.HandleFunc(taskOutboxScan, func(ctx context.Context, t *asynq.Task) error {
		events, err := outboxRepo.ClaimPending(ctx, cfg.ServiceName, cfg.OutboxBatchSize)
		if err != nil {
			return err
		}
		client := asynq.NewClient(redisOpt)
		defer client.Close()
		for _, event := range events {
			payload, _ := json.Marshal(dispatchPayload{EventID: event.EventID.String()})
			task := asynq.NewTask(taskOutboxDispatch, payload, asynq.Queue(cfg.AsynqQueue))
			if _, err := client.Enqueue(task); err != nil {
				logger.Error(ctx, "enqueue_failed", "failed to enqueue outbox dispatch",
					slog.String("error_code", "INTERNAL_ERROR"),
					slog.String("error", err.Error()),
				)
				attempts := event.Attempts + 1
				nextRetry := time.Now().UTC().Add(retryDelay(attempts))
				_ = outboxRepo.MarkFailed(ctx, event.EventID, attempts, &nextRetry, err.Error(), attempts >= cfg.OutboxMaxAttempts)
			}
		}
		return nil
	})
	mux.HandleFunc(taskOutboxDispatch, func(ctx context.Context, t *asynq.Task) error {
		ctx, span := otel.Tracer("asynq").Start(ctx, "outbox.dispatch")
		span.SetAttributes(attribute.String("queue", cfg.AsynqQueue))
		defer span.End()
		var payload dispatchPayload
		if err := json.Unmarshal(t.Payload(), &payload); err != nil {
			return err
		}
		eventID, err := uuid.Parse(strings.TrimSpace(payload.EventID))
		if err != nil {
			return err
		}
		event, err := outboxRepo.GetByID(ctx, eventID)
		if err != nil {
			return err
		}
		if event.Status == repos.OutboxStatusDelivered || event.Status == repos.OutboxStatusDead {
			return nil
		}
		headers := map[string]string{
			"event_id":       event.EventID.String(),
			"tenant_id":      event.TenantID.String(),
			"aggregate_type": event.AggregateType,
			"aggregate_id":   event.AggregateID.String(),
			"published_at":   time.Now().UTC().Format(time.RFC3339Nano),
		}
		if err := producer.Publish(ctx, event.Topic, []byte(event.AggregateID.String()), event.Payload, headers); err != nil {
			attempts := event.Attempts + 1
			nextRetry := time.Now().UTC().Add(retryDelay(attempts))
			dead := attempts >= cfg.OutboxMaxAttempts
			_ = outboxRepo.MarkFailed(ctx, event.EventID, attempts, &nextRetry, err.Error(), dead)
			if dead {
				logger.Warn(ctx, "outbox_dead", "outbox event moved to dead-letter",
					slog.String("event_id", event.EventID.String()),
					slog.Int("attempts", attempts),
				)
				return nil
			}
			return err
		}
		if err := outboxRepo.MarkDelivered(ctx, event.EventID); err != nil {
			return err
		}
		return nil
	})

	scheduler := asynq.NewScheduler(redisOpt, &asynq.SchedulerOpts{
		Location: time.UTC,
	})
	defer scheduler.Shutdown()
	inspector := asynq.NewInspector(redisOpt)
	defer inspector.Close()
	if _, err := scheduler.Register("@every "+strconv.Itoa(cfg.OutboxScanSec)+"s", asynq.NewTask(taskOutboxScan, nil, asynq.Queue(cfg.AsynqQueue))); err != nil {
		logger.Error(context.Background(), "scheduler_init_failed", "scheduler init failed",
			slog.String("error_code", "FAILED_PRECONDITION"),
			slog.String("error", err.Error()),
		)
		os.Exit(1)
	}
	if err := scheduler.Start(); err != nil {
		logger.Error(context.Background(), "scheduler_start_failed", "scheduler start failed",
			slog.String("error_code", "INTERNAL_ERROR"),
			slog.String("error", err.Error()),
		)
		os.Exit(1)
	}

	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			info, err := inspector.GetQueueInfo(cfg.AsynqQueue)
			if err != nil {
				continue
			}
			metricsx.SetAsynqQueueDepth(cfg.AsynqQueue, info.Size)
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		logger.Info(context.Background(), "worker_start", "outbox worker started",
			slog.String("queue", cfg.AsynqQueue),
			slog.Int("concurrency", cfg.AsynqConcurrency),
		)
		errCh <- server.Run(mux)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-sigCh:
		logger.Info(context.Background(), "shutdown_signal", "received signal", slog.String("signal", sig.String()))
	case err := <-errCh:
		if !errors.Is(err, asynq.ErrServerClosed) {
			logger.Error(context.Background(), "worker_failed", "worker failed",
				slog.String("error_code", "INTERNAL_ERROR"),
				slog.String("error", err.Error()),
			)
			os.Exit(1)
		}
	}

	logger.Info(context.Background(), "worker_stop", "outbox worker stopped")
}

func retryDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return 5 * time.Second
	}
	delay := time.Duration(attempt*attempt) * 5 * time.Second
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}
