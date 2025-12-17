package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

type statusResponse struct {
	Status  string `json:"status"`
	Service string `json:"service"`
	Env     string `json:"env,omitempty"`
	Version string `json:"version,omitempty"`
}

func main() {
	serviceName := envOrDefault("SERVICE_NAME", "core")
	envName := os.Getenv("ENV")
	version := os.Getenv("VERSION")

	port, err := parsePort(envOrDefault("PORT", "8081"))
	if err != nil {
		log.Fatalf("invalid config: PORT=%q: %v", os.Getenv("PORT"), err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, statusResponse{
			Status:  "ok",
			Service: serviceName,
			Env:     envName,
			Version: version,
		})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, statusResponse{
			Status:  "ready",
			Service: serviceName,
			Env:     envName,
			Version: version,
		})
	})

	server := &http.Server{
		Addr:              net.JoinHostPort("", strconv.Itoa(port)),
		Handler:           withRequestLog(mux),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("starting service: service=%s env=%s port=%d version=%s", serviceName, envName, port, version)
		errCh <- server.ListenAndServe()
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Printf("shutdown signal: %s", sig.String())
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server failed: %v", err)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown failed: %v", err)
	}
	log.Printf("service stopped: %s", serviceName)
}

func envOrDefault(key, defaultValue string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultValue
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

func withRequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(lrw, r)

		duration := time.Since(start)
		log.Printf("http_request method=%s path=%s status=%d duration_ms=%d remote=%s",
			r.Method,
			r.URL.Path,
			lrw.statusCode,
			duration.Milliseconds(),
			r.RemoteAddr,
		)
	})
}

type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (w *loggingResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
	w.ResponseWriter.WriteHeader(statusCode)
}

