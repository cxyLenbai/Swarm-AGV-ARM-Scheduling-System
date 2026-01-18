package httpx // httpx 包提供 HTTP 辅助函数和中间件。

import ( // 导入所需的标准库和本地包。
	"context"       // 请求上下文与作用域值。
	"crypto/rand"   // 生成随机 ID 的安全随机源。
	"encoding/hex"  // 请求 ID 的十六进制编码。
	"encoding/json" // JSON 编解码。
	"net"           // 网络相关工具（解析 IP）。
	"net/http"      // HTTP 服务器类型。
	"runtime/debug" // panic 时的堆栈信息。
	"strings"       // 字符串工具函数。
	"time"          // 时间与超时处理。

	"log/slog" // 结构化日志属性。

	"swarm-agv-arm-scheduling-system/shared/logx"    // 共享日志封装。
	"swarm-agv-arm-scheduling-system/shared/tenantx" // 租户上下文辅助函数。
)

type requestIDKey struct{} // 请求 ID 的上下文 key 类型。

type ErrorEnvelope struct { // 标准错误响应包装结构。
	Error ErrorBody `json:"error"` // 错误体负载。
} // 结束 ErrorEnvelope。

type ErrorBody struct { // 标准错误详情。
	Code      string `json:"code"`              // 机器可读的错误码。
	Message   string `json:"message"`           // 人类可读的错误信息。
	RequestID string `json:"request_id"`        // 用于追踪的请求 ID。
	Details   any    `json:"details,omitempty"` // 可选的额外详情。
} // 结束 ErrorBody。

func WriteJSON(w http.ResponseWriter, statusCode int, v any) { // 写入 JSON 响应并设置状态码。
	w.Header().Set("Content-Type", "application/json; charset=utf-8") // 确保内容类型为 JSON。
	w.WriteHeader(statusCode)                                         // 发送状态码。
	_ = json.NewEncoder(w).Encode(v)                                  // 编码响应体，忽略编码错误。
} // 结束 WriteJSON。

func WriteError(w http.ResponseWriter, r *http.Request, statusCode int, code string, message string, details any) { // 写入标准错误响应。
	WriteJSON(w, statusCode, ErrorEnvelope{ // 用 envelope 包装错误体。
		Error: ErrorBody{ // 构造错误负载。
			Code:      code,                              // 设置错误码。
			Message:   message,                           // 设置错误信息。
			RequestID: RequestIDFromContext(r.Context()), // 附加请求 ID。
			Details:   details,                           // 附加可选详情。
		}, // 结束 error body。
	}) // 结束 WriteJSON 调用。
} // 结束 WriteError。

func WithRequestID(next http.Handler) http.Handler { // 将请求 ID 注入到上下文和响应中。
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { // 包装处理器以添加请求 ID。
		requestID := strings.TrimSpace(r.Header.Get("X-Request-ID")) // 读取请求 ID 头。
		if requestID == "" {                                         // 若缺失则生成。
			requestID = newRequestID() // 创建新 ID。
		} // 结束缺失检查。
		w.Header().Set("X-Request-ID", requestID) // 回写 ID 到响应头。

		ctx := context.WithValue(r.Context(), requestIDKey{}, requestID) // 将 ID 存入上下文。
		next.ServeHTTP(w, r.WithContext(ctx))                            // 用更新后的上下文调用下游处理器。
	}) // 结束 handler function。
} // 结束 WithRequestID。

func RequestIDFromContext(ctx context.Context) string { // 从上下文中提取请求 ID。
	if v := ctx.Value(requestIDKey{}); v != nil { // 查询上下文值。
		if s, ok := v.(string); ok { // 断言为字符串类型。
			return s // 返回请求 ID。
		} // 结束类型断言。
	} // 结束上下文查询。
	return "" // 未找到时返回空字符串。
} // 结束 RequestIDFromContext。

func newRequestID() string { // 生成随机请求 ID。
	var b [16]byte                             // 分配 16 字节随机数组。
	if _, err := rand.Read(b[:]); err != nil { // 填充安全随机字节。
		return "" // 失败时返回空字符串。
	} // 结束随机读取检查。
	return hex.EncodeToString(b[:]) // 以十六进制编码。
} // 结束 newRequestID。

