package models // 模型包

import ( // 依赖导入
	"time" // 时间类型

	"github.com/google/uuid" // UUID 类型
)

type Tenant struct { // 租户模型
	TenantID  uuid.UUID // 租户 ID
	Slug      string    // 租户标识
	Name      string    // 租户名称
	CreatedAt time.Time // 创建时间
}

type User struct { // 用户模型
	UserID      uuid.UUID  // 用户 ID
	TenantID    uuid.UUID  // 租户 ID
	Subject     string     // 认证主体
	Email       string     // 邮箱
	DisplayName string     // 显示名称
	Role        string     // 角色
	CreatedAt   time.Time  // 创建时间
	LastLoginAt *time.Time // 最后登录时间
}

type Robot struct { // 机器人模型
	RobotID     uuid.UUID // 机器人 ID
	TenantID    uuid.UUID // 租户 ID
	RobotCode   string    // 机器人编号
	DisplayName string    // 显示名称
	Status      string    // 状态
	UpdatedAt   time.Time // 更新时间
}

type Task struct { // 任务模型
	TaskID          uuid.UUID  // 任务 ID
	TenantID        uuid.UUID  // 租户 ID
	TaskType        string     // 任务类型
	Status          string     // 状态
	IdempotencyKey  string     // 幂等键
	Payload         []byte     // 负载数据
	CreatedByUserID *uuid.UUID // 创建用户 ID
	CreatedAt       time.Time  // 创建时间
	UpdatedAt       time.Time  // 更新时间
}

type TaskEvent struct { // 任务事件模型
	EventID     uuid.UUID  // 事件 ID
	TenantID    uuid.UUID  // 租户 ID
	TaskID      uuid.UUID  // 任务 ID
	EventType   string     // 事件类型
	FromStatus  *string    // 原状态
	ToStatus    *string    // 新状态
	OccurredAt  time.Time  // 发生时间
	ActorUserID *uuid.UUID // 操作用户 ID
	Payload     []byte     // 负载数据
}

type AuditLog struct { // 审计日志模型
	AuditID      uuid.UUID  // 审计 ID
	OccurredAt   time.Time  // 发生时间
	TenantID     uuid.UUID  // 租户 ID
	ActorUserID  *uuid.UUID // 操作用户 ID
	Subject      string     // 认证主体
	Action       string     // 动作
	ResourceType *string    // 资源类型
	ResourceID   *string    // 资源 ID
	RequestID    string     // 请求 ID
	Method       string     // HTTP 方法
	Path         string     // 请求路径
	StatusCode   int        // 状态码
	DurationMS   int64      // 耗时毫秒
	ClientIP     string     // 客户端 IP
	UserAgent    string     // UA 信息
	Details      []byte     // 详情数据
}
