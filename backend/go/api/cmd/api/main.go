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
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"swarm-agv-arm-scheduling-system/api/internal/middleware"
	"swarm-agv-arm-scheduling-system/api/internal/models"
	"swarm-agv-arm-scheduling-system/api/internal/repos"
	"swarm-agv-arm-scheduling-system/shared/authx"
	"swarm-agv-arm-scheduling-system/shared/cachex"
	"swarm-agv-arm-scheduling-system/shared/clients/ai"
	"swarm-agv-arm-scheduling-system/shared/config"
	"swarm-agv-arm-scheduling-system/shared/dbx"
	"swarm-agv-arm-scheduling-system/shared/events"
	"swarm-agv-arm-scheduling-system/shared/httpx"
	"swarm-agv-arm-scheduling-system/shared/influxx"
	"swarm-agv-arm-scheduling-system/shared/lockx"
	"swarm-agv-arm-scheduling-system/shared/logx"
	"swarm-agv-arm-scheduling-system/shared/metricsx"
	"swarm-agv-arm-scheduling-system/shared/observability"
	"swarm-agv-arm-scheduling-system/shared/tenantx"
	"swarm-agv-arm-scheduling-system/shared/workflow"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

type statusResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
	Env     string `json:"env,omitempty"`
	Version string `json:"version,omitempty"`
}

