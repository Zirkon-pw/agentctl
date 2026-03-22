package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	rt "github.com/docup/agentctl/internal/core/runtime"
)

// Registry manages active session tracking and persisted control commands.
type Registry struct {
	baseDir string // .agentctl/runtime
	mu      sync.Mutex
}

// NewRegistry creates a new runtime registry.
func NewRegistry(agentctlDir string) *Registry {
	return &Registry{baseDir: filepath.Join(agentctlDir, "runtime")}
}

// RegisterRun adds or replaces an active session record and creates a task lock.
func (r *Registry) RegisterRun(active rt.ActiveRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	taskDir := filepath.Join(r.baseDir, active.TaskID)
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		return err
	}

	lockPath := filepath.Join(taskDir, "lock")
	if existing, err := os.ReadFile(lockPath); err == nil {
		current := string(existing)
		if current != active.RunID {
			return fmt.Errorf("task %s is already locked (session %s in progress)", active.TaskID, current)
		}
	}
	if err := os.WriteFile(lockPath, []byte(active.RunID), 0644); err != nil {
		return err
	}

	active.SessionID = active.RunID
	active.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(active, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(taskDir, "runtime.json"), data, 0644); err != nil {
		return err
	}
	return r.upsertActiveRun(active)
}

// UpdateRun refreshes the runtime metadata for an active session.
func (r *Registry) UpdateRun(active rt.ActiveRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	taskDir := filepath.Join(r.baseDir, active.TaskID)
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		return err
	}
	active.SessionID = active.RunID
	active.UpdatedAt = time.Now()
	data, err := json.MarshalIndent(active, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(taskDir, "runtime.json"), data, 0644); err != nil {
		return err
	}
	return r.upsertActiveRun(active)
}

// LoadActiveRun returns the current runtime metadata for a task, if present.
func (r *Registry) LoadActiveRun(taskID string) (*rt.ActiveRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, err := os.ReadFile(filepath.Join(r.baseDir, taskID, "runtime.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var active rt.ActiveRun
	if err := json.Unmarshal(data, &active); err != nil {
		return nil, err
	}
	return &active, nil
}

// UnregisterRun removes a run from active tracking and releases the lock.
func (r *Registry) UnregisterRun(taskID, runID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	taskDir := filepath.Join(r.baseDir, taskID)
	_ = os.Remove(filepath.Join(taskDir, "lock"))
	_ = os.Remove(filepath.Join(taskDir, "runtime.json"))
	_ = os.Remove(filepath.Join(taskDir, "heartbeat.json"))
	_ = os.Remove(filepath.Join(taskDir, "control.signal"))
	_ = os.Remove(filepath.Join(taskDir, "commands.ndjson"))

	return r.removeFromActiveRuns(taskID, runID)
}

// GetActiveRuns returns all currently registered active runs.
func (r *Registry) GetActiveRuns() ([]rt.ActiveRun, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	path := filepath.Join(r.baseDir, "active_runs.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var runs []rt.ActiveRun
	if err := json.Unmarshal(data, &runs); err != nil {
		return nil, err
	}
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].StartedAt.Before(runs[j].StartedAt)
	})
	return runs, nil
}

// IsLocked checks if a task has an active lock.
func (r *Registry) IsLocked(taskID string) bool {
	lockPath := filepath.Join(r.baseDir, taskID, "lock")
	_, err := os.Stat(lockPath)
	return err == nil
}

// AppendCommand appends a machine-readable control command for a running session.
func (r *Registry) AppendCommand(cmd rt.ProtocolCommand) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	taskDir := filepath.Join(r.baseDir, cmd.TaskID)
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		return err
	}

	data, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(taskDir, "commands.ndjson"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(taskDir, "control.signal"), []byte(commandTypeToLegacySignal(cmd.Type)), 0644)
}

// CommandsAfter reads queued protocol commands after the given sequence number.
func (r *Registry) CommandsAfter(taskID string, seq int64) ([]rt.ProtocolCommand, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, err := os.ReadFile(filepath.Join(r.baseDir, taskID, "commands.ndjson"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	lines := splitLines(data)
	var commands []rt.ProtocolCommand
	for _, line := range lines {
		if len(line) == 0 {
			continue
		}
		var cmd rt.ProtocolCommand
		if err := json.Unmarshal(line, &cmd); err != nil {
			return nil, err
		}
		if cmd.Seq > seq {
			commands = append(commands, cmd)
		}
	}
	return commands, nil
}

// WriteSignal writes a legacy control signal by translating it into a protocol command.
func (r *Registry) WriteSignal(taskID string, signal rt.Signal) error {
	active, err := r.LoadActiveRun(taskID)
	if err != nil {
		return err
	}
	runID := ""
	stageID := ""
	seq := time.Now().UnixNano()
	if active != nil {
		runID = active.RunID
		stageID = active.StageID
	}
	return r.AppendCommand(rt.ProtocolCommand{
		SessionID: runID,
		TaskID:    taskID,
		RunID:     runID,
		StageID:   stageID,
		Seq:       seq,
		Timestamp: time.Now(),
		Type:      legacySignalToCommandType(signal),
	})
}

// ReadSignal reads the current legacy control signal for a task.
func (r *Registry) ReadSignal(taskID string) (rt.Signal, error) {
	data, err := os.ReadFile(filepath.Join(r.baseDir, taskID, "control.signal"))
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return rt.Signal(data), nil
}

// ClearSignal removes the legacy control signal file.
func (r *Registry) ClearSignal(taskID string) error {
	return os.Remove(filepath.Join(r.baseDir, taskID, "control.signal"))
}

func (r *Registry) upsertActiveRun(active rt.ActiveRun) error {
	path := filepath.Join(r.baseDir, "active_runs.json")
	var runs []rt.ActiveRun
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &runs)
	}

	replaced := false
	for i := range runs {
		if runs[i].TaskID == active.TaskID {
			runs[i] = active
			replaced = true
			break
		}
	}
	if !replaced {
		runs = append(runs, active)
	}

	data, err := json.MarshalIndent(runs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func (r *Registry) removeFromActiveRuns(taskID, runID string) error {
	path := filepath.Join(r.baseDir, "active_runs.json")
	var runs []rt.ActiveRun
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &runs)
	}
	filtered := runs[:0]
	for _, active := range runs {
		if active.TaskID == taskID {
			if runID == "" || active.RunID == runID {
				continue
			}
		}
		filtered = append(filtered, active)
	}
	data, err := json.MarshalIndent(filtered, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func legacySignalToCommandType(signal rt.Signal) rt.ProtocolCommandType {
	switch signal {
	case rt.SignalPause:
		return rt.CommandTypePause
	case rt.SignalKill:
		return rt.CommandTypeKill
	default:
		return rt.CommandTypeCancel
	}
}

func commandTypeToLegacySignal(cmd rt.ProtocolCommandType) string {
	switch cmd {
	case rt.CommandTypePause:
		return string(rt.SignalPause)
	case rt.CommandTypeKill:
		return string(rt.SignalKill)
	case rt.CommandTypeResume:
		return "resume"
	case rt.CommandTypePing:
		return "ping"
	default:
		return string(rt.SignalStop)
	}
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
