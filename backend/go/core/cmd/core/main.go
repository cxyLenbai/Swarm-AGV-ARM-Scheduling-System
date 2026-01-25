package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"swarm-agv-arm-scheduling-system/core/internal/rmf"
	"swarm-agv-arm-scheduling-system/shared/config"
	"swarm-agv-arm-scheduling-system/shared/events"
	"swarm-agv-arm-scheduling-system/shared/httpx"
	"swarm-agv-arm-scheduling-system/shared/logx"
	"swarm-agv-arm-scheduling-system/shared/metricsx"
	"swarm-agv-arm-scheduling-system/shared/mqx"
	"swarm-agv-arm-scheduling-system/shared/observability"
	"swarm-agv-arm-scheduling-system/shared/workflow"
	"syscall"
	"time"

	"github.com/google/uuid"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type statusResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
	Env     string `json:"env,omitempty"`
	Version string `json:"version,omitempty"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	Details   any    `json:"details,omitempty"`
}

func main() {
	cfg, readyProblems := config.Load("core", 8081)
	serviceName := cfg.ServiceName
	envName := cfg.Env
	version := strings.TrimSpace(os.Getenv("VERSION"))
	logger := logx.New(cfg.ServiceName, cfg.Env, version, cfg.LogLevel)
	metricsx.Register()
	var err error
	var shutdownTracer func(context.Context) error
	if cfg.OtelEnabled {
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

	port, err := parsePort(strconv.Itoa(cfg.HTTPPort))
	if err != nil {
		log.Fatalf("invalid config: PORT=%q: %v", os.Getenv("PORT"), err)
	}

	var rmfClient *rmf.Client
	if cfg.RMFEnabled && cfg.RMFAPIURL != "" {
		client, err := rmf.NewClient(cfg.RMFAPIURL, cfg.RMFAPIToken, 5*time.Second)
		if err != nil {
			readyProblems = append(readyProblems, config.Problem{Field: "RMF_API_URL", Message: "failed to initialize rmf client"})
		} else {
			rmfClient = client
		}
	}

	var kafkaProducer *mqx.Producer
	if len(cfg.KafkaBrokers) > 0 {
		producer, err := mqx.NewProducer(cfg)
		if err != nil {
			readyProblems = append(readyProblems, config.Problem{Field: "KAFKA_BROKERS", Message: "failed to initialize kafka producer"})
		} else {
			kafkaProducer = producer
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, statusResponse{
			Status:  "ok",
			Service: serviceName,
			Env:     envName,
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
			Service: serviceName,
			Env:     envName,
			Version: version,
		})
	})
	mux.Handle("GET /metrics", metricsx.Handler())

	mux.HandleFunc("POST /api/v1/rmf/tasks", func(w http.ResponseWriter, r *http.Request) {
		if rmfClient == nil {
			httpx.WriteError(w, r, http.StatusFailedDependency, "FAILED_PRECONDITION", "rmf not configured", nil)
			return
		}
		var req rmf.TaskRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid json body", nil)
			return
		}
		resp, err := rmfClient.CreateTask(r.Context(), req)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadGateway, "INTERNAL_ERROR", "rmf create failed", nil)
			return
		}
		httpx.WriteJSON(w, http.StatusAccepted, resp)
	})

	mux.HandleFunc("POST /api/v1/rmf/tasks/{task_id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if rmfClient == nil {
			httpx.WriteError(w, r, http.StatusFailedDependency, "FAILED_PRECONDITION", "rmf not configured", nil)
			return
		}
		taskID := strings.TrimSpace(r.PathValue("task_id"))
		if taskID == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing task_id", nil)
			return
		}
		resp, err := rmfClient.CancelTask(r.Context(), taskID)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadGateway, "INTERNAL_ERROR", "rmf cancel failed", nil)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("POST /api/v1/rmf/status", func(w http.ResponseWriter, r *http.Request) {
		if kafkaProducer == nil {
			httpx.WriteError(w, r, http.StatusFailedDependency, "FAILED_PRECONDITION", "kafka not configured", nil)
			return
		}
		var req rmfStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid json body", nil)
			return
		}
		eventID := uuid.New()
		payload, _ := json.Marshal(req)
		envelope, _ := json.Marshal(events.Envelope{
			EventID:       eventID,
			TenantID:      req.TenantID,
			OccurredAt:    time.Now().UTC(),
			AggregateType: "task",
			AggregateID:   req.TaskID,
			EventType:     "rmf_status",
			Payload:       payload,
		})
		if err := kafkaProducer.Publish(r.Context(), events.TopicTaskEvents, []byte(req.TaskID.String()), envelope, map[string]string{
			"tenant_id": req.TenantID.String(),
			"event_id":  eventID.String(),
		}); err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to publish status", nil)
			return
		}
		httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"event_id": eventID})
	})

	mux.HandleFunc("POST /api/v1/scheduler/global-assign", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		var req globalAssignRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid json body", nil)
			return
		}
		assignments, loads, err := globalAssign(req)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
			return
		}
		resp := globalAssignResponse{
			TenantID:       req.TenantID,
			Assignments:    assignments,
			WarehouseLoads: loads,
		}
		if kafkaProducer != nil {
			_ = publishDecision(r.Context(), kafkaProducer, req.TenantID, "global_assignment", resp)
		}
		metricsx.ObserveSchedulerDecisionLatency(time.Since(start))
		httpx.WriteJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("POST /api/v1/scheduler/local-schedule", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		var req localScheduleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid json body", nil)
			return
		}
		resp, err := localSchedule(req)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
			return
		}
		if kafkaProducer != nil {
			_ = publishDecision(r.Context(), kafkaProducer, req.TenantID, "local_schedule", resp)
		}
		metricsx.ObserveSchedulerDecisionLatency(time.Since(start))
		httpx.WriteJSON(w, http.StatusOK, resp)
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
		Addr:              net.JoinHostPort("", strconv.Itoa(port)),
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		taskStates := workflow.AllTaskStatuses()
		logger.Info(context.Background(), "service_start", "starting service",
			slog.String("addr", server.Addr),
			slog.Int("http_port", cfg.HTTPPort),
			slog.String("log_level", cfg.LogLevel),
			slog.Int("request_timeout_ms", cfg.RequestTimeoutMS),
			slog.Int("workflow_states", len(taskStates)),
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
			logger.Error(context.Background(), "server_failed", "server failed", slog.String("error_code", "INTERNAL_ERROR"), slog.String("error", err.Error()))
			os.Exit(1)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error(context.Background(), "shutdown_failed", "shutdown failed", slog.String("error_code", "INTERNAL_ERROR"), slog.String("error", err.Error()))
	}
	if kafkaProducer != nil {
		_ = kafkaProducer.Close()
	}
	if shutdownTracer != nil {
		_ = shutdownTracer(context.Background())
	}
	logger.Info(context.Background(), "service_stop", "service stopped")
}

func parsePort(raw string) (int, error) {
	p, err := strconv.Atoi(raw)
	if err != nil {
		return 0, err
	}
	if p <= 0 || p > 65535 {
		return 0, errors.New("port must be 1-65535")
	}
	return p, nil
}

func writeJSON(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, r *http.Request, statusCode int, code string, message string, details any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(errorEnvelope{
		Error: errorBody{
			Code:      code,
			Message:   message,
			RequestID: requestIDFromContext(r.Context()),
			Details:   details,
		},
	})
}

func withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(lrw, r)

		duration := time.Since(start)
		log.Printf("http_request request_id=%s method=%s path=%s status=%d duration_ms=%d remote=%s",
			requestIDFromContext(r.Context()),
			r.Method,
			r.URL.Path,
			lrw.statusCode,
			duration.Milliseconds(),
			r.RemoteAddr,
		)
	})
}

type requestIDKey struct{}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
		if requestID == "" {
			requestID = newRequestID()
		}
		w.Header().Set("X-Request-ID", requestID)

		ctx := context.WithValue(r.Context(), requestIDKey{}, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requestIDFromContext(ctx context.Context) string {
	if v := ctx.Value(requestIDKey{}); v != nil {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return hex.EncodeToString(b[:])
}

type rmfStatusRequest struct {
	TenantID uuid.UUID `json:"tenant_id"`
	TaskID   uuid.UUID `json:"task_id"`
	Status   string    `json:"status"`
	RobotID  string    `json:"robot_id,omitempty"`
}

type globalAssignRequest struct {
	TenantID uuid.UUID         `json:"tenant_id"`
	Tasks    []globalTaskInput `json:"tasks"`
}

type globalTaskInput struct {
	TaskID              string     `json:"task_id"`
	WarehouseID         string     `json:"warehouse_id,omitempty"`
	CandidateWarehouses []string   `json:"candidate_warehouses,omitempty"`
	Priority            int        `json:"priority,omitempty"`
	CreatedAt           *time.Time `json:"created_at,omitempty"`
}

type taskAssignment struct {
	TaskID      string `json:"task_id"`
	WarehouseID string `json:"warehouse_id"`
}

type globalAssignResponse struct {
	TenantID       uuid.UUID        `json:"tenant_id"`
	Assignments    []taskAssignment `json:"assignments"`
	WarehouseLoads map[string]int   `json:"warehouse_loads"`
}

type localScheduleRequest struct {
	TenantID    uuid.UUID        `json:"tenant_id"`
	WarehouseID string           `json:"warehouse_id"`
	Tasks       []localTaskInput `json:"tasks"`
	Robots      []robotInput     `json:"robots,omitempty"`
}

type localTaskInput struct {
	TaskID    string     `json:"task_id"`
	Priority  int        `json:"priority,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
}

