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
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	"swarm-agv-arm-scheduling-system/api/internal/models"
	"swarm-agv-arm-scheduling-system/api/internal/repos"
	"swarm-agv-arm-scheduling-system/shared/cachex"
	"swarm-agv-arm-scheduling-system/shared/config"
	"swarm-agv-arm-scheduling-system/shared/dbx"
	"swarm-agv-arm-scheduling-system/shared/events"
	"swarm-agv-arm-scheduling-system/shared/influxx"
	"swarm-agv-arm-scheduling-system/shared/logx"
	"swarm-agv-arm-scheduling-system/shared/metricsx"
	"swarm-agv-arm-scheduling-system/shared/mqx"
	"swarm-agv-arm-scheduling-system/shared/observability"
)

const (
	alertChannel = "congestion.alerts"
)

type zoneWindow struct {
	events    []time.Time
	lastSpeed float64
}

func main() {
	cfg, problems := config.Load("congestion-worker", 8084)
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

	congestionRepo := repos.NewCongestionRepo(dbPool)
	producer, err := mqx.NewProducer(cfg)
	if err != nil {
		logger.Error(context.Background(), "kafka_init_failed", "kafka producer init failed",
			slog.String("error_code", "FAILED_PRECONDITION"),
			slog.String("error", err.Error()),
		)
		os.Exit(1)
	}
	defer producer.Close()

	var cacheClient *cachex.Client
	if cfg.RedisAddr != "" {
		cacheClient, err = cachex.New(cfg)
		if err != nil {
			logger.Warn(context.Background(), "redis_init_failed", "redis init failed",
				slog.String("error_code", "FAILED_PRECONDITION"),
				slog.String("error", err.Error()),
			)
		}
	}
	if cacheClient != nil {
		defer cacheClient.Close()
	}

	var influxClient *influxx.Client
	if cfg.InfluxURL != "" && cfg.InfluxToken != "" && cfg.InfluxOrg != "" && cfg.InfluxBucket != "" {
		influxClient, err = influxx.New(cfg)
		if err != nil {
			logger.Warn(context.Background(), "influx_init_failed", "influx init failed",
				slog.String("error_code", "FAILED_PRECONDITION"),
				slog.String("error", err.Error()),
			)
		}
	}
	if influxClient != nil {
		defer influxClient.Close()
	}

	robotReader, err := mqx.NewConsumer(cfg, events.TopicRobotStatus, cfg.KafkaGroupID+"-robot")
	if err != nil {
		logger.Error(context.Background(), "kafka_init_failed", "robot reader init failed",
			slog.String("error_code", "FAILED_PRECONDITION"),
			slog.String("error", err.Error()),
		)
		os.Exit(1)
	}
	defer robotReader.Close()

	taskReader, err := mqx.NewConsumer(cfg, events.TopicTaskEvents, cfg.KafkaGroupID+"-task")
	if err != nil {
		logger.Error(context.Background(), "kafka_init_failed", "task reader init failed",
			slog.String("error_code", "FAILED_PRECONDITION"),
			slog.String("error", err.Error()),
		)
		os.Exit(1)
	}
	defer taskReader.Close()

	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	state := &aggState{
		window:     make(map[string]*zoneWindow),
		lastAlert:  make(map[string]time.Time),
		cooldown:   time.Duration(cfg.CongestionCooldownSec) * time.Second,
		thresholds: []float64{cfg.CongestionL1, cfg.CongestionL2, cfg.CongestionL3},
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		runConsumer(ctx, robotReader, cfg.KafkaGroupID+"-robot", func(payload []byte) error {
			return handleRobotStatus(ctx, payload, state, congestionRepo, producer, cacheClient, influxClient, logger)
		}, logger)
	}()
	go func() {
		defer wg.Done()
		runConsumer(ctx, taskReader, cfg.KafkaGroupID+"-task", func(payload []byte) error {
			return handleTaskEvent(ctx, payload, state, congestionRepo, producer, cacheClient, logger)
		}, logger)
	}()

	wg.Wait()
	logger.Info(context.Background(), "worker_stop", "congestion worker stopped")
}

type aggState struct {
	mu         sync.Mutex
	window     map[string]*zoneWindow
	lastAlert  map[string]time.Time
	cooldown   time.Duration
	thresholds []float64
}

