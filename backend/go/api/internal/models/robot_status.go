package models

import (
	"time"

	"github.com/google/uuid"
)

type RobotStatus struct {
	StatusID   uuid.UUID
	TenantID   uuid.UUID
	RobotID    uuid.UUID
	RobotCode  string
	ReportedAt time.Time
	Payload    map[string]any
}