type robotInput struct {
	RobotID string `json:"robot_id"`
}

type scheduledItem struct {
	TaskID   string `json:"task_id"`
	RobotID  string `json:"robot_id,omitempty"`
	Sequence int    `json:"sequence"`
}

type localScheduleResponse struct {
	TenantID    uuid.UUID       `json:"tenant_id"`
	WarehouseID string          `json:"warehouse_id"`
	Schedule    []scheduledItem `json:"schedule"`
}

func globalAssign(req globalAssignRequest) ([]taskAssignment, map[string]int, error) {
	if req.TenantID == uuid.Nil {
		return nil, nil, errors.New("tenant_id is required")
	}
	if len(req.Tasks) == 0 {
		return nil, nil, errors.New("tasks is required")
	}
	loads := map[string]int{}
	assignments := make([]taskAssignment, 0, len(req.Tasks))
	for _, task := range req.Tasks {
		taskID := strings.TrimSpace(task.TaskID)
		if taskID == "" {
			return nil, nil, errors.New("task_id is required")
		}
		warehouseID := strings.TrimSpace(task.WarehouseID)
		if warehouseID == "" {
			if len(task.CandidateWarehouses) == 0 {
				return nil, nil, errors.New("warehouse_id or candidate_warehouses is required")
			}
			warehouseID = chooseWarehouse(task.CandidateWarehouses, loads)
		}
		loads[warehouseID]++
		assignments = append(assignments, taskAssignment{
			TaskID:      taskID,
			WarehouseID: warehouseID,
		})
	}
	return assignments, loads, nil
}

