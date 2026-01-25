package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/segmentio/kafka-go"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"swarm-agv-arm-scheduling-system/shared/config"
	"swarm-agv-arm-scheduling-system/shared/httpx"
	"swarm-agv-arm-scheduling-system/shared/logx"
	"swarm-agv-arm-scheduling-system/shared/metricsx"
	"swarm-agv-arm-scheduling-system/shared/observability"
)

type statusResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
	Env     string `json:"env,omitempty"`
	Version string `json:"version,omitempty"`
}

func main() {
	cfg, readyProblems := config.Load("bridge", 8091)
	version := strings.TrimSpace(os.Getenv("VERSION"))
	logger := logx.New(cfg.ServiceName, cfg.Env, version, cfg.LogLevel)
	metricsx.Register()

	var shutdownTracer func(context.Context) error
	if cfg.OtelEnabled {
		var err error
		shutdownTracer, err = observability.InitTracer(context.Background(), observability.TracerConfig{
			ServiceName: cfg.ServiceName,
			Env:         cfg.Env,
			Endpoint:    cfg.OtelEndpoint,
			Insecure:    cfg.OtelInsecure,
			SampleRatio: cfg.OtelSampleRatio,
		})
		if err != nil {
			logger.Error(context.Background(), "otel_init_failed", "otel init failed",
				slog.String("error_code", "FAILED_PRECONDITION"),
				slog.String("error", err.Error()),
			)
		}
	}

	if len(cfg.BridgeSourceBrokers) == 0 {
		readyProblems = append(readyProblems, config.Problem{Field: "BRIDGE_SOURCE_BROKERS", Message: "BRIDGE_SOURCE_BROKERS is required"})
	}
	if len(cfg.BridgeTargetBrokers) == 0 {
		readyProblems = append(readyProblems, config.Problem{Field: "BRIDGE_TARGET_BROKERS", Message: "BRIDGE_TARGET_BROKERS is required"})
	}
	if len(cfg.BridgeTopics) == 0 {
		readyProblems = append(readyProblems, config.Problem{Field: "BRIDGE_TOPICS", Message: "BRIDGE_TOPICS is required"})
	}
	if strings.TrimSpace(cfg.BridgeGroupID) == "" {
		readyProblems = append(readyProblems, config.Problem{Field: "BRIDGE_GROUP_ID", Message: "BRIDGE_GROUP_ID is required"})
	}

	writer := kafka.NewWriter(kafka.WriterConfig{
		Brokers:      cfg.BridgeTargetBrokers,
		BatchTimeout: 2 * time.Second,
		Balancer:     &kafka.LeastBytes{},
		RequiredAcks: int(kafka.RequireAll),
		Async:        false,
	})

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	for _, topic := range cfg.BridgeTopics {
		topic = strings.TrimSpace(topic)
		if topic == "" {
			continue
		}
		wg.Add(1)
		go func(t string) {
			defer wg.Done()
			runBridgeConsumer(ctx, logger, cfg, t, writer)
		}(topic)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, statusResponse{
			Status:  "ok",
			Service: cfg.ServiceName,
			Env:     cfg.Env,
			Version: version,
		})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if len(readyProblems) > 0 {
			httpx.WriteError(
				w,
				r,
				http.StatusServiceUnavailable,
				"FAILED_PRECONDITION",
				"service not ready: invalid configuration",
				map[string]any{"problems": readyProblems},
			)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, statusResponse{
			Status:  "ready",
			Service: cfg.ServiceName,
			Env:     cfg.Env,
			Version: version,
		})
	})
	mux.Handle("GET /metrics", metricsx.Handler())

	notFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "route not found", nil)
	})
	handler := httpx.WrapServeMux(mux, notFound)
	handler = httpx.WithTimeout(cfg.RequestTimeout, handler)
	handler = httpx.WithRequestID(handler)
	handler = httpx.WithRecover(logger, handler)
	handler = metricsx.Instrument(handler)
	handler = httpx.WithRequestLog(logger, httpx.RequestLogOptions{SkipPaths: map[string]bool{"/healthz": true, "/metrics": true}}, handler)
	handler = otelhttp.NewHandler(handler, "http")

	server := &http.Server{
		Addr:              net.JoinHostPort("", strconv.Itoa(cfg.HTTPPort)),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info(context.Background(), "service_start", "starting service",
			slog.String("addr", server.Addr),
			slog.Int("http_port", cfg.HTTPPort),
			slog.String("log_level", cfg.LogLevel),
			slog.Int("request_timeout_ms", cfg.RequestTimeoutMS),
			slog.Int("topics", len(cfg.BridgeTopics)),
		)
		errCh <- server.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		logger.Info(context.Background(), "shutdown_signal", "received signal", slog.String("signal", sig.String()))
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			logger.Error(context.Background(), "server_failed", "server failed",
				slog.String("error_code", "INTERNAL_ERROR"),
				slog.String("error", err.Error()),
			)
			os.Exit(1)
		}
	}

	cancel()
	wg.Wait()
	if err := server.Shutdown(context.Background()); err != nil {
		logger.Error(context.Background(), "shutdown_failed", "shutdown failed",
			slog.String("error_code", "INTERNAL_ERROR"),
			slog.String("error", err.Error()),
		)
	}
	if err := writer.Close(); err != nil {
		logger.Error(context.Background(), "bridge_writer_close_failed", "bridge writer close failed",
			slog.String("error_code", "INTERNAL_ERROR"),
			slog.String("error", err.Error()),
		)
	}
	if shutdownTracer != nil {
		_ = shutdownTracer(context.Background())
	}
	logger.Info(context.Background(), "service_stop", "service stopped")
}

func runBridgeConsumer(ctx context.Context, logger logx.Logger, cfg config.Config, topic string, writer *kafka.Writer) {
	reader := kafka.NewReader(kafka.ReaderConfig{
		Brokers:  cfg.BridgeSourceBrokers,
		GroupID:  cfg.BridgeGroupID,
		Topic:    topic,
		MinBytes: 1e3,
		MaxBytes: cfg.BridgeMaxBytes,
	})
	defer reader.Close()

	for {
		msg, err := reader.FetchMessage(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			logger.Error(ctx, "bridge_consume_failed", "failed to consume message",
				slog.String("error_code", "INTERNAL_ERROR"),
				slog.String("error", err.Error()),
				slog.String("topic", topic),
			)
			time.Sleep(500 * time.Millisecond)
			continue
		}

		out := kafka.Message{
			Topic:   topic,
			Key:     msg.Key,
			Value:   msg.Value,
			Headers: msg.Headers,
		}
		out.Headers = append(out.Headers, kafka.Header{Key: "bridge_timestamp", Value: []byte(time.Now().UTC().Format(time.RFC3339))})
		if err := writer.WriteMessages(ctx, out); err != nil {
			logger.Error(ctx, "bridge_publish_failed", "failed to publish message",
				slog.String("error_code", "FAILED_PRECONDITION"),
				slog.String("error", err.Error()),
				slog.String("topic", topic),
			)
			time.Sleep(500 * time.Millisecond)
			continue
		}
		if err := reader.CommitMessages(ctx, msg); err != nil {
			logger.Error(ctx, "bridge_commit_failed", "failed to commit message",
				slog.String("error_code", "INTERNAL_ERROR"),
				slog.String("error", err.Error()),
				slog.String("topic", topic),
			)
		}
	}
}