func WithRecover(l logx.Logger, next http.Handler) http.Handler { // 捕获 panic 并返回 500。
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { // 包装处理器以进行恢复。
		defer func() { // 延迟执行 panic 恢复。
			if rec := recover(); rec != nil { // 捕获 panic。
				stack := string(debug.Stack()) // 获取堆栈信息。
				attrs := []slog.Attr{          // 构造日志属性。
					slog.String("request_id", RequestIDFromContext(r.Context())), // 包含请求 ID。
					slog.String("method", r.Method),                              // 包含 HTTP 方法。
					slog.String("path", r.URL.Path),                              // 包含请求路径。
					slog.String("error_code", "INTERNAL_ERROR"),                  // 包含错误码。
					slog.Any("error", rec),                                       // 包含 panic 值。
				} // 结束 attrs slice。
				if strings.ToLower(l.Env()) != "prod" { // 非生产环境附带堆栈。
					attrs = append(attrs, slog.String("stack", stack)) // 附加堆栈信息。
				} // 结束环境检查。
				l.Error(r.Context(), "panic", "panic recovered", attrs...) // 记录 panic 事件。

				WriteError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "internal server error", nil) // 返回 500 错误。
			} // 结束 panic 检查。
		}() // 执行延迟恢复闭包。
		next.ServeHTTP(w, r) // 继续调用下游处理器。
	}) // 结束 handler function。
} // 结束 WithRecover。

type RequestLogOptions struct { // 请求日志中间件选项。
	SkipPaths map[string]bool // 需要跳过记录的路径。
} // 结束 RequestLogOptions。

func WithRequestLog(l logx.Logger, opts RequestLogOptions, next http.Handler) http.Handler { // 记录每个请求/响应的日志。
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { // 包装处理器以添加日志。
		if opts.SkipPaths != nil && opts.SkipPaths[r.URL.Path] { // 跳过指定路径。
			next.ServeHTTP(w, r) // 不记录日志直接调用。
			return               // 提前返回。
		} // 结束跳过检查。

		start := time.Now()                                                         // 记录开始时间。
		lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK} // 捕获状态码。

		next.ServeHTTP(lrw, r) // 执行处理器。

		duration := time.Since(start) // 计算耗时。
		attrs := []slog.Attr{         // 构建日志属性。
			slog.String("request_id", RequestIDFromContext(r.Context())), // 包含请求 ID。
			slog.String("method", r.Method),                              // 包含 HTTP 方法。
			slog.String("path", r.URL.Path),                              // 包含请求路径。
			slog.Int("status_code", lrw.statusCode),                      // 包含状态码。
			slog.Int64("duration_ms", duration.Milliseconds()),           // 包含耗时毫秒。
			slog.String("client_ip", clientIP(r)),                        // 包含客户端 IP。
		} // 结束 attrs slice。
		tenantID := tenantx.TenantIDFromContext(r.Context()) // 从上下文提取租户 ID。
		if tenantID == "" {                                  // 如果上下文为空则读头部。
			if v := strings.TrimSpace(r.Header.Get("X-Tenant-ID")); v != "" { // 读取租户 ID 头。
				tenantID = v // 使用头部值。
			} // 结束头部检查。
		} // 结束租户回退。
		if tenantID != "" { // 若存在租户 ID 则追加。
			attrs = append(attrs, slog.String("tenant_id", tenantID)) // 添加租户 ID 属性。
		} // 结束租户 ID 检查。
		l.Info( // 记录请求日志。
			r.Context(),    // 传递上下文。
			"http_request", // 事件名称。
			"http request", // 日志消息。
			attrs...,       // 日志属性。
		) // 结束日志调用。
	}) // 结束 handler function。
} // 结束 WithRequestLog。

func WithTimeout(timeout time.Duration, next http.Handler) http.Handler { // 为每个请求设置超时。
	if timeout <= 0 { // 未配置超时。
		return next // 返回原处理器。
	} // 结束超时检查。

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { // 用超时上下文包装处理器。
		ctx, cancel := context.WithTimeout(r.Context(), timeout) // 创建带超时的上下文。
		defer cancel()                                           // 确保调用 cancel。

		done := make(chan struct{})       // 完成信号。
		crw := newCaptureResponseWriter() // 捕获响应输出。

		go func() { // 在 goroutine 中运行处理器。
			next.ServeHTTP(crw, r.WithContext(ctx)) // 使用超时上下文执行处理器。
			close(done)                             // 发送完成信号。
		}() // 结束 goroutine。

		select { // 等待完成或超时。
		case <-done: // 处理器先完成。
			crw.copyTo(w) // 复制已捕获的响应。
		case <-ctx.Done(): // 发生超时。
			WriteError(w, r, http.StatusGatewayTimeout, "TIMEOUT", "request timeout", nil) // 返回超时错误。
		} // 结束 select。
	}) // 结束 handler function。
} // 结束 WithTimeout。

