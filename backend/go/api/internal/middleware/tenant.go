package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

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

		tenantID := strings.TrimSpace(r.Header.Get("X-Tenant-ID"))
		tenantSlug := strings.TrimSpace(r.Header.Get("X-Tenant-Slug"))
		if tenantID == "" && tenantSlug == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant header", nil)
			return
		}

		var tenant tenantx.TenantContext
		if tenantSlug != "" {
			if m.Tenants == nil {
				httpx.WriteError(w, r, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "tenant repository not configured", nil)
				return
			}
			record, err := m.Tenants.GetTenantBySlug(r.Context(), tenantSlug)
			if err != nil {
				if errors.Is(err, pgx.ErrNoRows) {
					httpx.WriteError(w, r, http.StatusNotFound, "NOT_FOUND", "tenant not found", nil)
					return
				}
				httpx.WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to resolve tenant", nil)
				return
			}
			if tenantID != "" && tenantID != record.TenantID.String() {
				httpx.WriteError(w, r, http.StatusForbidden, "FORBIDDEN", "tenant mismatch", nil)
				return
			}
			tenantID = record.TenantID.String()
			tenant.Slug = record.Slug
		}

		if tenantID == "" {
			httpx.WriteError(w, r, http.StatusBadRequest, "INVALID_ARGUMENT", "missing tenant id", nil)
			return
		}

		if auth, ok := authx.FromContext(r.Context()); ok {
			if err := validateTenantClaims(auth.Claims, tenantID); err != nil {
				httpx.WriteError(w, r, http.StatusForbidden, "FORBIDDEN", err.Error(), nil)
				return
			}
		}

		tenant.ID = tenantID
		if tenant.Slug == "" && tenantSlug != "" {
			tenant.Slug = tenantSlug
		}

		ctx := tenantx.WithTenant(r.Context(), tenant)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func validateTenantClaims(claims map[string]any, tenantID string) error {
	if claims == nil || tenantID == "" {
		return nil
	}
	if v, ok := claims["tenant_id"]; ok {
		claimTenantID := strings.TrimSpace(fmt.Sprint(v))
		if claimTenantID != "" && claimTenantID != tenantID {
			return errors.New("tenant claim mismatch")
		}
	}
	if v, ok := claims["tenants"]; ok {
		allowed := map[string]struct{}{}
		switch t := v.(type) {
		case []string:
			for _, item := range t {
				item = strings.TrimSpace(item)
				if item != "" {
					allowed[item] = struct{}{}
				}
			}
		case []any:
			for _, item := range t {
				val := strings.TrimSpace(fmt.Sprint(item))
				if val != "" {
					allowed[val] = struct{}{}
				}
			}
		case string:
			for _, item := range strings.Fields(t) {
				item = strings.TrimSpace(item)
				if item != "" {
					allowed[item] = struct{}{}
				}
			}
		default:
			val := strings.TrimSpace(fmt.Sprint(t))
			if val != "" {
				allowed[val] = struct{}{}
			}
		}
		if len(allowed) > 0 {
			if _, ok := allowed[tenantID]; !ok {
				return errors.New("tenant not allowed")
			}
		}
	}
	return nil
}
