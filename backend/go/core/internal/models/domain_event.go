package models

import (
	"time"

	"github.com/google/uuid"
)

const (
	AggregateTypeTask  = "task"
	AggregateTypeRobot = "robot"
)

type DomainEvent struct {
	EventID       uuid.UUID
	TenantID      uuid.UUID
	OccurredAt    time.Time
	AggregateType string
	AggregateID   uuid.UUID
	EventType     string
	Payload       []byte
}
