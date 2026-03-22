package query

import (
	. "github.com/docup/agentctl/internal/app/query"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/docup/agentctl/internal/core/task"
	"github.com/docup/agentctl/internal/infra/fsstore"
)

func setupStore(t *testing.T) *fsstore.TaskStore {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".agentctl")
	os.MkdirAll(filepath.Join(dir, "tasks"), 0755)
	return fsstore.NewTaskStore(dir)
}

func TestListTasks_Empty(t *testing.T) {
	store := setupStore(t)
	q := NewListTasks(store)
	results, err := q.Execute()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0, got %d", len(results))
	}
}

func TestListTasks_Multiple(t *testing.T) {
	store := setupStore(t)
	now := time.Now()

	store.Save(&task.Task{ID: "TASK-001", Title: "First", Status: task.StatusDraft, Agent: "claude", CreatedAt: now})
	store.Save(&task.Task{ID: "TASK-002", Title: "Second", Status: task.StatusRunning, Agent: "codex", CreatedAt: now.Add(time.Second)})

	q := NewListTasks(store)
	results, err := q.Execute()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2, got %d", len(results))
	}

	// Check fields mapped correctly
	found := false
	for _, r := range results {
		if r.ID == "TASK-002" {
			found = true
			if r.Status != "stage_running" {
				t.Errorf("expected stage_running, got %s", r.Status)
			}
			if r.Agent != "codex" {
				t.Errorf("expected codex, got %s", r.Agent)
			}
		}
	}
	if !found {
		t.Error("TASK-002 not found in results")
	}
}