func WrapServeMux(mux *http.ServeMux, next http.Handler) http.Handler { // 将请求委派给 mux，未匹配则回退。
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { // 包装处理器以检查 mux。
		h, pattern := mux.Handler(r) // 从 mux 解析处理器。
		if pattern == "" {           // 未匹配到路由。
			next.ServeHTTP(w, r) // 回退到下一个处理器。
			return               // 提前返回。
		} // 结束未匹配检查。
		h.ServeHTTP(w, r) // 处理匹配到的处理器。
	}) // 结束 handler function。
} // 结束 WrapServeMux。

type loggingResponseWriter struct { // 记录状态码的 ResponseWriter。
	http.ResponseWriter     // 内嵌原始 ResponseWriter。
	statusCode          int // 记录的状态码。
} // 结束 loggingResponseWriter。

func (w *loggingResponseWriter) WriteHeader(statusCode int) { // 在写入时捕获状态码。
	w.statusCode = statusCode                // 记录状态码。
	w.ResponseWriter.WriteHeader(statusCode) // 委派给底层 writer。
} // 结束 WriteHeader。

type captureResponseWriter struct { // 缓冲输出的 ResponseWriter。
	header     http.Header // 缓冲的头部。
	statusCode int         // 缓冲的状态码。
	body       []byte      // 缓冲的响应体字节。
} // 结束 captureResponseWriter。

func newCaptureResponseWriter() *captureResponseWriter { // 创建新的缓冲 writer。
	return &captureResponseWriter{ // 初始化缓冲 writer。
		header:     make(http.Header), // 创建头部 map。
		statusCode: http.StatusOK,     // 默认状态码为 200。
	} // 结束结构体字面量。
} // 结束 newCaptureResponseWriter。

func (w *captureResponseWriter) Header() http.Header { // 返回缓冲的头部。
	return w.header // 返回头部 map。
} // 结束 Header。

func (w *captureResponseWriter) WriteHeader(statusCode int) { // 缓冲状态码。
	w.statusCode = statusCode // 记录状态码。
} // 结束 WriteHeader。

func (w *captureResponseWriter) Write(p []byte) (int, error) { // 缓冲响应体字节。
	w.body = append(w.body, p...) // 追加到 body 缓冲。
	return len(p), nil            // 返回写入字节数。
} // 结束 Write。

func (w *captureResponseWriter) copyTo(dst http.ResponseWriter) { // 将缓冲响应写入目标。
	for k, v := range w.header { // 复制头部。
		for _, vv := range v { // 复制每个头部值。
			dst.Header().Add(k, vv) // 添加到目标头部。
		} // 结束头部值循环。
	} // 结束头部循环。
	dst.WriteHeader(w.statusCode) // 写入状态码。
	_, _ = dst.Write(w.body)      // 写入响应体字节。
} // 结束 copyTo。

func clientIP(r *http.Request) string { // 从头部或远端地址获取客户端 IP。
	if v := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); v != "" { // 检查 X-Forwarded-For。
		parts := strings.Split(v, ",") // 拆分逗号分隔列表。
		if len(parts) > 0 {            // 确保至少一个值。
			return strings.TrimSpace(parts[0]) // 使用第一个 IP。
		} // 结束 parts 长度检查。
	} // 结束 X-Forwarded-For 检查。
	if v := strings.TrimSpace(r.Header.Get("X-Real-IP")); v != "" { // 检查 X-Real-IP。
		return v // 使用 X-Real-IP。
	} // 结束 X-Real-IP 检查。
	host, _, err := net.SplitHostPort(r.RemoteAddr) // 从远端地址解析 host。
	if err == nil && host != "" {                   // 若解析出 host 则使用。
		return host // 返回 host。
	} // 结束 host 检查。
	return r.RemoteAddr // 回退到原始远端地址。
} // 结束 clientIP。
