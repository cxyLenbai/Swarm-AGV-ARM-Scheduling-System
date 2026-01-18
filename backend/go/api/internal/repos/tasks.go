package repos

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"swarm-agv-arm-scheduling-system/api/internal/models"
)

type TasksRepo struct {
	pool *pgxpool.Pool
}

var ErrInvalidTaskTransition = errors.New("invalid task transition")

func NewTasksRepo(pool *pgxpool.Pool) *TasksRepo {
	return &TasksRepo{pool: pool}
}

func (r *TasksRepo) CreateTask(ctx context.Context, tenantID uuid.UUID, taskType string, status string, idempotencyKey string, payload []byte, createdBy *uuid.UUID) (models.Task, bool, error) {
	return createTask(ctx, r.pool, tenantID, taskType, status, idempotencyKey, payload, createdBy)
}

func (r *TasksRepo) GetTaskByID(ctx context.Context, tenantID uuid.UUID, taskID uuid.UUID) (models.Task, error) {
	var task models.Task
	err := r.pool.QueryRow(ctx, `
		SELECT task_id, tenant_id, task_type, status, idempotency_key, payload, created_by_user_id, created_at, updated_at
		FROM tasks
		WHERE tenant_id = $1 AND task_id = $2
	`, tenantID, taskID).
		Scan(&task.TaskID, &task.TenantID, &task.TaskType, &task.Status, &task.IdempotencyKey, &task.Payload, &task.CreatedByUserID, &task.CreatedAt, &task.UpdatedAt)
	return task, err
}

