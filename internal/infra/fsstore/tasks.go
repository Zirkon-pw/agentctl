package fsstore

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/docup/agentctl/internal/core/task"
	"gopkg.in/yaml.v3"
)

// TaskStore handles reading and writing task YAML files.
type TaskStore struct {
	baseDir string // .agentctl/tasks
}

// NewTaskStore creates a new TaskStore.
func NewTaskStore(agentctlDir string) *TaskStore {
	return &TaskStore{baseDir: filepath.Join(agentctlDir, "tasks")}
}

// Save writes a task to disk as YAML.
func (s *TaskStore) Save(t *task.Task) error {
	if err := os.MkdirAll(s.baseDir, 0755); err != nil {
		return fmt.Errorf("creating tasks dir: %w", err)
	}
	data, err := yaml.Marshal(t)
	if err != nil {
		return fmt.Errorf("marshaling task %s: %w", t.ID, err)
	}
	path := filepath.Join(s.baseDir, t.ID+".yml")
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("writing task %s: %w", t.ID, err)
	}
	return nil
}

// Create atomically allocates an ID for a new task and persists it.
func (s *TaskStore) Create(t *task.Task) error {
	if err := os.MkdirAll(s.baseDir, 0755); err != nil {
		return fmt.Errorf("creating tasks dir: %w", err)
	}

	return s.withLock(func() error {
		for {
			autoAllocated := false
			if t.ID == "" {
				id, err := s.nextIDUnlocked()
				if err != nil {
					return err
				}
				t.ID = id
				autoAllocated = true
			}

			data, err := yaml.Marshal(t)
			if err != nil {
				return fmt.Errorf("marshaling task %s: %w", t.ID, err)
			}

			path := filepath.Join(s.baseDir, t.ID+".yml")
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
			if err != nil {
				if os.IsExist(err) && autoAllocated {
					t.ID = ""
					continue
				}
				if os.IsExist(err) {
					return fmt.Errorf("task %s already exists", t.ID)
				}
				return fmt.Errorf("writing task %s: %w", t.ID, err)
			}

			_, writeErr := f.Write(data)
			closeErr := f.Close()
			if writeErr != nil {
				return fmt.Errorf("writing task %s: %w", t.ID, writeErr)
			}
			if closeErr != nil {
				return fmt.Errorf("closing task %s: %w", t.ID, closeErr)
			}
			return nil
		}
	})
}

// Load reads a task from disk by ID.
func (s *TaskStore) Load(id string) (*task.Task, error) {
	path := filepath.Join(s.baseDir, id+".yml")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("task %s not found", id)
		}
		return nil, fmt.Errorf("reading task %s: %w", id, err)
	}
	var t task.Task
	if err := yaml.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("parsing task %s: %w", id, err)
	}
	return &t, nil
}

// List returns all tasks sorted by creation time (newest first).
func (s *TaskStore) List() ([]*task.Task, error) {
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("listing tasks: %w", err)
	}
	var tasks []*task.Task
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yml") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".yml")
		t, err := s.Load(id)
		if err != nil {
			continue
		}
		tasks = append(tasks, t)
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt.After(tasks[j].CreatedAt)
	})
	return tasks, nil
}

// Exists checks if a task file exists.
func (s *TaskStore) Exists(id string) bool {
	path := filepath.Join(s.baseDir, id+".yml")
	_, err := os.Stat(path)
	return err == nil
}

// NextID generates the next task ID based on existing tasks.
func (s *TaskStore) NextID() (string, error) {
	var id string
	err := s.withLock(func() error {
		nextID, err := s.nextIDUnlocked()
		if err != nil {
			return err
		}
		id = nextID
		return nil
	})
	if err != nil {
		return "", err
	}
	return id, nil
}

func (s *TaskStore) nextIDUnlocked() (string, error) {
	entries, err := os.ReadDir(s.baseDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "TASK-001", nil
		}
		return "", err
	}
	maxNum := 0
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".yml")
		if strings.HasPrefix(name, "TASK-") {
			numStr := strings.TrimPrefix(name, "TASK-")
			var num int
			if _, err := fmt.Sscanf(numStr, "%d", &num); err == nil {
				if num > maxNum {
					maxNum = num
				}
			}
		}
	}
	return fmt.Sprintf("TASK-%03d", maxNum+1), nil
}

func (s *TaskStore) withLock(fn func() error) error {
	if err := os.MkdirAll(s.baseDir, 0755); err != nil {
		return fmt.Errorf("creating tasks dir: %w", err)
	}

	lockPath := filepath.Join(s.baseDir, ".lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("opening task store lock: %w", err)
	}
	defer lockFile.Close()

	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return fmt.Errorf("locking task store: %w", err)
	}
	defer func() {
		if unlockErr := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN); unlockErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to unlock task store: %v\n", unlockErr)
		}
	}()

	return fn()
}
