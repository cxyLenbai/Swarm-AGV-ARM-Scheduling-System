package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"swarm-agv-arm-scheduling-system/gateway/internal/routing"
	"swarm-agv-arm-scheduling-system/shared/config"
	"swarm-agv-arm-scheduling-system/shared/events"
	"swarm-agv-arm-scheduling-system/shared/httpx"
	"swarm-agv-arm-scheduling-system/shared/logx"
	"swarm-agv-arm-scheduling-system/shared/metricsx"
	"swarm-agv-arm-scheduling-system/shared/mqx"
	"swarm-agv-arm-scheduling-system/shared/observability"
)

const maxBodyBytes = 2 << 20

type statusResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
	Env     string `json:"env,omitempty"`
	Version string `json:"version,omitempty"`
}

type ingestRequest struct {
	TenantID      string          `json:"tenant_id"`
	WarehouseID   string          `json:"warehouse_id"`
	EventType     string          `json:"event_type"`
	Topic         string          `json:"topic,omitempty"`
	RobotID       string          `json:"robot_id,omitempty"`
	ZoneID        string          `json:"zone_id,omitempty"`
	AggregateType string          `json:"aggregate_type,omitempty"`
	AggregateID   string          `json:"aggregate_id,omitempty"`
	OccurredAt    *time.Time      `json:"occurred_at,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

type ingestResponse struct {
	EventID    string `json:"event_id"`
	Cluster    string `json:"cluster"`
	Topic      string `json:"topic"`
	Partition  string `json:"partition_key"`
	OccurredAt string `json:"occurred_at"`
}

func main() {
	cfg, readyProblems := config.Load("gateway", 8090)
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

	routesPath := strings.TrimSpace(os.Getenv("GATEWAY_ROUTES_PATH"))
	if routesPath == "" {
		if p, err := routing.DefaultRoutesPath(cfg.Env); err == nil {
			routesPath = p
		} else {
			readyProblems = append(readyProblems, config.Problem{Field: "GATEWAY_ROUTES_PATH", Message: "failed to resolve default routes path"})
		}
	}

	var resolver routing.Resolver
	if routesPath != "" {
		var err error
		resolver, err = routing.Load(routesPath)
		if err != nil {
			readyProblems = append(readyProblems, config.Problem{Field: "GATEWAY_ROUTES_PATH", Message: err.Error()})
		}
	} else {
		readyProblems = append(readyProblems, config.Problem{Field: "GATEWAY_ROUTES_PATH", Message: "routes config path is required"})
	}

	producers := map[string]*mqx.Producer{}
	if len(resolver.Config.Clusters) > 0 {
		for name, cluster := range resolver.Config.Clusters {
			clone := cfg
			clone.KafkaBrokers = cluster.Brokers
			if strings.TrimSpace(cluster.ClientID) != "" {
				clone.KafkaClientID = cluster.ClientID
			} else if strings.TrimSpace(cfg.ServiceName) != "" {
				clone.KafkaClientID = cfg.ServiceName + "-" + name
			}
			producer, err := mqx.NewProducer(clone)
			if err != nil {
				readyProblems = append(readyProblems, config.Problem{Field: "KAFKA_BROKERS", Message: fmt.Sprintf("cluster %s: %v", name, err)})
				continue
			}
			producers[name] = producer
		}
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

	mux.HandleFunc("POST /v1/ingest", func(w http.ResponseWriter, r *http.Request) {
		req, err := decodeIngestRequest(r)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
			return
		}
		clusterName, ok := resolver.ResolveCluster(req.TenantID, req.WarehouseID)
		if !ok {
			httpx.WriteError(w, r, http.StatusBadRequest, "FAILED_PRECONDITION", "no matching route for tenant/warehouse", nil)
			return
		}
		producer, ok := producers[clusterName]
		if !ok || producer == nil {
			httpx.WriteError(w, r, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "cluster producer unavailable", map[string]any{"cluster": clusterName})
			return
		}
		topic := resolver.ResolveTopic(req.EventType, req.Topic)
		if topic == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "topic is required", nil)
			return
		}
		partitionKey := buildPartitionKey(req)

		payload := req.Payload
		if len(payload) == 0 {
			payload = []byte("null")
		}
		envelope, err := buildEnvelope(req, payload)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
			return
		}
		data, err := json.Marshal(envelope)
		if err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to encode event", nil)
			return
		}

		headers := map[string]string{
			"tenant_id":    req.TenantID,
			"warehouse_id": req.WarehouseID,
			"event_type":   req.EventType,
			"cluster":      clusterName,
			"request_id":   httpx.RequestIDFromContext(r.Context()),
		}
		if req.RobotID != "" {
			headers["robot_id"] = req.RobotID
		}
		if req.ZoneID != "" {
			headers["zone_id"] = req.ZoneID
		}

		if err := producer.Publish(r.Context(), topic, []byte(partitionKey), data, headers); err != nil {
			httpx.WriteError(w, r, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "failed to publish event", nil)
			return
		}

		httpx.WriteJSON(w, http.StatusAccepted, ingestResponse{
			EventID:    envelope.EventID.String(),
			Cluster:    clusterName,
			Topic:      topic,
			Partition:  partitionKey,
			OccurredAt: envelope.OccurredAt.UTC().Format(time.RFC3339),
		})
	})

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
			slog.String("routes_path", routesPath),
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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error(context.Background(), "shutdown_failed", "shutdown failed",
			slog.String("error_code", "INTERNAL_ERROR"),
			slog.String("error", err.Error()),
		)
	}
	for _, producer := range producers {
		if producer != nil {
			_ = producer.Close()
		}
	}
	if shutdownTracer != nil {
		_ = shutdownTracer(context.Background())
	}
	logger.Info(context.Background(), "service_stop", "service stopped")
}

func decodeIngestRequest(r *http.Request) (ingestRequest, error) {
	if r.Body == nil {
		return ingestRequest{}, errors.New("request body required")
	}
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBodyBytes))
	var req ingestRequest
	if err := dec.Decode(&req); err != nil {
		return ingestRequest{}, errors.New("invalid json body")
	}
	req.TenantID = strings.TrimSpace(req.TenantID)
	req.WarehouseID = strings.TrimSpace(req.WarehouseID)
	req.EventType = strings.TrimSpace(req.EventType)
	req.Topic = strings.TrimSpace(req.Topic)
	req.RobotID = strings.TrimSpace(req.RobotID)
	req.ZoneID = strings.TrimSpace(req.ZoneID)
	req.AggregateType = strings.TrimSpace(req.AggregateType)
	req.AggregateID = strings.TrimSpace(req.AggregateID)

	if req.TenantID == "" {
		return ingestRequest{}, errors.New("tenant_id is required")
	}
	if req.WarehouseID == "" {
		return ingestRequest{}, errors.New("warehouse_id is required")
	}
	if req.EventType == "" {
		return ingestRequest{}, errors.New("event_type is required")
	}
	return req, nil
}

func buildPartitionKey(req ingestRequest) string {
	if req.RobotID != "" {
		return req.RobotID
	}
	if req.ZoneID != "" {
		return req.ZoneID
	}
	if req.WarehouseID != "" {
		return req.WarehouseID
	}
	return req.TenantID
}

func buildEnvelope(req ingestRequest, payload json.RawMessage) (events.Envelope, error) {
	tenantID, err := uuid.Parse(req.TenantID)
	if err != nil {
		return events.Envelope{}, errors.New("tenant_id must be a UUID")
	}
	aggregateType := req.AggregateType
	if aggregateType == "" {
		aggregateType = "gateway.event"
	}
	var aggregateID uuid.UUID
	if req.AggregateID != "" {
		aggregateID, err = uuid.Parse(req.AggregateID)
		if err != nil {
			return events.Envelope{}, errors.New("aggregate_id must be a UUID")
		}
	} else {
		aggregateID = uuid.New()
	}
	occurredAt := time.Now().UTC()
	if req.OccurredAt != nil {
		occurredAt = req.OccurredAt.UTC()
	}
	return events.Envelope{
		EventID:       uuid.New(),
		TenantID:      tenantID,
		OccurredAt:    occurredAt,
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		EventType:     req.EventType,
		Payload:       payload,
	}, nil
}
