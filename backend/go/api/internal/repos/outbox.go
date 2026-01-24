package repos

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"swarm-agv-arm-scheduling-system/api/internal/models"
)

const (
	OutboxStatusPending   = "pending"
	OutboxStatusSending   = "sending"
	OutboxStatusDelivered = "delivered"
	OutboxStatusDead      = "dead"
)

type OutboxRepo struct {
	pool *pgxpool.Pool
}

func NewOutboxRepo(pool *pgxpool.Pool) *OutboxRepo {
	return &OutboxRepo{pool: pool}
}

func (r *OutboxRepo) Insert(ctx context.Context, db DBTX, event models.OutboxEvent) (models.OutboxEvent, error) {
	if event.EventID == uuid.Nil {
		event.EventID = uuid.New()
	}
	if event.Status == "" {
		event.Status = OutboxStatusPending
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	if event.UpdatedAt.IsZero() {
		event.UpdatedAt = event.CreatedAt
	}

	err := db.QueryRow(ctx, `
		INSERT INTO outbox_events (
			event_id, tenant_id, aggregate_type, aggregate_id, topic, payload, status, attempts, next_retry_at, locked_at, locked_by, last_error, created_at, updated_at, published_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
		)
		RETURNING event_id, tenant_id, aggregate_type, aggregate_id, topic, payload, status, attempts, next_retry_at, locked_at, locked_by, last_error, created_at, updated_at, published_at
	`, event.EventID, event.TenantID, event.AggregateType, event.AggregateID, event.Topic, event.Payload, event.Status, event.Attempts, event.NextRetryAt, event.LockedAt, event.LockedBy, event.LastError, event.CreatedAt, event.UpdatedAt, event.PublishedAt).
		Scan(&event.EventID, &event.TenantID, &event.AggregateType, &event.AggregateID, &event.Topic, &event.Payload, &event.Status, &event.Attempts, &event.NextRetryAt, &event.LockedAt, &event.LockedBy, &event.LastError, &event.CreatedAt, &event.UpdatedAt, &event.PublishedAt)
	return event, err
}

func (r *OutboxRepo) ClaimPending(ctx context.Context, owner string, limit int) ([]models.OutboxEvent, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		WITH candidates AS (
			SELECT event_id
			FROM outbox_events
			WHERE status = $1 AND (next_retry_at IS NULL OR next_retry_at <= now())
			ORDER BY created_at ASC
			FOR UPDATE SKIP LOCKED
			LIMIT $2
		)
		UPDATE outbox_events o
		SET status = $3, locked_at = now(), locked_by = $4, updated_at = now()
		FROM candidates c
		WHERE o.event_id = c.event_id
		RETURNING o.event_id, o.tenant_id, o.aggregate_type, o.aggregate_id, o.topic, o.payload, o.status,
			o.attempts, o.next_retry_at, o.locked_at, o.locked_by, o.last_error, o.created_at, o.updated_at, o.published_at
	`, OutboxStatusPending, limit, OutboxStatusSending, owner)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	events := make([]models.OutboxEvent, 0, limit)
	for rows.Next() {
		var event models.OutboxEvent
		if err := rows.Scan(
			&event.EventID, &event.TenantID, &event.AggregateType, &event.AggregateID, &event.Topic, &event.Payload, &event.Status,
			&event.Attempts, &event.NextRetryAt, &event.LockedAt, &event.LockedBy, &event.LastError, &event.CreatedAt, &event.UpdatedAt, &event.PublishedAt,
		); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (r *OutboxRepo) GetByID(ctx context.Context, eventID uuid.UUID) (models.OutboxEvent, error) {
	var event models.OutboxEvent
	err := r.pool.QueryRow(ctx, `
		SELECT event_id, tenant_id, aggregate_type, aggregate_id, topic, payload, status, attempts, next_retry_at, locked_at, locked_by, last_error, created_at, updated_at, published_at
		FROM outbox_events
		WHERE event_id = $1
	`, eventID).Scan(
		&event.EventID, &event.TenantID, &event.AggregateType, &event.AggregateID, &event.Topic, &event.Payload, &event.Status, &event.Attempts,
		&event.NextRetryAt, &event.LockedAt, &event.LockedBy, &event.LastError, &event.CreatedAt, &event.UpdatedAt, &event.PublishedAt,
	)
	return event, err
}

func (r *OutboxRepo) MarkDelivered(ctx context.Context, eventID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE outbox_events
		SET status = $2, published_at = now(), updated_at = now()
		WHERE event_id = $1
	`, eventID, OutboxStatusDelivered)
	return err
}

func (r *OutboxRepo) MarkFailed(ctx context.Context, eventID uuid.UUID, attempts int, nextRetryAt *time.Time, lastErr string, dead bool) error {
	status := OutboxStatusPending
	if dead {
		status = OutboxStatusDead
		nextRetryAt = nil
	}
	_, err := r.pool.Exec(ctx, `
		UPDATE outbox_events
		SET status = $2, attempts = $3, next_retry_at = $4, last_error = $5, updated_at = now()
		WHERE event_id = $1
	`, eventID, status, attempts, nextRetryAt, lastErr)
	return err
}

func (r *OutboxRepo) IsLocked(ctx context.Context, eventID uuid.UUID) (bool, error) {
	var lockedAt *time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT locked_at FROM outbox_events WHERE event_id = $1
	`, eventID).Scan(&lockedAt)
	if err != nil {
		return false, err
	}
	return lockedAt != nil, nil
}

func (r *OutboxRepo) EnsurePending(ctx context.Context, eventID uuid.UUID) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE outbox_events
		SET status = $2, updated_at = now()
		WHERE event_id = $1 AND status = $3
	`, eventID, OutboxStatusPending, OutboxStatusSending)
	return err
}

func (r *OutboxRepo) InsertInTx(ctx context.Context, tx pgx.Tx, event models.OutboxEvent) (models.OutboxEvent, error) {
	return r.Insert(ctx, tx, event)
}
