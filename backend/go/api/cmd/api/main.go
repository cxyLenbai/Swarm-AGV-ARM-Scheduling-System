package main

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"swarm-agv-arm-scheduling-system/api/internal/middleware"
	"swarm-agv-arm-scheduling-system/api/internal/models"
	"swarm-agv-arm-scheduling-system/api/internal/repos"
	"swarm-agv-arm-scheduling-system/shared/authx"
	"swarm-agv-arm-scheduling-system/shared/config"
	"swarm-agv-arm-scheduling-system/shared/dbx"
	"swarm-agv-arm-scheduling-system/shared/httpx"
	"swarm-agv-arm-scheduling-system/shared/logx"
	"swarm-agv-arm-scheduling-system/shared/tenantx"
	"swarm-agv-arm-scheduling-system/shared/workflow"
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
		httpx.WriteJSON(w, http.StatusOK, statusResponse{
			Status:  "ready",
			Service: cfg.ServiceName,
			Env:     cfg.Env,
			Version: version,
		})
	})

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
		eventPayload, err := json.Marshal(map[string]any{"payload": json.RawMessage(payload)})
		if err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to encode payload", nil)
			return
		}
		event := models.TaskEvent{
			EventType: workflow.TaskEventCreated,
			ToStatus:  &status,
			Payload:   eventPayload,
		}
		task, created, _, err := tasksRepo.CreateTaskWithEvent(r.Context(), tenantID, req.TaskType, status, req.IdempotencyKey, payload, nil, event)
		if err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to create task", nil)
			return
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
		task, err := tasksRepo.GetTaskByID(r.Context(), tenantID, taskID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httpx.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "task not found", nil)
				return
			}
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load task", nil)
			return
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

		eventPayload, _ := json.Marshal(map[string]any{"action": "cancel"})
		task, changed, err := tasksRepo.TransitionTaskStatus(
			r.Context(),
			tenantID,
			taskID,
			workflow.TaskStatusCanceled,
			workflow.TaskEventCanceled,
			eventPayload,
			nil,
			workflow.CanTransition,
			workflow.EventTypeForTransition,
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
		eventPayload, err := json.Marshal(map[string]any{"payload": req.Payload})
		if err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to encode payload", nil)
			return
		}

		task, changed, err := tasksRepo.TransitionTaskStatus(
			r.Context(),
			tenantID,
			taskID,
			req.Status,
			"",
			eventPayload,
			nil,
			workflow.CanTransition,
			workflow.EventTypeForTransition,
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

		status, err := robotStatusRepo.InsertStatus(r.Context(), tenantID, robot.RobotID, robotCode, reportedAt, payload)
		if err != nil {
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to write status", nil)
			return
		}

		if req.Status != "" {
			_, _ = robotsRepo.UpsertRobot(r.Context(), tenantID, robotCode, robot.DisplayName, req.Status)
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

		status, err := robotStatusRepo.GetLatestStatus(r.Context(), tenantID, robotCode)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				httpx.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "status not found", nil)
				return
			}
			httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load status", nil)
			return
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
			return r.URL.Path == "/healthz" || r.URL.Path == "/readyz"
		},
	}.Wrap(handler)
	handler = middleware.AuditMiddleware{
		Enabled: cfg.AuditEnabled,
		Repo:    auditRepo,
		Logger:  logger,
		Skip: func(r *http.Request) bool {
			return r.URL.Path == "/healthz" || r.URL.Path == "/readyz"
		},
	}.Wrap(handler)
	handler = middleware.TenantMiddleware{
		Tenants: tenantsRepo,
		Skip: func(r *http.Request) bool {
			return r.URL.Path == "/healthz" || r.URL.Path == "/readyz"
		},
	}.Wrap(handler)
	handler = middleware.AuthMiddleware{
		Verifier: verifier,
		Skip: func(r *http.Request) bool {
			return r.URL.Path == "/healthz" || r.URL.Path == "/readyz"
		},
	}.Wrap(handler)
	handler = middleware.DBRequiredMiddleware{
		Pool: dbPool,
		Skip: func(r *http.Request) bool {
			return r.URL.Path == "/healthz" || r.URL.Path == "/readyz"
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
	handler = httpx.WithRequestLog(logger, httpx.RequestLogOptions{SkipPaths: map[string]bool{"/healthz": true}}, handler)

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
