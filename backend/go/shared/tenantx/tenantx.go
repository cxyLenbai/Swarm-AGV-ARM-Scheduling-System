package tenantx

import "context"

type contextKey struct{}

type TenantContext struct {
	ID   string
	Slug string
}

func WithTenant(ctx context.Context, tenant TenantContext) context.Context {
	return context.WithValue(ctx, contextKey{}, tenant)
}

func FromContext(ctx context.Context) (TenantContext, bool) {
	if v := ctx.Value(contextKey{}); v != nil {
		if t, ok := v.(TenantContext); ok {
			return t, true
		}
	}
	return TenantContext{}, false
}

func TenantIDFromContext(ctx context.Context) string {
	if t, ok := FromContext(ctx); ok {
		return t.ID
	}
	return ""
}