func main() {
	cfg, readyProblems := config.Load("api", 8080)
	version := strings.TrimSpace(os.Getenv("VERSION"))
	logger := logx.New(cfg.ServiceName, cfg.Env, version, cfg.LogLevel)
	var err error
	metricsx.Register()
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

	if cfg.DatabaseURL == "" {
		readyProblems = append(readyProblems, config.Problem{Field: "DATABASE_URL", Message: "DATABASE_URL is required"})
	}

	var dbPool *pgxpool.Pool
	if cfg.DatabaseURL != "" {
		var err error
		dbPool, err = dbx.NewPool(cfg)
		if err != nil {
			readyProblems = append(readyProblems, config.Problem{Field: "DATABASE_URL", Message: "failed to connect to database"})
			logger.Error(context.Background(), "db_init_failed", "database init failed",
				slog.String("error_code", "FAILED_PRECONDITION"),
				slog.String("error", err.Error()),
			)
		}
	}

	tenantsRepo := repos.NewTenantsRepo(dbPool)
	auditRepo := repos.NewAuditRepo(dbPool)
	robotsRepo := repos.NewRobotsRepo(dbPool)
	robotStatusRepo := repos.NewRobotStatusRepo(dbPool)
	tasksRepo := repos.NewTasksRepo(dbPool)
	outboxRepo := repos.NewOutboxRepo(dbPool)
	congestionRepo := repos.NewCongestionRepo(dbPool)

	var cacheClient *cachex.Client
	if cfg.RedisAddr != "" {
		cacheClient, err = cachex.New(cfg)
		if err != nil {
			readyProblems = append(readyProblems, config.Problem{Field: "REDIS_ADDR", Message: "failed to initialize redis client"})
			logger.Error(context.Background(), "redis_init_failed", "redis init failed",
				slog.String("error_code", "FAILED_PRECONDITION"),
				slog.String("error", err.Error()),
			)
		}
	}

	var influxClient *influxx.Client
	if cfg.InfluxURL != "" || cfg.InfluxToken != "" || cfg.InfluxOrg != "" || cfg.InfluxBucket != "" {
		influxClient, err = influxx.New(cfg)
		if err != nil {
			readyProblems = append(readyProblems, config.Problem{Field: "INFLUX_URL", Message: "failed to initialize influx client"})
			logger.Error(context.Background(), "influx_init_failed", "influx init failed",
				slog.String("error_code", "FAILED_PRECONDITION"),
				slog.String("error", err.Error()),
			)
		}
	}

	var aiClient *ai.Client
	if cfg.AIEnabled && cfg.AIServiceURL != "" {
		aiClient, err = ai.New(cfg)
		if err != nil {
			readyProblems = append(readyProblems, config.Problem{Field: "AI_SERVICE_URL", Message: "failed to initialize ai client"})
			logger.Error(context.Background(), "ai_init_failed", "ai client init failed",
				slog.String("error_code", "FAILED_PRECONDITION"),
				slog.String("error", err.Error()),
			)
		}
	}

	var verifier *authx.JWTVerifier
	if cfg.OIDCIssuer != "" && cfg.OIDCAudience != "" {
		var err error
		verifier, err = authx.NewJWTVerifier(cfg.OIDCIssuer, cfg.OIDCAudience, cfg.OIDCJWKSURL, cfg.JWKSTTLSeconds, cfg.JWTClockSkewSec)
		if err != nil {
			readyProblems = append(readyProblems, config.Problem{Field: "OIDC_ISSUER", Message: "failed to initialize JWT verifier"})
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
		if err := dbx.Ping(r.Context(), dbPool); err != nil {
			httpx.WriteError(
				w,
				r,
				http.StatusServiceUnavailable,
				"FAILED_PRECONDITION",
				"service not ready: database unavailable",
				map[string]any{"problem": "db_ping_failed"},
			)
			return
		}
		if cacheClient != nil {
			if err := cacheClient.Ping(r.Context()); err != nil {
				httpx.WriteError(
					w,
					r,
					http.StatusServiceUnavailable,
					"FAILED_PRECONDITION",
					"service not ready: redis unavailable",
					map[string]any{"problem": "redis_ping_failed"},
				)
				return
			}
		}
		httpx.WriteJSON(w, http.StatusOK, statusResponse{
			Status:  "ready",
			Service: cfg.ServiceName,
			Env:     cfg.Env,
			Version: version,
		})
	})
	mux.Handle("GET /metrics", metricsx.Handler())

	mux.HandleFunc("GET /api/v1/me", func(w http.ResponseWriter, r *http.Request) {
		auth, ok := authx.FromContext(r.Context())
		if !ok {
			httpx.WriteError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "missing auth context", nil)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"subject": auth.Subject,
			"email":   auth.Email,
			"name":    auth.Name,
			"roles":   auth.Roles,
			"claims":  auth.Claims,
		})
	})
	mux.HandleFunc("GET /api/v1/tenants/current", func(w http.ResponseWriter, r *http.Request) {
		tenant, ok := tenantx.FromContext(r.Context())
		if !ok || tenant.ID == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant", nil)
			return
		}
		tenantID, err := uuid.Parse(tenant.ID)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid tenant id", nil)
			return
		}
		record, err := tenantsRepo.GetTenantByID(r.Context(), tenantID)
		if err != nil {
			httpx.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "tenant not found", nil)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"tenant_id": record.TenantID,
			"slug":      record.Slug,
			"name":      record.Name,
		})
	})
	mux.HandleFunc("POST /api/v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		tenant := tenantx.TenantIDFromContext(r.Context())
		if tenant == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant", nil)
			return
		}
		tenantID, err := uuid.Parse(tenant)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid tenant id", nil)
			return
		}

		var req createTaskRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid json body", nil)
			return
		}
		req.TaskType = strings.TrimSpace(req.TaskType)
		req.IdempotencyKey = strings.TrimSpace(req.IdempotencyKey)
		if req.TaskType == "" || req.IdempotencyKey == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing task_type or idempotency_key", nil)
			return
		}

		payload := req.Payload
		if len(payload) == 0 {
			payload = json.RawMessage(`{}`)
		}
		status := workflow.TaskStatusPending
		eventID := uuid.New()
		eventPayload, err := json.Marshal(map[string]any{
			"task_type":       req.TaskType,
			"status":          status,
			"idempotency_key": req.IdempotencyKey,
			"payload":         json.RawMessage(payload),
		})
		if err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to encode payload", nil)
			return
		}
		event := models.TaskEvent{
			EventID:   eventID,
			EventType: workflow.TaskEventCreated,
			ToStatus:  &status,
			Payload:   eventPayload,
		}
		outboxEvent := models.OutboxEvent{
			EventID:       eventID,
			TenantID:      tenantID,
			AggregateType: "task",
			Topic:         events.TopicTaskEvents,
		}
		task, created, _, outboxRecord, err := tasksRepo.CreateTaskWithEventAndOutbox(r.Context(), tenantID, req.TaskType, status, req.IdempotencyKey, payload, nil, event, outboxRepo, outboxEvent)
		if err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create task", nil)
			return
		}
		if created && cacheClient != nil {
			_ = cacheClient.SetJSON(r.Context(), taskCacheKey(tenantID, task.TaskID), task, 30*time.Second)
		}
		if created && outboxRecord.EventID == uuid.Nil {
			logger.Warn(r.Context(), "outbox_missing", "outbox event not recorded",
				slog.String("error_code", "FAILED_PRECONDITION"),
				slog.String("task_id", task.TaskID.String()),
			)
		}

		resp := map[string]any{
			"task_id":            task.TaskID,
			"tenant_id":          task.TenantID,
			"task_type":          task.TaskType,
			"status":             task.Status,
			"idempotency_key":    task.IdempotencyKey,
			"payload":            json.RawMessage(task.Payload),
			"created_by_user_id": task.CreatedByUserID,
			"created_at":         task.CreatedAt,
			"updated_at":         task.UpdatedAt,
			"created":            created,
		}
		if created {
			httpx.WriteJSON(w, http.StatusCreated, resp)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
	})
	mux.HandleFunc("GET /api/v1/tasks/{task_id}", func(w http.ResponseWriter, r *http.Request) {
		tenant := tenantx.TenantIDFromContext(r.Context())
		if tenant == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant", nil)
			return
		}
		tenantID, err := uuid.Parse(tenant)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid tenant id", nil)
			return
		}
		taskID, err := uuid.Parse(strings.TrimSpace(r.PathValue("task_id")))
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid task_id", nil)
			return
		}
		var task models.Task
		if cacheClient != nil {
			var cached models.Task
			found, err := cacheClient.GetJSON(r.Context(), taskCacheKey(tenantID, taskID), &cached)
			if err == nil && found {
				task = cached
			}
		}
		if task.TaskID == uuid.Nil {
			task, err = tasksRepo.GetTaskByID(r.Context(), tenantID, taskID)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					httpx.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "task not found", nil)
					return
				}
				httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load task", nil)
				return
			}
			if cacheClient != nil {
				_ = cacheClient.SetJSON(r.Context(), taskCacheKey(tenantID, task.TaskID), task, 30*time.Second)
			}
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"task_id":            task.TaskID,
			"tenant_id":          task.TenantID,
			"task_type":          task.TaskType,
			"status":             task.Status,
			"idempotency_key":    task.IdempotencyKey,
			"payload":            json.RawMessage(task.Payload),
			"created_by_user_id": task.CreatedByUserID,
			"created_at":         task.CreatedAt,
			"updated_at":         task.UpdatedAt,
		})
	})
	mux.HandleFunc("GET /api/v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		tenant := tenantx.TenantIDFromContext(r.Context())
		if tenant == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant", nil)
			return
		}
		tenantID, err := uuid.Parse(tenant)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid tenant id", nil)
			return
		}

		limit := readIntWithDefault(r.URL.Query().Get("limit"), 50)
		offset := readIntWithDefault(r.URL.Query().Get("offset"), 0)
		tasks, err := tasksRepo.ListTasks(r.Context(), tenantID, limit, offset)
		if err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to list tasks", nil)
			return
		}
		out := make([]map[string]any, 0, len(tasks))
		for _, task := range tasks {
			out = append(out, map[string]any{
				"task_id":            task.TaskID,
				"tenant_id":          task.TenantID,
				"task_type":          task.TaskType,
				"status":             task.Status,
				"idempotency_key":    task.IdempotencyKey,
				"payload":            json.RawMessage(task.Payload),
				"created_by_user_id": task.CreatedByUserID,
				"created_at":         task.CreatedAt,
				"updated_at":         task.UpdatedAt,
			})
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"tasks":  out,
			"limit":  limit,
			"offset": offset,
		})
	})
	mux.HandleFunc("POST /api/v1/tasks/{task_id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		tenant := tenantx.TenantIDFromContext(r.Context())
		if tenant == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant", nil)
			return
		}
		tenantID, err := uuid.Parse(tenant)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid tenant id", nil)
			return
		}
		taskID, err := uuid.Parse(strings.TrimSpace(r.PathValue("task_id")))
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid task_id", nil)
			return
		}
		var taskLock *lockx.Lock
		if cacheClient != nil {
			taskLock, _, err = lockx.Acquire(r.Context(), cacheClient.Client(), taskLockKey(tenantID, taskID), 10*time.Second)
			if err != nil {
				httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to acquire task lock", nil)
				return
			}
			if taskLock == nil {
				httpx.WriteError(w, r, http.StatusConflict, "CONFLICT", "task is locked", nil)
				return
			}
			defer func() {
				_ = lockx.Release(context.Background(), cacheClient.Client(), taskLock)
			}()
		}

		eventID := uuid.New()
		eventPayload, _ := json.Marshal(map[string]any{"action": "cancel"})
		outboxEvent := models.OutboxEvent{
			EventID:       eventID,
			TenantID:      tenantID,
			AggregateType: "task",
			AggregateID:   taskID,
			Topic:         events.TopicTaskEvents,
		}
		task, changed, _, err := tasksRepo.TransitionTaskStatusWithOutbox(
			r.Context(),
			tenantID,
			taskID,
			workflow.TaskStatusCanceled,
			workflow.TaskEventCanceled,
			eventPayload,
			nil,
			workflow.CanTransition,
			workflow.EventTypeForTransition,
			outboxRepo,
			outboxEvent,
		)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httpx.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "task not found", nil)
				return
			}
			if errors.Is(err, repos.ErrInvalidTaskTransition) {
				httpx.WriteError(w, r, http.StatusConflict, "CONFLICT", "invalid task transition", nil)
				return
			}
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to cancel task", nil)
			return
		}
		if changed && cacheClient != nil {
			_ = cacheClient.SetJSON(r.Context(), taskCacheKey(tenantID, task.TaskID), task, 30*time.Second)
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"task_id":    task.TaskID,
			"status":     task.Status,
			"updated_at": task.UpdatedAt,
			"changed":    changed,
		})
	})
	mux.HandleFunc("POST /api/v1/tasks/{task_id}/status", func(w http.ResponseWriter, r *http.Request) {
		tenant := tenantx.TenantIDFromContext(r.Context())
		if tenant == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant", nil)
			return
		}
		tenantID, err := uuid.Parse(tenant)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid tenant id", nil)
			return
		}
		taskID, err := uuid.Parse(strings.TrimSpace(r.PathValue("task_id")))
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid task_id", nil)
			return
		}
		var taskLock *lockx.Lock
		if cacheClient != nil {
			taskLock, _, err = lockx.Acquire(r.Context(), cacheClient.Client(), taskLockKey(tenantID, taskID), 10*time.Second)
			if err != nil {
				httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to acquire task lock", nil)
				return
			}
			if taskLock == nil {
				httpx.WriteError(w, r, http.StatusConflict, "CONFLICT", "task is locked", nil)
				return
			}
			defer func() {
				_ = lockx.Release(context.Background(), cacheClient.Client(), taskLock)
			}()
		}

		var req taskStatusUpdateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid json body", nil)
			return
		}
		req.Status = workflow.NormalizeTaskStatus(req.Status)
		if req.Status == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing status", nil)
			return
		}
		eventID := uuid.New()
		eventPayload, err := json.Marshal(map[string]any{
			"payload": req.Payload,
		})
		if err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to encode payload", nil)
			return
		}
		outboxEvent := models.OutboxEvent{
			EventID:       eventID,
			TenantID:      tenantID,
			AggregateType: "task",
			AggregateID:   taskID,
			Topic:         events.TopicTaskEvents,
		}

		task, changed, _, err := tasksRepo.TransitionTaskStatusWithOutbox(
			r.Context(),
			tenantID,
			taskID,
			req.Status,
			"",
			eventPayload,
			nil,
			workflow.CanTransition,
			workflow.EventTypeForTransition,
			outboxRepo,
			outboxEvent,
		)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httpx.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "task not found", nil)
				return
			}
			if errors.Is(err, repos.ErrInvalidTaskTransition) {
				httpx.WriteError(w, r, http.StatusConflict, "CONFLICT", "invalid task transition", nil)
				return
			}
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to transition task", nil)
			return
		}
		if changed && cacheClient != nil {
			_ = cacheClient.SetJSON(r.Context(), taskCacheKey(tenantID, task.TaskID), task, 30*time.Second)
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"task_id":    task.TaskID,
			"status":     task.Status,
			"updated_at": task.UpdatedAt,
			"changed":    changed,
		})
	})
	mux.HandleFunc("PUT /api/v1/robots/{robot_code}", func(w http.ResponseWriter, r *http.Request) {
		tenant := tenantx.TenantIDFromContext(r.Context())
		if tenant == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant", nil)
			return
		}
		tenantID, err := uuid.Parse(tenant)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid tenant id", nil)
			return
		}

		robotCode := strings.TrimSpace(r.PathValue("robot_code"))
		if robotCode == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing robot_code", nil)
			return
		}

		var req upsertRobotRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid json body", nil)
			return
		}

		record, err := robotsRepo.UpsertRobot(r.Context(), tenantID, robotCode, req.DisplayName, req.Status)
		if err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to upsert robot", nil)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"robot_id":     record.RobotID,
			"tenant_id":    record.TenantID,
			"robot_code":   record.RobotCode,
			"display_name": record.DisplayName,
			"status":       record.Status,
			"updated_at":   record.UpdatedAt,
		})
	})
	mux.HandleFunc("POST /api/v1/robots/{robot_code}/status", func(w http.ResponseWriter, r *http.Request) {
		tenant := tenantx.TenantIDFromContext(r.Context())
		if tenant == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant", nil)
			return
		}
		tenantID, err := uuid.Parse(tenant)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid tenant id", nil)
			return
		}

		robotCode := strings.TrimSpace(r.PathValue("robot_code"))
		if robotCode == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing robot_code", nil)
			return
		}

		var req robotStatusRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid json body", nil)
			return
		}

		robot, err := robotsRepo.GetRobotByCode(r.Context(), tenantID, robotCode)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httpx.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "robot not found", nil)
				return
			}
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load robot", nil)
			return
		}

		reportedAt := time.Now().UTC()
		if req.ReportedAt != nil {
			reportedAt = req.ReportedAt.UTC()
		}
		payload, err := json.Marshal(req)
		if err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to encode status payload", nil)
			return
		}

		eventID := uuid.New()
		envelopePayload, _ := json.Marshal(events.Envelope{
			EventID:       eventID,
			TenantID:      tenantID,
			OccurredAt:    reportedAt,
			AggregateType: "robot",
			AggregateID:   robot.RobotID,
			EventType:     "robot_status",
			Payload:       json.RawMessage(payload),
		})
		outboxEvent := models.OutboxEvent{
			EventID:       eventID,
			TenantID:      tenantID,
			AggregateType: "robot",
			AggregateID:   robot.RobotID,
			Topic:         events.TopicRobotStatus,
			Payload:       envelopePayload,
		}
		status, _, err := robotStatusRepo.InsertStatusWithOutbox(r.Context(), tenantID, robot.RobotID, robotCode, reportedAt, payload, outboxRepo, outboxEvent)
		if err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to write status", nil)
			return
		}

		if req.Status != "" {
			_, _ = robotsRepo.UpsertRobot(r.Context(), tenantID, robotCode, robot.DisplayName, req.Status)
		}
		if cacheClient != nil {
			_ = cacheClient.SetJSON(r.Context(), robotStatusCacheKey(tenantID, robotCode), status, 30*time.Second)
		}
		if influxClient != nil {
			fields := map[string]any{}
			if req.BatteryPct != nil {
				fields["battery_pct"] = *req.BatteryPct
			}
			if req.Online != nil {
				fields["online"] = *req.Online
			}
			if req.Status != "" {
				fields["status"] = req.Status
			}
			if req.Position != nil {
				fields["x"] = req.Position.X
				fields["y"] = req.Position.Y
				fields["theta"] = req.Position.Theta
			}
			if len(fields) > 0 {
				if err := influxClient.WritePoint(r.Context(), "robot_status", map[string]string{
					"tenant_id":  tenantID.String(),
					"robot_id":   robot.RobotID.String(),
					"robot_code": robotCode,
				}, fields, reportedAt); err != nil {
					metricsx.IncInfluxWriteFailure()
				}
			}
		}

		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"status_id":   status.StatusID,
			"tenant_id":   status.TenantID,
			"robot_id":    status.RobotID,
			"robot_code":  status.RobotCode,
			"reported_at": status.ReportedAt,
			"payload":     status.Payload,
		})
	})
	mux.HandleFunc("GET /api/v1/robots/{robot_code}/status", func(w http.ResponseWriter, r *http.Request) {
		tenant := tenantx.TenantIDFromContext(r.Context())
		if tenant == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant", nil)
			return
		}
		tenantID, err := uuid.Parse(tenant)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid tenant id", nil)
			return
		}

		robotCode := strings.TrimSpace(r.PathValue("robot_code"))
		if robotCode == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing robot_code", nil)
			return
		}

		var status models.RobotStatus
		if cacheClient != nil {
			var cached models.RobotStatus
			found, err := cacheClient.GetJSON(r.Context(), robotStatusCacheKey(tenantID, robotCode), &cached)
			if err == nil && found {
				status = cached
			}
		}
		if status.StatusID == uuid.Nil {
			status, err = robotStatusRepo.GetLatestStatus(r.Context(), tenantID, robotCode)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					httpx.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "status not found", nil)
					return
				}
				httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load status", nil)
				return
			}
			if cacheClient != nil {
				_ = cacheClient.SetJSON(r.Context(), robotStatusCacheKey(tenantID, robotCode), status, 30*time.Second)
			}
		}

		httpx.WriteJSON(w, http.StatusOK, map[string]any{
			"status_id":   status.StatusID,
			"tenant_id":   status.TenantID,
			"robot_id":    status.RobotID,
			"robot_code":  status.RobotCode,
			"reported_at": status.ReportedAt,
			"payload":     status.Payload,
		})
	})

	mux.HandleFunc("GET /api/v1/congestion/hotspots", func(w http.ResponseWriter, r *http.Request) {
		tenant := tenantx.TenantIDFromContext(r.Context())
		if tenant == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant", nil)
			return
		}
		tenantID, err := uuid.Parse(tenant)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid tenant id", nil)
			return
		}
		horizonSec := readIntWithDefault(r.URL.Query().Get("horizon_seconds"), 0)
		limit := readIntWithDefault(r.URL.Query().Get("limit"), 50)
		snapshots, err := congestionRepo.ListHotspots(r.Context(), tenantID, time.Duration(horizonSec)*time.Second, limit)
		if err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load hotspots", nil)
			return
		}
		out := make([]map[string]any, 0, len(snapshots))
		for _, snap := range snapshots {
			out = append(out, map[string]any{
				"zone_id":          snap.ZoneID,
				"congestion_index": snap.CongestionIndex,
				"avg_speed":        snap.AvgSpeed,
				"queue_length":     snap.QueueLength,
				"risk":             snap.Risk,
				"confidence":       snap.Confidence,
				"updated_at":       snap.UpdatedAt,
			})
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"hotspots": out, "limit": limit})
	})

	mux.HandleFunc("GET /api/v1/congestion/alerts", func(w http.ResponseWriter, r *http.Request) {
		tenant := tenantx.TenantIDFromContext(r.Context())
		if tenant == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant", nil)
			return
		}
		tenantID, err := uuid.Parse(tenant)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid tenant id", nil)
			return
		}
		status := strings.TrimSpace(r.URL.Query().Get("status"))
		limit := readIntWithDefault(r.URL.Query().Get("limit"), 50)
		alerts, err := congestionRepo.ListAlerts(r.Context(), tenantID, status, limit)
		if err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load alerts", nil)
			return
		}
		out := make([]map[string]any, 0, len(alerts))
		for _, alert := range alerts {
			out = append(out, map[string]any{
				"alert_id":         alert.AlertID,
				"zone_id":          alert.ZoneID,
				"level":            alert.Level,
				"status":           alert.Status,
				"congestion_index": alert.CongestionIndex,
				"risk":             alert.Risk,
				"confidence":       alert.Confidence,
				"detected_at":      alert.DetectedAt,
				"message":          alert.Message,
				"details":          json.RawMessage(alert.Details),
			})
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"alerts": out, "limit": limit})
	})

	mux.HandleFunc("POST /api/v1/congestion/alerts/{alert_id}/ack", func(w http.ResponseWriter, r *http.Request) {
		tenant := tenantx.TenantIDFromContext(r.Context())
		if tenant == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant", nil)
			return
		}
		tenantID, err := uuid.Parse(tenant)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid tenant id", nil)
			return
		}
		alertID, err := uuid.Parse(strings.TrimSpace(r.PathValue("alert_id")))
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid alert_id", nil)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var req alertAckRequest
		_ = json.Unmarshal(body, &req)
		if err := congestionRepo.UpdateAlertStatus(r.Context(), tenantID, alertID, "acknowledged", strings.TrimSpace(req.Notes)); err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to update alert", nil)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"alert_id": alertID, "status": "acknowledged"})
	})

	mux.HandleFunc("POST /api/v1/congestion/predict", func(w http.ResponseWriter, r *http.Request) {
		if aiClient == nil {
			httpx.WriteError(w, r, http.StatusFailedDependency, "FAILED_PRECONDITION", "ai service not configured", nil)
			return
		}
		tenant := tenantx.TenantIDFromContext(r.Context())
		if tenant == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant", nil)
			return
		}
		tenantID, err := uuid.Parse(tenant)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid tenant id", nil)
			return
		}
		var req predictHotspotsRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid json body", nil)
			return
		}
		zones := make([]ai.PredictZoneInput, 0, len(req.Zones))
		for _, z := range req.Zones {
			zones = append(zones, ai.PredictZoneInput{
				ZoneID:          z.ZoneID,
				CongestionIndex: z.CongestionIndex,
				AvgSpeed:        z.AvgSpeed,
				QueueLength:     z.QueueLength,
			})
		}
		resp, err := aiClient.PredictHotspots(r.Context(), ai.PredictRequest{
			TenantID: tenantID.String(),
			Horizon:  req.HorizonSeconds,
			Zones:    zones,
			Signals:  req.Signals,
		})
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadGateway, "INTERNAL_ERROR", "ai prediction failed", nil)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, resp)
	})

	mux.HandleFunc("GET /api/v1/stream/congestion", func(w http.ResponseWriter, r *http.Request) {
		if cacheClient == nil {
			httpx.WriteError(w, r, http.StatusFailedDependency, "FAILED_PRECONDITION", "redis not configured", nil)
			return
		}
		tenant := tenantx.TenantIDFromContext(r.Context())
		if tenant == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant", nil)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		flusher, ok := w.(http.Flusher)
		if !ok {
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "stream unsupported", nil)
			return
		}
		pubsub := cacheClient.Client().Subscribe(r.Context(), "congestion.alerts")
		defer pubsub.Close()
		ch := pubsub.Channel()
		for {
			select {
			case <-r.Context().Done():
				return
			case msg := <-ch:
				if msg == nil {
					continue
				}
				if !strings.HasPrefix(msg.Payload, tenant+":") {
					continue
				}
				payload := strings.TrimPrefix(msg.Payload, tenant+":")
				fmt.Fprintf(w, "event: congestion_alert\n")
				fmt.Fprintf(w, "data: %s\n\n", payload)
				flusher.Flush()
			}
		}
	})

	mux.HandleFunc("POST /api/v1/scheduler/decisions", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		tenant := tenantx.TenantIDFromContext(r.Context())
		if tenant == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant", nil)
			return
		}
		tenantID, err := uuid.Parse(tenant)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid tenant id", nil)
			return
		}
		var req schedulerDecisionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid json body", nil)
			return
		}
		eventID := uuid.New()
		decisionPayload, _ := json.Marshal(req)
		envelopePayload, _ := json.Marshal(events.Envelope{
			EventID:       eventID,
			TenantID:      tenantID,
			OccurredAt:    time.Now().UTC(),
			AggregateType: "scheduler",
			AggregateID:   uuid.Nil,
			EventType:     "scheduler_decision",
			Payload:       json.RawMessage(decisionPayload),
		})
		outboxEvent := models.OutboxEvent{
			EventID:       eventID,
			TenantID:      tenantID,
			AggregateType: "scheduler",
			AggregateID:   uuid.Nil,
			Topic:         events.TopicSchedulerDecisions,
			Payload:       envelopePayload,
		}
		_, err = outboxRepo.Insert(r.Context(), dbPool, outboxEvent)
		if err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to enqueue decision", nil)
			return
		}
		metricsx.ObserveSchedulerDecisionLatency(time.Since(start))
		httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"event_id": eventID})
	})

	mux.HandleFunc("GET /api/v1/timeseries/robots/{robot_id}", func(w http.ResponseWriter, r *http.Request) {
		if influxClient == nil {
			httpx.WriteError(w, r, http.StatusFailedDependency, "FAILED_PRECONDITION", "influx not configured", nil)
			return
		}
		tenant := tenantx.TenantIDFromContext(r.Context())
		if tenant == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant", nil)
			return
		}
		tenantID, err := uuid.Parse(tenant)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid tenant id", nil)
			return
		}
		robotID, err := uuid.Parse(strings.TrimSpace(r.PathValue("robot_id")))
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid robot_id", nil)
			return
		}
		start, end, window, limit, err := parseTimeRange(r)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
			return
		}
		flux := buildRobotTimeseriesQuery(cfg.InfluxBucket, tenantID, robotID, start, end, window, limit)
		points, err := queryTimeseries(r.Context(), influxClient, flux)
		if err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query timeseries", nil)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"points": points})
	})

	mux.HandleFunc("POST /api/v1/congestion/zones/{zone_id}/timeseries", func(w http.ResponseWriter, r *http.Request) {
		if influxClient == nil {
			httpx.WriteError(w, r, http.StatusFailedDependency, "FAILED_PRECONDITION", "influx not configured", nil)
			return
		}
		tenant := tenantx.TenantIDFromContext(r.Context())
		if tenant == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant", nil)
			return
		}
		tenantID, err := uuid.Parse(tenant)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid tenant id", nil)
			return
		}
		zoneID := strings.TrimSpace(r.PathValue("zone_id"))
		if zoneID == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid zone_id", nil)
			return
		}
		var req zoneCongestionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid json body", nil)
			return
		}
		ts := time.Now().UTC()
		if req.Timestamp != nil {
			ts = req.Timestamp.UTC()
		}
		fields := map[string]any{
			"congestion_index": req.CongestionIndex,
			"avg_speed":        req.AvgSpeed,
			"queue_length":     req.QueueLength,
			"risk":             req.Risk,
			"confidence":       req.Confidence,
		}
		if err := influxClient.WritePoint(r.Context(), "zone_congestion", map[string]string{
			"tenant_id": tenantID.String(),
			"zone_id":   zoneID,
		}, fields, ts); err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to write timeseries", nil)
			return
		}
		httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"status": "accepted"})
	})

	mux.HandleFunc("GET /api/v1/congestion/zones/{zone_id}", func(w http.ResponseWriter, r *http.Request) {
		if influxClient == nil {
			httpx.WriteError(w, r, http.StatusFailedDependency, "FAILED_PRECONDITION", "influx not configured", nil)
			return
		}
		tenant := tenantx.TenantIDFromContext(r.Context())
		if tenant == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant", nil)
			return
		}
		tenantID, err := uuid.Parse(tenant)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid tenant id", nil)
			return
		}
		zoneID := strings.TrimSpace(r.PathValue("zone_id"))
		if zoneID == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid zone_id", nil)
			return
		}
		start, end, window, limit, err := parseTimeRange(r)
		if err != nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", err.Error(), nil)
			return
		}
		flux := buildZoneTimeseriesQuery(cfg.InfluxBucket, tenantID, zoneID, start, end, window, limit)
		points, err := queryTimeseries(r.Context(), influxClient, flux)
		if err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to query timeseries", nil)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"points": points})
	})

	notFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "route not found", nil)
	})

	rateLimiter := middleware.NewIPRateLimiter(readRateLimitRPS(), readRateLimitBurst(), 2*time.Minute)
	corsOrigins := parseCSVEnv("CORS_ALLOW_ORIGINS")
	corsAllowCredentials := parseBoolEnv("CORS_ALLOW_CREDENTIALS")
	corsMaxAge := time.Duration(readIntEnv("CORS_MAX_AGE_SECONDS", 300)) * time.Second

	handler := httpx.WrapServeMux(mux, notFound)
	handler = middleware.RateLimitMiddleware{
		Limiter: rateLimiter,
		Skip: func(r *http.Request) bool {
			return r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/metrics"
		},
	}.Wrap(handler)
	handler = middleware.AuditMiddleware{
		Enabled: cfg.AuditEnabled,
		Repo:    auditRepo,
		Logger:  logger,
		Skip: func(r *http.Request) bool {
			return r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/metrics"
		},
	}.Wrap(handler)
	handler = middleware.TenantMiddleware{
		Tenants: tenantsRepo,
		Skip: func(r *http.Request) bool {
			return r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/metrics"
		},
	}.Wrap(handler)
	handler = middleware.AuthMiddleware{
		Verifier: verifier,
		Skip: func(r *http.Request) bool {
			return r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/metrics"
		},
	}.Wrap(handler)
	handler = middleware.DBRequiredMiddleware{
		Pool: dbPool,
		Skip: func(r *http.Request) bool {
			return r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/metrics"
		},
	}.Wrap(handler)
	handler = middleware.CORSMiddleware{
		AllowedOrigins:   corsOrigins,
		AllowCredentials: corsAllowCredentials,
		MaxAge:           corsMaxAge,
	}.Wrap(handler)
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
	if dbPool != nil {
		dbPool.Close()
	}
	if cacheClient != nil {
		_ = cacheClient.Close()
	}
	if influxClient != nil {
		influxClient.Close()
	}
	if shutdownTracer != nil {
		_ = shutdownTracer(context.Background())
	}
	logger.Info(context.Background(), "service_stop", "service stopped")
}