func chooseWarehouse(candidates []string, loads map[string]int) string {
	var chosen string
	minLoad := int(^uint(0) >> 1)
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		load := loads[candidate]
		if chosen == "" || load < minLoad {
			chosen = candidate
			minLoad = load
		}
	}
	return chosen
}

func localSchedule(req localScheduleRequest) (localScheduleResponse, error) {
	if req.TenantID == uuid.Nil {
		return localScheduleResponse{}, errors.New("tenant_id is required")
	}
	warehouseID := strings.TrimSpace(req.WarehouseID)
	if warehouseID == "" {
		return localScheduleResponse{}, errors.New("warehouse_id is required")
	}
	if len(req.Tasks) == 0 {
		return localScheduleResponse{}, errors.New("tasks is required")
	}
	tasks := make([]localTaskInput, 0, len(req.Tasks))
	for _, t := range req.Tasks {
		if strings.TrimSpace(t.TaskID) == "" {
			return localScheduleResponse{}, errors.New("task_id is required")
		}
		tasks = append(tasks, t)
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		if tasks[i].Priority != tasks[j].Priority {
			return tasks[i].Priority > tasks[j].Priority
		}
		ti := time.Time{}
		tj := time.Time{}
		if tasks[i].CreatedAt != nil {
			ti = tasks[i].CreatedAt.UTC()
		}
		if tasks[j].CreatedAt != nil {
			tj = tasks[j].CreatedAt.UTC()
		}
		return ti.Before(tj)
	})

	robots := make([]string, 0, len(req.Robots))
	for _, robot := range req.Robots {
		if id := strings.TrimSpace(robot.RobotID); id != "" {
			robots = append(robots, id)
		}
	}
	schedule := make([]scheduledItem, 0, len(tasks))
	for idx, task := range tasks {
		item := scheduledItem{
			TaskID:   strings.TrimSpace(task.TaskID),
			Sequence: idx + 1,
		}
		if len(robots) > 0 {
			item.RobotID = robots[idx%len(robots)]
		}
		schedule = append(schedule, item)
	}
	return localScheduleResponse{
		TenantID:    req.TenantID,
		WarehouseID: warehouseID,
		Schedule:    schedule,
	}, nil
}

func publishDecision(ctx context.Context, producer *mqx.Producer, tenantID uuid.UUID, decisionType string, payload any) error {
	if producer == nil || tenantID == uuid.Nil {
		return nil
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	eventID := uuid.New()
	envelope, err := json.Marshal(events.Envelope{
		EventID:       eventID,
		TenantID:      tenantID,
		OccurredAt:    time.Now().UTC(),
		AggregateType: "scheduler",
		AggregateID:   uuid.New(),
		EventType:     decisionType,
		Payload:       body,
	})
	if err != nil {
		return err
	}
	headers := map[string]string{
		"tenant_id":     tenantID.String(),
		"event_id":      eventID.String(),
		"decision_type": decisionType,
	}
	return producer.Publish(ctx, events.TopicSchedulerDecisions, []byte(tenantID.String()), envelope, headers)
}

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *loggingResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}
