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
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"swarm-agv-arm-scheduling-system/api/internal/middleware"
	"swarm-agv-arm-scheduling-system/api/internal/repos"
	"swarm-agv-arm-scheduling-system/shared/authx"
	"swarm-agv-arm-scheduling-system/shared/config"
	"swarm-agv-arm-scheduling-system/shared/dbx"
	"swarm-agv-arm-scheduling-system/shared/httpx"
	"swarm-agv-arm-scheduling-system/shared/logx"
	"swarm-agv-arm-scheduling-system/shared/tenantx"
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

	notFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "route not found", nil)
	})

	handler := httpx.WrapServeMux(mux, notFound)
	handler = middleware.DBRequiredMiddleware{
		Pool: dbPool,
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
