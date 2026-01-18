package repos // 仓储包

import ( // 依赖导入
	"context" // 上下文处理
	"time"    // 时间工具

	"github.com/jackc/pgx/v5"         // pgx 批处理
	"github.com/jackc/pgx/v5/pgxpool" // 连接池

	"swarm-agv-arm-scheduling-system/api/internal/models" // 模型定义
)

type AuditRepo struct { // 审计仓储
	pool *pgxpool.Pool // 数据库连接池
}

func NewAuditRepo(pool *pgxpool.Pool) *AuditRepo { // 构造审计仓储
	return &AuditRepo{pool: pool} // 返回实例
}

func (r *AuditRepo) WriteAuditLog(ctx context.Context, entries []models.AuditLog) error { // 写入审计日志
	if len(entries) == 0 { // 空输入
		return nil // 无需写入
	}

	batch := &pgx.Batch{}    // 创建批处理
	for i := range entries { // 遍历条目
		entry := entries[i]            // 取出条目
		if entry.OccurredAt.IsZero() { // 缺少时间
			entry.OccurredAt = time.Now().UTC() // 使用当前时间
		}
		batch.Queue(` // 追加批量 SQL
			INSERT INTO audit_logs (
				occurred_at, tenant_id, actor_user_id, subject, action,
				resource_type, resource_id, request_id, method, path,
				status_code, duration_ms, client_ip, user_agent, details
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7, $8, $9, $10,
				$11, $12, $13, $14, $15
			)
		`, // SQL 模板
			entry.OccurredAt,
			entry.TenantID,
			entry.ActorUserID,
			nullIfEmpty(entry.Subject),
			entry.Action,
			entry.ResourceType,
			entry.ResourceID,
			nullIfEmpty(entry.RequestID),
			nullIfEmpty(entry.Method),
			nullIfEmpty(entry.Path),
			entry.StatusCode,
			entry.DurationMS,
			nullIfEmpty(entry.ClientIP),
			nullIfEmpty(entry.UserAgent),
			entry.Details,
		) // 绑定参数
	}

	br := r.pool.SendBatch(ctx, batch) // 发送批处理
	defer br.Close()                   // 关闭批处理

	for range entries { // 逐条执行
		if _, err := br.Exec(); err != nil { // 执行失败
			return err // 返回错误
		}
	}
	return nil // 成功
}
