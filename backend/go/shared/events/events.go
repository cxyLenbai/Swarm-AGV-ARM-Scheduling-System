package events

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

type Envelope struct {
	EventID       uuid.UUID       `json:"event_id"`
	TenantID      uuid.UUID       `json:"tenant_id"`
	OccurredAt    time.Time       `json:"occurred_at"`
	AggregateType string          `json:"aggregate_type"`
	AggregateID   uuid.UUID       `json:"aggregate_id"`
	EventType     string          `json:"event_type"`
	Payload       json.RawMessage `json:"payload"`
}

const (
	TopicRobotStatus           = "robot.status"
	TopicTaskEvents            = "task.events"
	TopicAlerts                = "alerts"
	TopicSchedulerDecisions    = "scheduler.decisions"
	TopicCongestionMetrics     = "congestion.metrics"
	TopicCongestionPredictions = "congestion.predictions"
)
