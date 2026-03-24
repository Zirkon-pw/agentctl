package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"syscall"
	"time"

	rt "github.com/docup/agentctl/internal/core/runtime"
)

// Registry manages active session tracking and legacy signal files.
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

// WriteSignal writes a simple legacy control signal file for a task.
func (r *Registry) WriteSignal(taskID string, signal rt.Signal) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	taskDir := filepath.Join(r.baseDir, taskID)
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(taskDir, "control.signal"), []byte(signal), 0644)
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
	return r.withActiveRunsLock(func(runs []rt.ActiveRun) ([]rt.ActiveRun, error) {
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
		return runs, nil
	})
}

func (r *Registry) removeFromActiveRuns(taskID, runID string) error {
	return r.withActiveRunsLock(func(runs []rt.ActiveRun) ([]rt.ActiveRun, error) {
		filtered := make([]rt.ActiveRun, 0, len(runs))
		for _, active := range runs {
			if active.TaskID == taskID {
				if runID == "" || active.RunID == runID {
					continue
				}
			}
			filtered = append(filtered, active)
		}
		return filtered, nil
	})
}

func (r *Registry) withActiveRunsLock(fn func([]rt.ActiveRun) ([]rt.ActiveRun, error)) error {
	if err := os.MkdirAll(r.baseDir, 0755); err != nil {
		return err
	}
	lockPath := filepath.Join(r.baseDir, ".active_runs.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("opening active runs lock: %w", err)
	}
	defer lockFile.Close()

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("locking active runs: %w", err)
	}
	defer syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)

	path := filepath.Join(r.baseDir, "active_runs.json")
	var runs []rt.ActiveRun
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &runs)
	}

	result, err := fn(runs)
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}

	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
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
