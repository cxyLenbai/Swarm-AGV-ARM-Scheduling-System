package middleware

import (
	"net/http"
	"strings"

	"swarm-agv-arm-scheduling-system/shared/authx"
	"swarm-agv-arm-scheduling-system/shared/httpx"
)

type AuthMiddleware struct {
	Verifier *authx.JWTVerifier
	Skip     func(*http.Request) bool
}

func (m AuthMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.Skip != nil && m.Skip(r) {
			next.ServeHTTP(w, r)
			return
		}

		if m.Verifier == nil {
			httpx.WriteError(w, r, http.StatusFailedPrecondition, "FAILED_PRECONDITION", "auth verifier not configured", nil)
			return
		}

		authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
		if !strings.HasPrefix(strings.ToLower(authHeader), "bearer ") {
			httpx.WriteError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "missing bearer token", nil)
			return
		}
		token := strings.TrimSpace(authHeader[len("bearer "):])
		auth, err := m.Verifier.Verify(r.Context(), token)
		if err != nil {
			httpx.WriteError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "invalid token", nil)
			return
		}

		ctx := authx.WithAuth(r.Context(), auth)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