func runConsumer(ctx context.Context, reader *kafka.Reader, groupID string, handler func([]byte) error, logger logx.Logger) {
	for {
		msg, err := reader.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			logger.Error(ctx, "kafka_fetch_failed", "failed to fetch message",
				slog.String("error_code", "INTERNAL_ERROR"),
				slog.String("error", err.Error()),
			)
			time.Sleep(500 * time.Millisecond)
			continue
		}
		_, span := otel.Tracer("mqx").Start(ctx, "kafka.consume")
		span.SetAttributes(
			attribute.String("messaging.system", "kafka"),
			attribute.String("messaging.destination", msg.Topic),
		)
		if err := handler(msg.Value); err != nil {
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
		metricsx.SetKafkaLag(stats.Topic, groupID, stats.Lag)
	}
}

func handleRobotStatus(ctx context.Context, payload []byte, state *aggState, repo *repos.CongestionRepo, producer *mqx.Producer, cacheClient *cachex.Client, influxClient *influxx.Client, logger logx.Logger) error {
	var envelope events.Envelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	zoneID, speed, ok := extractZoneFromPayload(envelope.Payload)
	if !ok || zoneID == "" {
		return nil
	}
	tenantID := envelope.TenantID
	now := time.Now().UTC()

	snapshot, level := state.update(zoneID, speed, now)
	snapshot.TenantID = tenantID
	zoneUUID, err := uuid.Parse(zoneID)
	if err != nil {
		return nil
	}
	snapshot.ZoneID = zoneUUID
	if err := repo.EnsureZone(ctx, tenantID, zoneUUID, zoneID); err != nil {
		return err
	}
	if _, err := repo.UpsertZoneSnapshot(ctx, snapshot); err != nil {
		return err
	}
	if influxClient != nil {
		_ = influxClient.WritePoint(ctx, "zone_congestion", map[string]string{
			"tenant_id": tenantID.String(),
			"zone_id":   zoneID,
		}, map[string]any{
			"congestion_index": snapshot.CongestionIndex,
			"avg_speed":        snapshot.AvgSpeed,
			"queue_length":     snapshot.QueueLength,
			"risk":             snapshot.Risk,
			"confidence":       snapshot.Confidence,
		}, now)
	}
	if level == "" {
		return nil
	}
	alert, shouldEmit := state.alertIfNeeded(zoneID, snapshot, level, now)
	if !shouldEmit {
		return nil
	}
	alert.TenantID = tenantID
	alert.ZoneID = snapshot.ZoneID
	alert, err = repo.CreateAlert(ctx, alert)
	if err != nil {
		return err
	}

	metricsPayload, _ := json.Marshal(map[string]any{
		"zone_id":          zoneID,
		"congestion_index": snapshot.CongestionIndex,
		"avg_speed":        snapshot.AvgSpeed,
		"queue_length":     snapshot.QueueLength,
		"risk":             snapshot.Risk,
		"confidence":       snapshot.Confidence,
	})
	_ = producer.Publish(ctx, events.TopicCongestionMetrics, []byte(zoneID), metricsPayload, map[string]string{
		"tenant_id": tenantID.String(),
	})

	alertPayload, _ := json.Marshal(map[string]any{
		"alert_id":         alert.AlertID,
		"zone_id":          zoneID,
		"level":            alert.Level,
		"congestion_index": alert.CongestionIndex,
		"risk":             alert.Risk,
		"confidence":       alert.Confidence,
		"detected_at":      alert.DetectedAt,
	})
	_ = producer.Publish(ctx, events.TopicAlerts, []byte(alert.AlertID.String()), alertPayload, map[string]string{
		"tenant_id": tenantID.String(),
	})
	if cacheClient != nil {
		payload := tenantID.String() + ":" + string(alertPayload)
		_ = cacheClient.Client().Publish(ctx, alertChannel, payload).Err()
	}
	logger.Info(ctx, "congestion_alert", "congestion alert emitted",
		slog.String("tenant_id", tenantID.String()),
		slog.String("zone_id", zoneID),
		slog.String("level", alert.Level),
	)
	return nil
}

