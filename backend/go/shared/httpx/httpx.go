package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"log/slog"

	"swarm-agv-arm-scheduling-system/shared/logx"
	"swarm-agv-arm-scheduling-system/shared/tenantx"
)

type requestIDKey struct{}

type ErrorEnvelope struct {
	Error ErrorBody `json:"error"`
}

type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
	Details   any    `json:"details,omitempty"`
}

func WriteJSON(w http.ResponseWriter, statusCode int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(v)
}

func WriteError(w http.ResponseWriter, r *http.Request, statusCode int, code string, message string, details any) {
	WriteJSON(w, statusCode, ErrorEnvelope{
		Error: ErrorBody{
			Code:      code,
			Message:   message,
			RequestID: RequestIDFromContext(r.Context()),
			Details:   details,
		},
	})
}

func WithRequestID(next http.Handler) http.Handler {
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

func RequestIDFromContext(ctx context.Context) string {
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
		return ""
	}
	return hex.EncodeToString(b[:])
}

func WithRecover(l logx.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				stack := string(debug.Stack())
				attrs := []slog.Attr{
					slog.String("request_id", RequestIDFromContext(r.Context())),
					slog.String("method", r.Method),
					slog.String("path", r.URL.Path),
					slog.String("error_code", "INTERNAL_ERROR"),
					slog.Any("error", rec),
				}
				if strings.ToLower(l.Env()) != "prod" {
					attrs = append(attrs, slog.String("stack", stack))
				}
				l.Error(r.Context(), "panic", "panic recovered", attrs...)

				WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type RequestLogOptions struct {
	SkipPaths map[string]bool
}

func WithRequestLog(l logx.Logger, opts RequestLogOptions, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if opts.SkipPaths != nil && opts.SkipPaths[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(lrw, r)

		duration := time.Since(start)
		attrs := []slog.Attr{
			slog.String("request_id", RequestIDFromContext(r.Context())),
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status_code", lrw.statusCode),
			slog.Int64("duration_ms", duration.Milliseconds()),
			slog.String("client_ip", clientIP(r)),
		}
		tenantID := tenantx.TenantIDFromContext(r.Context())
		if tenantID == "" {
			if v := strings.TrimSpace(r.Header.Get("X-Tenant-ID")); v != "" {
				tenantID = v
			}
		}
		if tenantID != "" {
			attrs = append(attrs, slog.String("tenant_id", tenantID))
		}
		l.Info(
			r.Context(),
			"http_request",
			"http request",
			attrs...,
		)
	})
}

func WithTimeout(timeout time.Duration, next http.Handler) http.Handler {
	if timeout <= 0 {
		return next
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		done := make(chan struct{})
		crw := newCaptureResponseWriter()

		go func() {
			next.ServeHTTP(crw, r.WithContext(ctx))
			close(done)
		}()

		select {
		case <-done:
			crw.copyTo(w)
		case <-ctx.Done():
			WriteError(w, r, http.StatusGatewayTimeout, "TIMEOUT", "request timeout", nil)
		}
	})
}

func WrapServeMux(mux *http.ServeMux, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern == "" {
			next.ServeHTTP(w, r)
			return
		}
		h.ServeHTTP(w, r)
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

type captureResponseWriter struct {
	header     http.Header
	statusCode int
	body       []byte
}

func newCaptureResponseWriter() *captureResponseWriter {
	return &captureResponseWriter{
		header:     make(http.Header),
		statusCode: http.StatusOK,
	}
}

func (w *captureResponseWriter) Header() http.Header {
	return w.header
}

func (w *captureResponseWriter) WriteHeader(statusCode int) {
	w.statusCode = statusCode
}

func (w *captureResponseWriter) Write(p []byte) (int, error) {
	w.body = append(w.body, p...)
	return len(p), nil
}

func (w *captureResponseWriter) copyTo(dst http.ResponseWriter) {
	for k, v := range w.header {
		for _, vv := range v {
			dst.Header().Add(k, vv)
		}
	}
	dst.WriteHeader(w.statusCode)
	_, _ = dst.Write(w.body)
}

func clientIP(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); v != "" {
		parts := strings.Split(v, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" {
		return v
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}
