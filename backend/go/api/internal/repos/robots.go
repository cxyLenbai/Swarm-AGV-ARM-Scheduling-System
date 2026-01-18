package repos // 仓储包

import ( // 依赖导入
	"context" // 上下文处理
	"time"    // 时间工具

	"github.com/google/uuid"          // UUID 类型
	"github.com/jackc/pgx/v5/pgxpool" // 连接池

	"swarm-agv-arm-scheduling-system/api/internal/models" // 模型定义
)

type RobotsRepo struct { // 机器人仓储
	pool *pgxpool.Pool // 数据库连接池
}

func NewRobotsRepo(pool *pgxpool.Pool) *RobotsRepo { // 构造机器人仓储
	return &RobotsRepo{pool: pool} // 返回实例
}

func (r *RobotsRepo) UpsertRobot(ctx context.Context, tenantID uuid.UUID, robotCode string, displayName string, status string) (models.Robot, error) { // 插入或更新机器人
	var robot models.Robot  // 返回对象
	now := time.Now().UTC() // 当前时间
	err := r.pool.QueryRow(ctx, ` // 执行 upsert SQL
		INSERT INTO robots (tenant_id, robot_code, display_name, status, updated_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (tenant_id, robot_code) DO UPDATE SET
			display_name = EXCLUDED.display_name,
			status = EXCLUDED.status,
			updated_at = EXCLUDED.updated_at
		RETURNING robot_id, tenant_id, robot_code, display_name, status, updated_at
	`, tenantID, robotCode, displayName, nullIfEmpty(status), now).
		Scan(&robot.RobotID, &robot.TenantID, &robot.RobotCode, &robot.DisplayName, &robot.Status, &robot.UpdatedAt) // 读取结果
	return robot, err // 返回结果
}

func (r *RobotsRepo) GetRobotByCode(ctx context.Context, tenantID uuid.UUID, robotCode string) (models.Robot, error) { // 按编码获取机器人
	var robot models.Robot // 返回对象
	err := r.pool.QueryRow(ctx, ` // 执行查询
		SELECT robot_id, tenant_id, robot_code, display_name, status, updated_at
		FROM robots
		WHERE tenant_id = $1 AND robot_code = $2
	`, tenantID, robotCode).
		Scan(&robot.RobotID, &robot.TenantID, &robot.RobotCode, &robot.DisplayName, &robot.Status, &robot.UpdatedAt) // 读取结果
	return robot, err // 返回结果
}

func (r *RobotsRepo) ListRobots(ctx context.Context, tenantID uuid.UUID, limit int, offset int) ([]models.Robot, error) { // 列出机器人
	if limit <= 0 { // 默认分页大小
		limit = 50 // 默认值
	}
	rows, err := r.pool.Query(ctx, ` // 执行查询
		SELECT robot_id, tenant_id, robot_code, display_name, status, updated_at
		FROM robots
		WHERE tenant_id = $1
		ORDER BY updated_at DESC
		LIMIT $2 OFFSET $3
	`, tenantID, limit, offset)
	if err != nil { // 查询失败
		return nil, err // 返回错误
	}
	defer rows.Close() // 关闭 rows

	var robots []models.Robot // 结果集合
	for rows.Next() {         // 遍历结果
		var robot models.Robot                                                                                                                    // 单条记录
		if err := rows.Scan(&robot.RobotID, &robot.TenantID, &robot.RobotCode, &robot.DisplayName, &robot.Status, &robot.UpdatedAt); err != nil { // 扫描列
			return nil, err // 返回错误
		}
		robots = append(robots, robot) // 追加结果
	}
	return robots, rows.Err() // 返回结果与行错误
}
