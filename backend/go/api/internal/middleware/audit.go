package middleware

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"swarm-agv-arm-scheduling-system/api/internal/models"
	"swarm-agv-arm-scheduling-system/api/internal/repos"
	"swarm-agv-arm-scheduling-system/shared/authx"
	"swarm-agv-arm-scheduling-system/shared/httpx"
	"swarm-agv-arm-scheduling-system/shared/logx"
	"swarm-agv-arm-scheduling-system/shared/tenantx"
)

type AuditMiddleware struct {
	Enabled bool
	Repo    *repos.AuditRepo
	Logger  logx.Logger
	Skip    func(*http.Request) bool
	Timeout time.Duration
}

func (m AuditMiddleware) Wrap(next http.Handler) http.Handler {
	if !m.Enabled || m.Repo == nil {
		return next
	}
	timeout := m.Timeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.Skip != nil && m.Skip(r) {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
		next.ServeHTTP(lrw, r)

		tenantID := tenantx.TenantIDFromContext(r.Context())
		if tenantID == "" {
			tenantID = strings.TrimSpace(r.Header.Get("X-Tenant-ID"))
		}
		if tenantID == "" {
			return
		}
		tenantUUID, err := uuid.Parse(tenantID)
		if err != nil {
			return
		}

		if !shouldAudit(r, lrw.statusCode) {
			return
		}

		resourceType, resourceID := resourceFromPath(r.URL.Path)
		entry := models.AuditLog{
			OccurredAt:   time.Now().UTC(),
			TenantID:     tenantUUID,
			Action:       actionForRequest(r, lrw.statusCode),
			ResourceType: resourceType,
			ResourceID:   resourceID,
			RequestID:    httpx.RequestIDFromContext(r.Context()),
			Method:       r.Method,
			Path:         r.URL.Path,
			StatusCode:   lrw.statusCode,
			DurationMS:   time.Since(start).Milliseconds(),
			ClientIP:     clientIP(r),
			UserAgent:    strings.TrimSpace(r.UserAgent()),
			Details:      auditDetails(r, lrw.statusCode),
		}

		if auth, ok := authx.FromContext(r.Context()); ok {
			entry.Subject = auth.Subject
		}

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			if err := m.Repo.WriteAuditLog(ctx, []models.AuditLog{entry}); err != nil {
				m.Logger.Warn(context.Background(), "audit_write_failed", "audit write failed",
					slog.String("error_code", "INTERNAL_ERROR"),
					slog.String("error", err.Error()),
				)
			}
		}()
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

func shouldAudit(r *http.Request, statusCode int) bool {
	if statusCode == http.StatusUnauthorized {
		return true
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete {
		return true
	}
	path := r.URL.Path
	return strings.Contains(path, "/robots") || strings.Contains(path, "/tasks")
}

func actionForRequest(r *http.Request, statusCode int) string {
	if statusCode == http.StatusUnauthorized {
		return "auth_failed"
	}
	switch r.Method {
	case http.MethodPost:
		return "create"
	case http.MethodPut, http.MethodPatch:
		return "update"
	case http.MethodDelete:
		return "delete"
	default:
		return "read"
	}
}

func auditDetails(r *http.Request, statusCode int) []byte {
	details := map[string]any{
		"status_code": statusCode,
	}
	b, err := json.Marshal(details)
	if err != nil {
		return nil
	}
	return b
}

func resourceFromPath(path string) (*string, *string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 {
		return nil, nil
	}
	if parts[0] == "api" && len(parts) >= 3 && parts[1] == "v1" {
		resource := parts[2]
		if resource == "robots" || resource == "tasks" {
			var id *string
			if len(parts) >= 4 {
				val := strings.TrimSpace(parts[3])
				if val != "" {
					id = &val
				}
			}
			return &resource, id
		}
	}
	return nil, nil
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
	return r.RemoteAddr
}
