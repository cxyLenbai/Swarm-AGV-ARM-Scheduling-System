package models

import (
	"time"

	"github.com/google/uuid"
)

type Tenant struct {
	TenantID  uuid.UUID
	Slug      string
	Name      string
	CreatedAt time.Time
}

type User struct {
	UserID      uuid.UUID
	TenantID    uuid.UUID
	Subject     string
	Email       string
	DisplayName string
	Role        string
	CreatedAt   time.Time
	LastLoginAt *time.Time
}

type Robot struct {
	RobotID     uuid.UUID
	TenantID    uuid.UUID
	RobotCode   string
	DisplayName string
	Status      string
	UpdatedAt   time.Time
}

type Task struct {
	TaskID          uuid.UUID
	TenantID        uuid.UUID
	TaskType        string
	Status          string
	IdempotencyKey  string
	Payload         []byte
	CreatedByUserID *uuid.UUID
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type TaskEvent struct {
	EventID     uuid.UUID
	TenantID    uuid.UUID
	TaskID      uuid.UUID
	EventType   string
	FromStatus  *string
	ToStatus    *string
	OccurredAt  time.Time
	ActorUserID *uuid.UUID
	Payload     []byte
}

type AuditLog struct {
	AuditID      uuid.UUID
	OccurredAt   time.Time
	TenantID     uuid.UUID
	ActorUserID  *uuid.UUID
	Subject      string
	Action       string
	ResourceType *string
	ResourceID   *string
	RequestID    string
	Method       string
	Path         string
	StatusCode   int
	DurationMS   int64
	ClientIP     string
	UserAgent    string
	Details      []byte
}
