package repos

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"swarm-agv-arm-scheduling-system/api/internal/models"
)

type RobotStatusRepo struct {
	pool *pgxpool.Pool
}

func NewRobotStatusRepo(pool *pgxpool.Pool) *RobotStatusRepo {
	return &RobotStatusRepo{pool: pool}
}

func (r *RobotStatusRepo) InsertStatus(ctx context.Context, tenantID uuid.UUID, robotID uuid.UUID, robotCode string, reportedAt time.Time, payload []byte) (models.RobotStatus, error) {
	return insertStatus(ctx, r.pool, tenantID, robotID, robotCode, reportedAt, payload)
}

func (r *RobotStatusRepo) InsertStatusWithOutbox(ctx context.Context, tenantID uuid.UUID, robotID uuid.UUID, robotCode string, reportedAt time.Time, payload []byte, outbox *OutboxRepo, outboxEvent models.OutboxEvent) (models.RobotStatus, models.OutboxEvent, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return models.RobotStatus{}, models.OutboxEvent{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	status, err := insertStatus(ctx, tx, tenantID, robotID, robotCode, reportedAt, payload)
	if err != nil {
		_ = tx.Rollback(ctx)
		return models.RobotStatus{}, models.OutboxEvent{}, err
	}

	if outbox != nil {
		outboxEvent.TenantID = tenantID
		outboxEvent.AggregateID = robotID
		outboxEvent, err = outbox.Insert(ctx, tx, outboxEvent)
		if err != nil {
			_ = tx.Rollback(ctx)
			return models.RobotStatus{}, models.OutboxEvent{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return models.RobotStatus{}, models.OutboxEvent{}, err
	}
	return status, outboxEvent, nil
}

func (r *RobotStatusRepo) GetLatestStatus(ctx context.Context, tenantID uuid.UUID, robotCode string) (models.RobotStatus, error) {
	var status models.RobotStatus
	var raw []byte
	err := r.pool.QueryRow(ctx, `
		SELECT status_id, tenant_id, robot_id, robot_code, reported_at, payload
		FROM robot_statuses
		WHERE tenant_id = $1 AND robot_code = $2
		ORDER BY reported_at DESC
		LIMIT 1
	`, tenantID, robotCode).Scan(
		&status.StatusID, &status.TenantID, &status.RobotID, &status.RobotCode, &status.ReportedAt, &raw,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.RobotStatus{}, err
		}
		return models.RobotStatus{}, err
	}
	status.Payload = decodePayload(raw)
	return status, nil
}

func decodePayload(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return map[string]any{}
	}
	return payload
}

func insertStatus(ctx context.Context, db DBTX, tenantID uuid.UUID, robotID uuid.UUID, robotCode string, reportedAt time.Time, payload []byte) (models.RobotStatus, error) {
	var status models.RobotStatus
	var raw []byte
	err := db.QueryRow(ctx, `
		INSERT INTO robot_statuses (tenant_id, robot_id, robot_code, reported_at, payload)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING status_id, tenant_id, robot_id, robot_code, reported_at, payload
	`, tenantID, robotID, robotCode, reportedAt, payload).Scan(
		&status.StatusID, &status.TenantID, &status.RobotID, &status.RobotCode, &status.ReportedAt, &raw,
	)
	if err != nil {
		return models.RobotStatus{}, err
	}
	status.Payload = decodePayload(raw)
	return status, nil
}
