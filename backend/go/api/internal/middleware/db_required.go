package middleware // 中间件包

import ( // 依赖导入
	"net/http" // HTTP 相关

	"github.com/jackc/pgx/v5/pgxpool" // 连接池

	"swarm-agv-arm-scheduling-system/shared/httpx" // HTTP 响应工具
)

type DBRequiredMiddleware struct { // 数据库必需中间件配置
	Pool *pgxpool.Pool            // 数据库连接池
	Skip func(*http.Request) bool // 跳过判断函数
}

func (m DBRequiredMiddleware) Wrap(next http.Handler) http.Handler { // 包装处理器并检查数据库
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { // 处理器包装
		if m.Skip != nil && m.Skip(r) { // 可选跳过
			next.ServeHTTP(w, r) // 调用下游
			return               // 结束
		}
		if m.Pool == nil { // 连接池未配置
			httpx.WriteError(w, r, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "database not configured", nil) // 返回错误
			return                                                                                                       // 结束
		}
		next.ServeHTTP(w, r) // 继续处理
	}) // 返回处理器
}