type upsertRobotRequest struct {
	DisplayName string `json:"display_name"`
	Status      string `json:"status"`
}

type robotStatusRequest struct {
	ReportedAt *time.Time     `json:"reported_at,omitempty"`
	Online     *bool          `json:"online,omitempty"`
	BatteryPct *float64       `json:"battery_pct,omitempty"`
	Position   *robotPosition `json:"position,omitempty"`
	Status     string         `json:"status,omitempty"`
	Meta       map[string]any `json:"meta,omitempty"`
	Extra      map[string]any `json:"extra,omitempty"`
}

type robotPosition struct {
	X     float64 `json:"x"`
	Y     float64 `json:"y"`
	Theta float64 `json:"theta"`
}

type createTaskRequest struct {
	TaskType       string          `json:"task_type"`
	IdempotencyKey string          `json:"idempotency_key"`
	Payload        json.RawMessage `json:"payload"`
}

type taskStatusUpdateRequest struct {
	Status  string         `json:"status"`
	Payload map[string]any `json:"payload,omitempty"`
}

func parseCSVEnv(name string) []string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	var out []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseBoolEnv(name string) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	return raw == "1" || raw == "true" || raw == "yes" || raw == "y"
}

func readIntEnv(name string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return fallback
	}
	val, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return val
}