func handleTaskEvent(ctx context.Context, payload []byte, state *aggState, repo *repos.CongestionRepo, producer *mqx.Producer, cacheClient *cachex.Client, logger logx.Logger) error {
	var envelope events.Envelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return err
	}
	zoneID, speed, ok := extractZoneFromPayload(envelope.Payload)
	if !ok || zoneID == "" {
		return nil
	}
	now := time.Now().UTC()
	snapshot, level := state.update(zoneID, speed, now)
	snapshot.TenantID = envelope.TenantID
	zoneUUID, err := uuid.Parse(zoneID)
	if err != nil {
		return nil
	}
	snapshot.ZoneID = zoneUUID
	if err := repo.EnsureZone(ctx, envelope.TenantID, zoneUUID, zoneID); err != nil {
		return err
	}
	if _, err := repo.UpsertZoneSnapshot(ctx, snapshot); err != nil {
		return err
	}
	if level == "" {
		return nil
	}
	alert, shouldEmit := state.alertIfNeeded(zoneID, snapshot, level, now)
	if !shouldEmit {
		return nil
	}
	alert.TenantID = envelope.TenantID
	alert.ZoneID = snapshot.ZoneID
	alert, err = repo.CreateAlert(ctx, alert)
	if err != nil {
		return err
	}
	alertPayload, _ := json.Marshal(map[string]any{
		"alert_id":         alert.AlertID,
		"zone_id":          zoneID,
		"level":            alert.Level,
		"congestion_index": alert.CongestionIndex,
		"risk":             alert.Risk,
		"confidence":       alert.Confidence,
		"detected_at":      alert.DetectedAt,
	})
	_ = producer.Publish(ctx, events.TopicAlerts, []byte(alert.AlertID.String()), alertPayload, map[string]string{
		"tenant_id": envelope.TenantID.String(),
	})
	if cacheClient != nil {
		payload := envelope.TenantID.String() + ":" + string(alertPayload)
		_ = cacheClient.Client().Publish(ctx, alertChannel, payload).Err()
	}
	logger.Info(ctx, "congestion_alert", "congestion alert emitted",
		slog.String("tenant_id", envelope.TenantID.String()),
		slog.String("zone_id", zoneID),
		slog.String("level", alert.Level),
	)
	return nil
}

func (s *aggState) update(zoneID string, speed float64, now time.Time) (models.ZoneCongestionSnapshot, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	window := s.window[zoneID]
	if window == nil {
		window = &zoneWindow{}
		s.window[zoneID] = window
	}
	window.events = append(window.events, now)
	cutoff := now.Add(-1 * time.Minute)
	idx := 0
	for _, ts := range window.events {
		if ts.After(cutoff) {
			window.events[idx] = ts
			idx++
		}
	}
	window.events = window.events[:idx]
	if speed > 0 {
		window.lastSpeed = speed
	}
	queueLength := float64(len(window.events))
	congestionIndex := clamp(queueLength/10.0, 0, 1)
	risk := clamp(congestionIndex, 0, 1)
	confidence := clamp(0.5+0.5*congestionIndex, 0, 1)

	level := ""
	if congestionIndex >= s.thresholds[2] {
		level = "L3"
	} else if congestionIndex >= s.thresholds[1] {
		level = "L2"
	} else if congestionIndex >= s.thresholds[0] {
		level = "L1"
	}

	return models.ZoneCongestionSnapshot{
		CongestionIndex: congestionIndex,
		AvgSpeed:        window.lastSpeed,
		QueueLength:     queueLength,
		Risk:            risk,
		Confidence:      confidence,
		UpdatedAt:       now,
	}, level
}

func (s *aggState) alertIfNeeded(zoneID string, snapshot models.ZoneCongestionSnapshot, level string, now time.Time) (models.CongestionAlert, bool) {
	last := s.lastAlert[zoneID]
	if !last.IsZero() && now.Sub(last) < s.cooldown {
		return models.CongestionAlert{}, false
	}
	s.lastAlert[zoneID] = now
	message := "congestion level " + level
	return models.CongestionAlert{
		Level:           level,
		CongestionIndex: snapshot.CongestionIndex,
		Risk:            snapshot.Risk,
		Confidence:      snapshot.Confidence,
		DetectedAt:      now,
		Message:         &message,
		Details:         []byte("{}"),
	}, true
}

func extractZoneFromPayload(raw json.RawMessage) (string, float64, bool) {
	if len(raw) == 0 {
		return "", 0, false
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", 0, false
	}
	var zoneID string
	if meta, ok := payload["meta"].(map[string]any); ok {
		zoneID = asString(meta["zone_id"])
	}
	if zoneID == "" {
		zoneID = asString(payload["zone_id"])
	}
	speed := asFloat(payload["speed"])
	return zoneID, speed, zoneID != ""
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	default:
		return ""
	}
}

func asFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f
	default:
		return 0
	}
}

func clamp(v float64, min float64, max float64) float64 {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
