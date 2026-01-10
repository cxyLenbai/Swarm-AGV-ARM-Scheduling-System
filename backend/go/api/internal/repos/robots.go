package repos

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"swarm-agv-arm-scheduling-system/api/internal/models"
)

type RobotsRepo struct {
	pool *pgxpool.Pool
}

func NewRobotsRepo(pool *pgxpool.Pool) *RobotsRepo {
	return &RobotsRepo{pool: pool}
}

func (r *RobotsRepo) UpsertRobot(ctx context.Context, tenantID uuid.UUID, robotCode string, displayName string, status string) (models.Robot, error) {
	var robot models.Robot
	now := time.Now().UTC()
	err := r.pool.QueryRow(ctx, `
		INSERT INTO robots (tenant_id, robot_code, display_name, status, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (tenant_id, robot_code) DO UPDATE SET
			display_name = EXCLUDED.display_name,
			status = EXCLUDED.status,
			updated_at = EXCLUDED.updated_at
		RETURNING robot_id, tenant_id, robot_code, display_name, status, updated_at
	`, tenantID, robotCode, displayName, nullIfEmpty(status), now).
		Scan(&robot.RobotID, &robot.TenantID, &robot.RobotCode, &robot.DisplayName, &robot.Status, &robot.UpdatedAt)
	return robot, err
}

func (r *RobotsRepo) GetRobotByCode(ctx context.Context, tenantID uuid.UUID, robotCode string) (models.Robot, error) {
	var robot models.Robot
	err := r.pool.QueryRow(ctx, `
		SELECT robot_id, tenant_id, robot_code, display_name, status, updated_at
		FROM robots
		WHERE tenant_id = $1 AND robot_code = $2
	`, tenantID, robotCode).
		Scan(&robot.RobotID, &robot.TenantID, &robot.RobotCode, &robot.DisplayName, &robot.Status, &robot.UpdatedAt)
	return robot, err
}

func (r *RobotsRepo) ListRobots(ctx context.Context, tenantID uuid.UUID, limit int, offset int) ([]models.Robot, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.pool.Query(ctx, `
		SELECT robot_id, tenant_id, robot_code, display_name, status, updated_at
		FROM robots
		WHERE tenant_id = $1
		ORDER BY updated_at DESC
		LIMIT $2 OFFSET $3
	`, tenantID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var robots []models.Robot
	for rows.Next() {
		var robot models.Robot
		if err := rows.Scan(&robot.RobotID, &robot.TenantID, &robot.RobotCode, &robot.DisplayName, &robot.Status, &robot.UpdatedAt); err != nil {
			return nil, err
		}
		robots = append(robots, robot)
	}
	return robots, rows.Err()
}