func readIntWithDefault(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	val, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return val
}

func readRateLimitRPS() float64 {
	raw := strings.TrimSpace(os.Getenv("RATE_LIMIT_RPS"))
	if raw == "" {
		return 5
	}
	val, err := strconv.ParseFloat(raw, 64)
	if err != nil || val <= 0 {
		return 5
	}
	return val
}

func readRateLimitBurst() int {
	raw := strings.TrimSpace(os.Getenv("RATE_LIMIT_BURST"))
	if raw == "" {
		return 10
	}
	val, err := strconv.Atoi(raw)
	if err != nil || val <= 0 {
		return 10
	}
	return val
}

func taskCacheKey(tenantID uuid.UUID, taskID uuid.UUID) string {
	return "tenant:" + tenantID.String() + ":task:" + taskID.String()
}

func robotStatusCacheKey(tenantID uuid.UUID, robotCode string) string {
	return "tenant:" + tenantID.String() + ":robot:" + robotCode + ":status"
}

func taskLockKey(tenantID uuid.UUID, taskID uuid.UUID) string {
	return "lock:tenant:" + tenantID.String() + ":task:" + taskID.String()
}

func parseTimeRange(r *http.Request) (time.Time, time.Time, time.Duration, int, error) {
	now := time.Now().UTC()
	startRaw := strings.TrimSpace(r.URL.Query().Get("start"))
	endRaw := strings.TrimSpace(r.URL.Query().Get("end"))
	windowRaw := strings.TrimSpace(r.URL.Query().Get("window"))
	limitRaw := strings.TrimSpace(r.URL.Query().Get("limit"))

	var start time.Time
	var end time.Time
	if startRaw == "" {
		start = now.Add(-1 * time.Hour)
	} else {
		parsed, err := time.Parse(time.RFC3339, startRaw)
		if err != nil {
			return time.Time{}, time.Time{}, 0, 0, errors.New("invalid start")
		}
		start = parsed.UTC()
	}
	if endRaw == "" {
		end = now
	} else {
		parsed, err := time.Parse(time.RFC3339, endRaw)
		if err != nil {
			return time.Time{}, time.Time{}, 0, 0, errors.New("invalid end")
		}
		end = parsed.UTC()
	}
	var window time.Duration
	if windowRaw != "" {
		parsed, err := time.ParseDuration(windowRaw)
		if err != nil {
			return time.Time{}, time.Time{}, 0, 0, errors.New("invalid window")
		}
		window = parsed
	}
	limit := 500
	if limitRaw != "" {
		if val, err := strconv.Atoi(limitRaw); err == nil && val > 0 {
			limit = val
		}
	}
	return start, end, window, limit, nil
}