func (r *TasksRepo) ListTasks(ctx context.Context, tenantID uuid.UUID, limit int, offset int) ([]models.Task, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT task_id, tenant_id, task_type, status, idempotency_key, payload, created_by_user_id, created_at, updated_at
		FROM tasks
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT $2 OFFSET $3
	`, tenantID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tasks []models.Task
	for rows.Next() {
		var task models.Task
		if err := rows.Scan(&task.TaskID, &task.TenantID, &task.TaskType, &task.Status, &task.IdempotencyKey, &task.Payload, &task.CreatedByUserID, &task.CreatedAt, &task.UpdatedAt); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

func (r *TasksRepo) AppendTaskEvent(ctx context.Context, event models.TaskEvent) (models.TaskEvent, error) {
	return appendTaskEvent(ctx, r.pool, event)
}

func (r *TasksRepo) TransitionTaskStatus(ctx context.Context, tenantID uuid.UUID, taskID uuid.UUID, toStatus string, eventType string, payload []byte, actorUserID *uuid.UUID, canTransition func(string, string) bool, eventTypeForTransition func(string, string) string) (models.Task, bool, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return models.Task{}, false, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	var task models.Task
	err = tx.QueryRow(ctx, `
		SELECT task_id, tenant_id, task_type, status, idempotency_key, payload, created_by_user_id, created_at, updated_at
		FROM tasks
		WHERE tenant_id = $1 AND task_id = $2
		FOR UPDATE
	`, tenantID, taskID).
		Scan(&task.TaskID, &task.TenantID, &task.TaskType, &task.Status, &task.IdempotencyKey, &task.Payload, &task.CreatedByUserID, &task.CreatedAt, &task.UpdatedAt)
	if err != nil {
		return models.Task{}, false, err
	}
	if task.Status == toStatus {
		if err := tx.Commit(ctx); err != nil {
			return models.Task{}, false, err
		}
		return task, false, nil
	}
	if canTransition != nil && !canTransition(task.Status, toStatus) {
		return models.Task{}, false, ErrInvalidTaskTransition
	}
	if eventType == "" && eventTypeForTransition != nil {
		eventType = eventTypeForTransition(task.Status, toStatus)
	}

	now := time.Now().UTC()
	_, err = tx.Exec(ctx, `
		UPDATE tasks
		SET status = $3, updated_at = $4
		WHERE tenant_id = $1 AND task_id = $2
	`, tenantID, taskID, toStatus, now)
	if err != nil {
		return models.Task{}, false, err
	}

	fromStatus := task.Status
	task.Status = toStatus
	task.UpdatedAt = now

	event := models.TaskEvent{
		TenantID:    tenantID,
		TaskID:      taskID,
		EventType:   eventType,
		OccurredAt:  now,
		ActorUserID: actorUserID,
		Payload:     payload,
	}
	if fromStatus != "" {
		event.FromStatus = &fromStatus
	}
	if toStatus != "" {
		event.ToStatus = &toStatus
	}
	if _, err = appendTaskEvent(ctx, tx, event); err != nil {
		return models.Task{}, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return models.Task{}, false, err
	}
	return task, true, nil
}

func (r *TasksRepo) CreateTaskWithEvent(ctx context.Context, tenantID uuid.UUID, taskType string, status string, idempotencyKey string, payload []byte, createdBy *uuid.UUID, event models.TaskEvent) (models.Task, bool, models.TaskEvent, error) {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return models.Task{}, false, models.TaskEvent{}, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
		}
	}()

	task, created, err := createTask(ctx, tx, tenantID, taskType, status, idempotencyKey, payload, createdBy)
	if err != nil {
		_ = tx.Rollback(ctx)
		return models.Task{}, false, models.TaskEvent{}, err
	}

	if created {
		event.TenantID = tenantID
		event.TaskID = task.TaskID
		event, err = appendTaskEvent(ctx, tx, event)
		if err != nil {
			_ = tx.Rollback(ctx)
			return models.Task{}, false, models.TaskEvent{}, err
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return models.Task{}, false, models.TaskEvent{}, err
	}
	return task, created, event, nil
}

func createTask(ctx context.Context, db DBTX, tenantID uuid.UUID, taskType string, status string, idempotencyKey string, payload []byte, createdBy *uuid.UUID) (models.Task, bool, error) {
	var task models.Task
	now := time.Now().UTC()
	err := db.QueryRow(ctx, `
		INSERT INTO tasks (tenant_id, task_type, status, idempotency_key, payload, created_by_user_id, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7)
		ON CONFLICT (tenant_id, idempotency_key) DO NOTHING
		RETURNING task_id, tenant_id, task_type, status, idempotency_key, payload, created_by_user_id, created_at, updated_at
	`, tenantID, taskType, status, idempotencyKey, payload, createdBy, now).
		Scan(&task.TaskID, &task.TenantID, &task.TaskType, &task.Status, &task.IdempotencyKey, &task.Payload, &task.CreatedByUserID, &task.CreatedAt, &task.UpdatedAt)
	if err == nil {
		return task, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return models.Task{}, false, err
	}

	err = db.QueryRow(ctx, `
		SELECT task_id, tenant_id, task_type, status, idempotency_key, payload, created_by_user_id, created_at, updated_at
		FROM tasks
		WHERE tenant_id = $1 AND idempotency_key = $2
	`, tenantID, idempotencyKey).
		Scan(&task.TaskID, &task.TenantID, &task.TaskType, &task.Status, &task.IdempotencyKey, &task.Payload, &task.CreatedByUserID, &task.CreatedAt, &task.UpdatedAt)
	if err != nil {
		return models.Task{}, false, err
	}
	return task, false, nil
}

func appendTaskEvent(ctx context.Context, db DBTX, event models.TaskEvent) (models.TaskEvent, error) {
	if event.OccurredAt.IsZero() {
		event.OccurredAt = time.Now().UTC()
	}
	err := db.QueryRow(ctx, `
		INSERT INTO task_events (tenant_id, task_id, event_type, from_status, to_status, occurred_at, actor_user_id, payload)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING event_id, tenant_id, task_id, event_type, from_status, to_status, occurred_at, actor_user_id, payload
	`, event.TenantID, event.TaskID, event.EventType, event.FromStatus, event.ToStatus, event.OccurredAt, event.ActorUserID, event.Payload).
		Scan(&event.EventID, &event.TenantID, &event.TaskID, &event.EventType, &event.FromStatus, &event.ToStatus, &event.OccurredAt, &event.ActorUserID, &event.Payload)
	return event, err
}
