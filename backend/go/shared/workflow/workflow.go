package workflow

import "strings"

const (
	TaskStatusPending  = "pending"
	TaskStatusRunning  = "running"
	TaskStatusDone     = "done"
	TaskStatusFailed   = "failed"
	TaskStatusCanceled = "canceled"
)

const (
	TaskEventCreated   = "task_created"
	TaskEventStarted   = "task_started"
	TaskEventCompleted = "task_completed"
	TaskEventFailed    = "task_failed"
	TaskEventCanceled  = "task_canceled"
)

var taskTransitions = map[string]map[string]string{
	TaskStatusPending: {
		TaskStatusRunning:  TaskEventStarted,
		TaskStatusCanceled: TaskEventCanceled,
	},
	TaskStatusRunning: {
		TaskStatusDone:     TaskEventCompleted,
		TaskStatusFailed:   TaskEventFailed,
		TaskStatusCanceled: TaskEventCanceled,
	},
}

func NormalizeTaskStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func CanTransition(fromStatus string, toStatus string) bool {
	fromStatus = NormalizeTaskStatus(fromStatus)
	toStatus = NormalizeTaskStatus(toStatus)
	if fromStatus == toStatus {
		return true
	}
	next := taskTransitions[fromStatus]
	if next == nil {
		return false
	}
	_, ok := next[toStatus]
	return ok
}

func EventTypeForTransition(fromStatus string, toStatus string) string {
	fromStatus = NormalizeTaskStatus(fromStatus)
	toStatus = NormalizeTaskStatus(toStatus)
	if fromStatus == toStatus {
		return ""
	}
	next := taskTransitions[fromStatus]
	if next == nil {
		return ""
	}
	return next[toStatus]
}

func AllTaskStatuses() []string {
	return []string{
		TaskStatusPending,
		TaskStatusRunning,
		TaskStatusDone,
		TaskStatusFailed,
		TaskStatusCanceled,
	}
}
