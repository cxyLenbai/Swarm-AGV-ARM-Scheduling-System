package workflow

import "testing"

func TestCanTransition(t *testing.T) {
	if !CanTransition(TaskStatusPending, TaskStatusRunning) {
		t.Fatalf("expected pending -> running to be allowed")
	}
	if CanTransition(TaskStatusDone, TaskStatusRunning) {
		t.Fatalf("expected done -> running to be blocked")
	}
}

func TestEventTypeForTransition(t *testing.T) {
	ev := EventTypeForTransition(TaskStatusPending, TaskStatusRunning)
	if ev == "" {
		t.Fatalf("expected event type for pending -> running")
	}
}