func buildRobotTimeseriesQuery(bucket string, tenantID uuid.UUID, robotID uuid.UUID, start time.Time, end time.Time, window time.Duration, limit int) string {
	query := strings.Builder{}
	query.WriteString(`from(bucket: "` + bucket + `")`)
	query.WriteString(` |> range(start: ` + start.Format(time.RFC3339) + `, stop: ` + end.Format(time.RFC3339) + `)`)
	query.WriteString(` |> filter(fn: (r) => r._measurement == "robot_status")`)
	query.WriteString(` |> filter(fn: (r) => r.tenant_id == "` + tenantID.String() + `")`)
	query.WriteString(` |> filter(fn: (r) => r.robot_id == "` + robotID.String() + `")`)
	if window > 0 {
		query.WriteString(` |> aggregateWindow(every: ` + window.String() + `, fn: mean, createEmpty: false)`)
	}
	if limit > 0 {
		query.WriteString(` |> limit(n: ` + strconv.Itoa(limit) + `)`)
	}
	return query.String()
}

func buildZoneTimeseriesQuery(bucket string, tenantID uuid.UUID, zoneID string, start time.Time, end time.Time, window time.Duration, limit int) string {
	query := strings.Builder{}
	query.WriteString(`from(bucket: "` + bucket + `")`)
	query.WriteString(` |> range(start: ` + start.Format(time.RFC3339) + `, stop: ` + end.Format(time.RFC3339) + `)`)
	query.WriteString(` |> filter(fn: (r) => r._measurement == "zone_congestion")`)
	query.WriteString(` |> filter(fn: (r) => r.tenant_id == "` + tenantID.String() + `")`)
	query.WriteString(` |> filter(fn: (r) => r.zone_id == "` + zoneID + `")`)
	if window > 0 {
		query.WriteString(` |> aggregateWindow(every: ` + window.String() + `, fn: mean, createEmpty: false)`)
	}
	if limit > 0 {
		query.WriteString(` |> limit(n: ` + strconv.Itoa(limit) + `)`)
	}
	return query.String()
}

