package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"
)

type CORSMiddleware struct {
	AllowedOrigins   []string
	AllowedMethods   []string
	AllowedHeaders   []string
	AllowCredentials bool
	MaxAge           time.Duration
	Skip             func(*http.Request) bool
}

func (m CORSMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.Skip != nil && m.Skip(r) {
			next.ServeHTTP(w, r)
			return
		}

		origin := strings.TrimSpace(r.Header.Get("Origin"))
		allowedOrigin := m.allowOrigin(origin)
		if allowedOrigin != "" {
			w.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
			w.Header().Add("Vary", "Origin")
			if m.AllowCredentials {
				w.Header().Set("Access-Control-Allow-Credentials", "true")
			}
		}

		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			w.Header().Set("Access-Control-Allow-Methods", strings.Join(m.allowedMethods(), ", "))
			w.Header().Set("Access-Control-Allow-Headers", strings.Join(m.allowedHeaders(), ", "))
			if m.MaxAge > 0 {
				w.Header().Set("Access-Control-Max-Age", formatMaxAge(m.MaxAge))
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func (m CORSMiddleware) allowOrigin(origin string) string {
	if origin == "" {
		return ""
	}
	if len(m.AllowedOrigins) == 0 {
		if m.AllowCredentials {
			return origin
		}
		return "*"
	}
	for _, allowed := range m.AllowedOrigins {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" {
			continue
		}
		if allowed == "*" {
			if m.AllowCredentials {
				return origin
			}
			return "*"
		}
		if strings.EqualFold(allowed, origin) {
			return origin
		}
	}
	return ""
}

func (m CORSMiddleware) allowedMethods() []string {
	if len(m.AllowedMethods) > 0 {
		return m.AllowedMethods
	}
	return []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}
}

func (m CORSMiddleware) allowedHeaders() []string {
	if len(m.AllowedHeaders) > 0 {
		return m.AllowedHeaders
	}
	return []string{"Authorization", "Content-Type", "X-Request-ID", "X-Tenant-ID", "X-Tenant-Slug"}
}

func formatMaxAge(d time.Duration) string {
	seconds := int(d.Seconds())
	if seconds < 0 {
		seconds = 0
	}
	return strconv.Itoa(seconds)
}
