package repos

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"swarm-agv-arm-scheduling-system/api/internal/models"
)

type AuditRepo struct {
	pool *pgxpool.Pool
}

func NewAuditRepo(pool *pgxpool.Pool) *AuditRepo {
	return &AuditRepo{pool: pool}
}

func (r *AuditRepo) WriteAuditLog(ctx context.Context, entries []models.AuditLog) error {
	if len(entries) == 0 {
		return nil
	}

	batch := &pgx.Batch{}
	for i := range entries {
		entry := entries[i]
		if entry.OccurredAt.IsZero() {
			entry.OccurredAt = time.Now().UTC()
		}
		batch.Queue(`
			INSERT INTO audit_logs (
				occurred_at, tenant_id, actor_user_id, subject, action,
				resource_type, resource_id, request_id, method, path,
				status_code, duration_ms, client_ip, user_agent, details
			) VALUES (
				$1, $2, $3, $4, $5,
				$6, $7, $8, $9, $10,
				$11, $12, $13, $14, $15
			)
		`,
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
		)
	}

	br := r.pool.SendBatch(ctx, batch)
	defer br.Close()

	for range entries {
		if _, err := br.Exec(); err != nil {
			return err
		}
	}
	return nil
}
