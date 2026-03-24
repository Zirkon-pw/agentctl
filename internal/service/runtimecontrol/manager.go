package runtimecontrol

import (
	"fmt"
	"time"

	rt "github.com/docup/agentctl/internal/core/runtime"
	"github.com/docup/agentctl/internal/infra/events"
	infrart "github.com/docup/agentctl/internal/infra/runtime"
)

// Manager provides runtime observation and control.
type Manager struct {
	registry     *infrart.Registry
	heartbeatMgr *infrart.HeartbeatManager
	eventSink    *events.Sink
	staleAfter   time.Duration
}

// NewManager creates a runtime control manager.
func NewManager(registry *infrart.Registry, heartbeatMgr *infrart.HeartbeatManager, eventSink *events.Sink, staleAfterSec int) *Manager {
	return &Manager{
		registry:     registry,
		heartbeatMgr: heartbeatMgr,
		eventSink:    eventSink,
		staleAfter:   time.Duration(staleAfterSec) * time.Second,
	}
}

// ActiveRuns returns all currently active runs.
func (m *Manager) ActiveRuns() ([]rt.ActiveRun, error) {
	return m.registry.GetActiveRuns()
}

// TaskEvents returns events for a task.
func (m *Manager) TaskEvents(taskID string, tail int) ([]rt.Event, error) {
	if tail > 0 {
		return m.eventSink.Tail(taskID, tail)
	}
	return m.eventSink.Read(taskID)
}

// TaskEventsAfter returns events for a task after the given sequence cursor.
func (m *Manager) TaskEventsAfter(taskID string, afterSeq int64, limit int) ([]rt.Event, error) {
	return m.eventSink.ReadAfter(taskID, afterSeq, limit)
}

// TaskHeartbeat returns the heartbeat for a task and whether it's stale.
func (m *Manager) TaskHeartbeat(taskID string) (*rt.Heartbeat, bool, error) {
	hb, err := m.heartbeatMgr.Read(taskID)
	if err != nil {
		return nil, false, err
	}
	return hb, hb.IsStale(m.staleAfter), nil
}

// IsRunning checks if a task has an active run.
func (m *Manager) IsRunning(taskID string) bool {
	return m.registry.IsLocked(taskID)
}

// InspectInfo holds detailed information about a task's runtime state.
type InspectInfo struct {
	TaskID    string        `json:"task_id"`
	IsRunning bool          `json:"is_running"`
	Heartbeat *rt.Heartbeat `json:"heartbeat,omitempty"`
	IsStale   bool          `json:"is_stale"`
	Events    []rt.Event    `json:"recent_events"`
	ActiveRun *rt.ActiveRun `json:"active_run,omitempty"`
}

// Inspect returns detailed runtime info for a task.
func (m *Manager) Inspect(taskID string) (*InspectInfo, error) {
	info := &InspectInfo{
		TaskID:    taskID,
		IsRunning: m.IsRunning(taskID),
	}

	if hb, stale, err := m.TaskHeartbeat(taskID); err == nil {
		info.Heartbeat = hb
		info.IsStale = stale
	}

	if events, err := m.eventSink.Tail(taskID, 10); err == nil {
		info.Events = events
	}

	if runs, err := m.registry.GetActiveRuns(); err == nil {
		for _, r := range runs {
			if r.TaskID == taskID {
				r := r
				info.ActiveRun = &r
				break
			}
		}
	}

	// Stale detection warning
	if info.IsRunning && info.IsStale {
		fmt.Printf("WARNING: task %s heartbeat is stale (>%v)\n", taskID, m.staleAfter)
	}

	return info, nil
}
