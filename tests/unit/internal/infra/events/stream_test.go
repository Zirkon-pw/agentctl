package events

import (
	. "github.com/docup/agentctl/internal/infra/events"
	"os"
	"path/filepath"
	"testing"
)

func tmpEventsDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "runtime")
	os.MkdirAll(dir, 0755)
	return dir
}

func TestSink_EmitAndRead(t *testing.T) {
	dir := tmpEventsDir(t)
	sink := NewSink(dir)

	sink.Emit("TASK-001", "RUN-001", "queued", "")
	sink.Emit("TASK-001", "RUN-001", "running", "agent=claude")
	sink.Emit("TASK-001", "RUN-001", "completed", "exit_code=0")

	events, err := sink.Read("TASK-001")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if events[0].EventType != "queued" {
		t.Errorf("expected 'queued', got %q", events[0].EventType)
	}
	if events[1].Details != "agent=claude" {
		t.Errorf("expected details 'agent=claude', got %q", events[1].Details)
	}
	if events[2].TaskID != "TASK-001" {
		t.Errorf("wrong task ID: %s", events[2].TaskID)
	}
	if events[0].Sequence != 1 || events[1].Sequence != 2 || events[2].Sequence != 3 {
		t.Fatalf("expected synthetic sequence numbers 1..3, got %+v", events)
	}
}

func TestSink_Read_NoFile(t *testing.T) {
	dir := tmpEventsDir(t)
	sink := NewSink(dir)

	events, err := sink.Read("NONEXISTENT")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if events != nil {
		t.Error("expected nil for nonexistent")
	}
}

func TestSink_Tail(t *testing.T) {
	dir := tmpEventsDir(t)
	sink := NewSink(dir)

	for i := 0; i < 10; i++ {
		sink.Emit("TASK-001", "RUN-001", "event", "")
	}

	events, err := sink.Tail("TASK-001", 3)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
}

func TestSink_Tail_MoreThanTotal(t *testing.T) {
	dir := tmpEventsDir(t)
	sink := NewSink(dir)

	sink.Emit("TASK-001", "RUN-001", "queued", "")
	sink.Emit("TASK-001", "RUN-001", "running", "")

	events, err := sink.Tail("TASK-001", 100)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
}

func TestSink_Tail_NoFile(t *testing.T) {
	dir := tmpEventsDir(t)
	sink := NewSink(dir)

	events, err := sink.Tail("NONEXISTENT", 5)
	if err != nil {
		t.Fatalf("tail: %v", err)
	}
	if events != nil {
		t.Error("expected nil")
	}
}

func TestSink_ReadAfter(t *testing.T) {
	dir := tmpEventsDir(t)
	sink := NewSink(dir)

	for i := 0; i < 5; i++ {
		sink.Emit("TASK-001", "RUN-001", "event", "")
	}

	events, err := sink.ReadAfter("TASK-001", 3, 0)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events after seq 3, got %d", len(events))
	}
	if events[0].Sequence != 4 || events[1].Sequence != 5 {
		t.Fatalf("unexpected sequences: %+v", events)
	}
}
