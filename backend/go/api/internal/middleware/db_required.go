package middleware

import (
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"swarm-agv-arm-scheduling-system/shared/httpx"
)

type DBRequiredMiddleware struct {
	Pool *pgxpool.Pool
	Skip func(*http.Request) bool
}

func (m DBRequiredMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.Skip != nil && m.Skip(r) {
			next.ServeHTTP(w, r)
			return
		}
		if m.Pool == nil {
			httpx.WriteError(w, r, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "database not configured", nil)
			return
		}
		next.ServeHTTP(w, r)
	})
}
