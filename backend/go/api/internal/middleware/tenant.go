package middleware

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"swarm-agv-arm-scheduling-system/api/internal/repos"
	"swarm-agv-arm-scheduling-system/shared/authx"
	"swarm-agv-arm-scheduling-system/shared/httpx"
	"swarm-agv-arm-scheduling-system/shared/tenantx"
)

type TenantMiddleware struct {
	Tenants *repos.TenantsRepo
	Skip    func(*http.Request) bool
}

func (m TenantMiddleware) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.Skip != nil && m.Skip(r) {
			next.ServeHTTP(w, r)
			return
		}

		headerTenantID := strings.TrimSpace(r.Header.Get("X-Tenant-ID"))
		headerTenantSlug := strings.TrimSpace(r.Header.Get("X-Tenant-Slug"))

		if headerTenantID == "" && headerTenantSlug == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant header", nil)
			return
		}

		var tenantID uuid.UUID
		var tenantSlug string
		if headerTenantID != "" {
			parsed, err := uuid.Parse(headerTenantID)
			if err != nil {
				httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid tenant id", nil)
				return
			}
			tenantID = parsed
		}

		if headerTenantSlug != "" {
			tenantSlug = headerTenantSlug
			if m.Tenants == nil {
				httpx.WriteError(w, r, http.StatusFailedPrecondition, "FAILED_PRECONDITION", "tenant lookup not configured", nil)
				return
			}
			tenant, err := m.Tenants.GetTenantBySlug(r.Context(), headerTenantSlug)
			if err != nil {
				httpx.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "tenant not found", nil)
				return
			}
			if headerTenantID != "" && tenant.TenantID != tenantID {
				httpx.WriteError(w, r, http.StatusForbidden, "FORBIDDEN", "tenant mismatch", nil)
				return
			}
			tenantID = tenant.TenantID
		}

		if tenantID == uuid.Nil {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant id", nil)
			return
		}

		if auth, ok := authx.FromContext(r.Context()); ok {
			if claimTenantID := strings.TrimSpace(asString(auth.Claims["tenant_id"])); claimTenantID != "" && claimTenantID != tenantID.String() {
				httpx.WriteError(w, r, http.StatusForbidden, "FORBIDDEN", "tenant claim mismatch", nil)
				return
			}
			if tenantsClaim := auth.Claims["tenants"]; tenantsClaim != nil && !claimContainsTenant(tenantsClaim, tenantID.String()) {
				httpx.WriteError(w, r, http.StatusForbidden, "FORBIDDEN", "tenant not allowed", nil)
				return
			}
		}

		ctx := tenantx.WithTenant(r.Context(), tenantx.TenantContext{
			ID:   tenantID.String(),
			Slug: tenantSlug,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func claimContainsTenant(claim any, tenantID string) bool {
	switch t := claim.(type) {
	case []string:
		for _, v := range t {
			if strings.TrimSpace(v) == tenantID {
				return true
			}
		}
	case []any:
		for _, v := range t {
			if strings.TrimSpace(asString(v)) == tenantID {
				return true
			}
		}
	case string:
		for _, v := range strings.Fields(t) {
			if strings.TrimSpace(v) == tenantID {
				return true
			}
		}
	}
	return false
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}