func queryTimeseries(ctx context.Context, client *influxx.Client, flux string) ([]map[string]any, error) {
	result, err := client.Query(ctx, flux)
	if err != nil {
		return nil, err
	}
	defer result.Close()

	pointsByTime := map[time.Time]map[string]any{}
	for result.Next() {
		record := result.Record()
		ts := record.Time().UTC()
		if _, ok := pointsByTime[ts]; !ok {
			pointsByTime[ts] = map[string]any{
				"time": ts,
			}
		}
		pointsByTime[ts][record.Field()] = record.Value()
	}
	if result.Err() != nil {
		return nil, result.Err()
	}

	points := make([]map[string]any, 0, len(pointsByTime))
	for _, point := range pointsByTime {
		points = append(points, point)
	}
	sort.Slice(points, func(i, j int) bool {
		ti := points[i]["time"].(time.Time)
		tj := points[j]["time"].(time.Time)
		return ti.Before(tj)
	})
	return points, nil
}

type zoneCongestionRequest struct {
	Timestamp       *time.Time `json:"timestamp,omitempty"`
	CongestionIndex float64    `json:"congestion_index"`
	AvgSpeed        float64    `json:"avg_speed"`
	QueueLength     float64    `json:"queue_length"`
	Risk            float64    `json:"risk"`
	Confidence      float64    `json:"confidence"`
}

type alertAckRequest struct {
	Notes string `json:"notes"`
}

type predictHotspotsRequest struct {
	HorizonSeconds int                `json:"horizon_seconds"`
	Zones          []predictZoneInput `json:"zones"`
	Signals        map[string]float64 `json:"signals,omitempty"`
}

type predictZoneInput struct {
	ZoneID          string  `json:"zone_id"`
	CongestionIndex float64 `json:"congestion_index"`
	AvgSpeed        float64 `json:"avg_speed"`
	QueueLength     float64 `json:"queue_length"`
}

type schedulerDecisionRequest struct {
	DecisionID  string           `json:"decision_id"`
	Reason      string           `json:"reason"`
	Assignments []map[string]any `json:"assignments"`
	Parameters  map[string]any   `json:"parameters,omitempty"`
	OccurredAt  *time.Time       `json:"occurred_at,omitempty"`
}
