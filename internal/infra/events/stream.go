package events

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/docup/agentctl/internal/core/runtime"
)

// Sink writes events to an NDJSON file.
type Sink struct {
	baseDir string // .agentctl/runtime or .agentctl/runs
	mu      sync.Mutex
	nextSeq map[string]int64
}

// NewSink creates an event sink.
func NewSink(baseDir string) *Sink {
	return &Sink{
		baseDir: baseDir,
		nextSeq: make(map[string]int64),
	}
}

// Emit writes an event to the events.ndjson file for a task.
func (s *Sink) Emit(taskID, runID, eventType, details string) error {
	return s.EmitEvent(runtime.Event{
		Timestamp: time.Now(),
		TaskID:    taskID,
		RunID:     runID,
		EventType: eventType,
		Details:   details,
	})
}

// EmitEvent writes a fully populated event to the events.ndjson file.
func (s *Sink) EmitEvent(event runtime.Event) error {
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if event.Sequence == 0 {
		next, err := s.nextSequenceLocked(event.TaskID)
		if err != nil {
			return err
		}
		event.Sequence = next
		s.nextSeq[event.TaskID] = next
	}

	data, err := json.Marshal(event)
	if err != nil {
		return err
	}

	dir := filepath.Join(s.baseDir, event.TaskID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	path := filepath.Join(dir, "events.ndjson")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = fmt.Fprintf(f, "%s\n", data)
	return err
}

func (s *Sink) nextSequenceLocked(taskID string) (int64, error) {
	if last, ok := s.nextSeq[taskID]; ok {
		return last + 1, nil
	}

	events, err := s.Read(taskID)
	if err != nil {
		return 0, err
	}
	var last int64
	for i, ev := range events {
		seq := ev.Sequence
		if seq == 0 {
			seq = int64(i + 1)
		}
		if seq > last {
			last = seq
		}
	}
	s.nextSeq[taskID] = last
	return last + 1, nil
}

// Read reads all events for a task.
func (s *Sink) Read(taskID string) ([]runtime.Event, error) {
	path := filepath.Join(s.baseDir, taskID, "events.ndjson")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var events []runtime.Event
	lines := splitLines(data)
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var ev runtime.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Sequence == 0 {
			ev.Sequence = int64(len(events) + 1)
		}
		events = append(events, ev)
	}
	return events, nil
}

// Tail returns the last N events for a task.
func (s *Sink) Tail(taskID string, n int) ([]runtime.Event, error) {
	events, err := s.Read(taskID)
	if err != nil {
		return nil, err
	}
	if len(events) <= n {
		return events, nil
	}
	return events[len(events)-n:], nil
}

// ReadAfter reads events with sequence greater than afterSeq.
func (s *Sink) ReadAfter(taskID string, afterSeq int64, limit int) ([]runtime.Event, error) {
	events, err := s.Read(taskID)
	if err != nil {
		return nil, err
	}

	filtered := make([]runtime.Event, 0, len(events))
	for _, ev := range events {
		if ev.Sequence <= afterSeq {
			continue
		}
		filtered = append(filtered, ev)
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}
	return filtered, nil
}

func splitLines(data []byte) [][]byte {
	var lines [][]byte
	start := 0
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, data[start:i])
			start = i + 1
		}
	}
	if start < len(data) {
		lines = append(lines, data[start:])
	}
	return lines
}
