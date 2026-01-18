package middleware // 中间件包

import ( // 依赖导入
	"context"       // 上下文处理
	"encoding/json" // JSON 编码
	"log/slog"      // 结构化日志
	"net/http"      // HTTP 相关
	"strings"       // 字符串工具
	"time"          // 时间工具

	"github.com/google/uuid" // UUID 解析

	"swarm-agv-arm-scheduling-system/api/internal/models" // 领域模型
	"swarm-agv-arm-scheduling-system/api/internal/repos"  // 仓储
	"swarm-agv-arm-scheduling-system/shared/authx"        // 认证上下文
	"swarm-agv-arm-scheduling-system/shared/httpx"        // HTTP 上下文
	"swarm-agv-arm-scheduling-system/shared/logx"         // 日志封装
	"swarm-agv-arm-scheduling-system/shared/tenantx"      // 租户上下文
)

type AuditMiddleware struct { // 审计中间件配置
	Enabled bool                     // 是否启用审计
	Repo    *repos.AuditRepo         // 审计仓储
	Logger  logx.Logger              // 日志实例
	Skip    func(*http.Request) bool // 跳过判断函数
	Timeout time.Duration            // 写入超时
}

func (m AuditMiddleware) Wrap(next http.Handler) http.Handler { // 包装处理器并执行审计
	if !m.Enabled || m.Repo == nil { // 未启用或仓储缺失
		return next // 直接透传
	}
	timeout := m.Timeout // 读取超时
	if timeout <= 0 {    // 使用默认值
		timeout = 2 * time.Second // 默认超时
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { // 处理器包装
		if m.Skip != nil && m.Skip(r) { // 可选跳过
			next.ServeHTTP(w, r) // 调用下游
			return               // 结束
		}

		start := time.Now()                                                         // 记录开始时间
		lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK} // 捕获状态码
		next.ServeHTTP(lrw, r)                                                      // 执行业务处理器

		tenantID := tenantx.TenantIDFromContext(r.Context()) // 从上下文获取租户
		if tenantID == "" {                                  // 兜底读取请求头
			tenantID = strings.TrimSpace(r.Header.Get("X-Tenant-ID")) // 读取请求头
		}
		if tenantID == "" { // 租户缺失
			return // 跳过审计
		}
		tenantUUID, err := uuid.Parse(tenantID) // 解析租户 UUID
		if err != nil {                         // 解析失败
			return // 跳过审计
		}

		if !shouldAudit(r, lrw.statusCode) { // 判断是否需要审计
			return // 跳过审计
		}

		resourceType, resourceID := resourceFromPath(r.URL.Path) // 推断资源信息
		entry := models.AuditLog{                                // 构建审计记录
			OccurredAt:   time.Now().UTC(),                        // 发生时间
			TenantID:     tenantUUID,                              // 租户 UUID
			Action:       actionForRequest(r, lrw.statusCode),     // 动作名称
			ResourceType: resourceType,                            // 资源类型
			ResourceID:   resourceID,                              // 资源 ID
			RequestID:    httpx.RequestIDFromContext(r.Context()), // 请求 ID
			Method:       r.Method,                                // HTTP 方法
			Path:         r.URL.Path,                              // 请求路径
			StatusCode:   lrw.statusCode,                          // 响应状态
			DurationMS:   time.Since(start).Milliseconds(),        // 耗时
			ClientIP:     clientIP(r),                             // 客户端 IP
			UserAgent:    strings.TrimSpace(r.UserAgent()),        // UA 信息
			Details:      auditDetails(r, lrw.statusCode),         // 额外信息
		}

		if auth, ok := authx.FromContext(r.Context()); ok { // 获取认证主体
			entry.Subject = auth.Subject // 设置主体
		}

		go func() { // 异步写入审计
			ctx, cancel := context.WithTimeout(context.Background(), timeout)           // 限时上下文
			defer cancel()                                                              // 释放资源
			if err := m.Repo.WriteAuditLog(ctx, []models.AuditLog{entry}); err != nil { // 写入仓储
				m.Logger.Warn(context.Background(), "audit_write_failed", "audit write failed", // 记录警告
					slog.String("error_code", "INTERNAL_ERROR"), // 错误码
					slog.String("error", err.Error()),           // 错误信息
				)
			}
		}() // 启动协程
	}) // 返回处理器
}

type loggingResponseWriter struct { // 响应写入器包装
	http.ResponseWriter     // 嵌入写入器
	statusCode          int // 记录状态码
}

func (w *loggingResponseWriter) WriteHeader(statusCode int) { // 拦截状态码
	w.statusCode = statusCode                // 保存状态码
	w.ResponseWriter.WriteHeader(statusCode) // 继续写入
}

func shouldAudit(r *http.Request, statusCode int) bool { // 判断是否需要审计
	if statusCode == http.StatusUnauthorized { // 认证失败
		return true // 必须审计
	}
	if r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete { // 写操作
		return true // 审计写操作
	}
	path := r.URL.Path                                                           // 读取路径
	return strings.Contains(path, "/robots") || strings.Contains(path, "/tasks") // 审计关键资源
}

func actionForRequest(r *http.Request, statusCode int) string { // 请求到动作的映射
	if statusCode == http.StatusUnauthorized { // 认证失败
		return "auth_failed" // 特殊动作
	}
	switch r.Method { // 按方法分支
	case http.MethodPost: // 创建
		return "create" // 动作名称
	case http.MethodPut, http.MethodPatch: // 更新
		return "update" // 动作名称
	case http.MethodDelete: // 删除
		return "delete" // 动作名称
	default: // 读取
		return "read" // 动作名称
	}
}

func auditDetails(r *http.Request, statusCode int) []byte { // 构建详情载荷
	details := map[string]any{ // 详情映射
		"status_code": statusCode, // 状态码
	}
	b, err := json.Marshal(details) // 编码为 JSON
	if err != nil {                 // 编码失败
		return nil // 无详情
	}
	return b // JSON 字节
}

func resourceFromPath(path string) (*string, *string) { // 从路径提取资源
	parts := strings.Split(strings.Trim(path, "/"), "/") // 分割路径
	if len(parts) < 2 {                                  // 段不足
		return nil, nil // 无结果
	}
	if parts[0] == "api" && len(parts) >= 3 && parts[1] == "v1" { // api v1 前缀
		resource := parts[2]                             // 资源名称
		if resource == "robots" || resource == "tasks" { // 受支持资源
			var id *string       // 可选 ID
			if len(parts) >= 4 { // 存在 ID 段
				val := strings.TrimSpace(parts[3]) // 清理 ID
				if val != "" {                     // 非空 ID
					id = &val // 设置 ID
				}
			}
			return &resource, id // 返回资源与 ID
		}
	}
	return nil, nil // 不支持的路径
}

func clientIP(r *http.Request) string { // 获取客户端 IP
	if v := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); v != "" { // 代理头
		parts := strings.Split(v, ",") // 分割列表
		if len(parts) > 0 {            // 有值
			return strings.TrimSpace(parts[0]) // 取第一个 IP
		}
	}
	if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" { // Real IP 头
		return v // 使用 Real IP
	}
	return r.RemoteAddr // 兜底使用 RemoteAddr
}
