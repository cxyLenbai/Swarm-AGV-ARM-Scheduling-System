package tenantx // tenantx 包提供租户上下文工具。

import "context" // 上下文包。

type contextKey struct{} // 租户上下文的 key 类型。

type TenantContext struct { // 租户上下文信息。
	ID   string // 租户 ID。
	Slug string // 租户标识（slug）。
} // 结束 TenantContext。

func WithTenant(ctx context.Context, tenant TenantContext) context.Context { // 将租户信息写入上下文。
	return context.WithValue(ctx, contextKey{}, tenant) // 返回携带租户信息的新上下文。
} // 结束 WithTenant。

func FromContext(ctx context.Context) (TenantContext, bool) { // 从上下文中读取租户信息。
	if v := ctx.Value(contextKey{}); v != nil { // 读取上下文值。
		if t, ok := v.(TenantContext); ok { // 断言为 TenantContext。
			return t, true // 返回租户信息和 true。
		} // 结束类型断言。
	} // 结束上下文读取。
	return TenantContext{}, false // 未命中返回默认值与 false。
} // 结束 FromContext。

func TenantIDFromContext(ctx context.Context) string { // 从上下文中获取租户 ID。
	if t, ok := FromContext(ctx); ok { // 读取租户上下文。
		return t.ID // 返回租户 ID。
	} // 结束读取检查。
	return "" // 未找到返回空字符串。
} // 结束 TenantIDFromContext。
