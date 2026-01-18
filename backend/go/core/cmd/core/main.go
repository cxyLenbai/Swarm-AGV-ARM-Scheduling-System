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
	"strconv"
	"strings"
	"swarm-agv-arm-scheduling-system/shared/config"
	"swarm-agv-arm-scheduling-system/shared/httpx"
	"swarm-agv-arm-scheduling-system/shared/logx"
	"swarm-agv-arm-scheduling-system/shared/workflow"
	"syscall"
	"time"
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

	port, err := parsePort(strconv.Itoa(cfg.HTTPPort))
	if err != nil {
		log.Fatalf("invalid config: PORT=%q: %v", os.Getenv("PORT"), err)
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

	notFound := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "route not found", nil)
	})
	handler := httpx.WrapServeMux(mux, notFound)
	handler = httpx.WithTimeout(cfg.RequestTimeout, handler)
	handler = httpx.WithRequestID(handler)
	handler = httpx.WithRecover(logger, handler)
	handler = httpx.WithRequestLog(logger, httpx.RequestLogOptions{SkipPaths: map[string]bool{"/healthz": true}}, handler)

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

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *loggingResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}
