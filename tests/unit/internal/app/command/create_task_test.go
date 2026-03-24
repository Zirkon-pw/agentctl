package command

import (
	. "github.com/docup/agentctl/internal/app/command"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/docup/agentctl/internal/app/dto"
	"github.com/docup/agentctl/internal/config/loader"
	"github.com/docup/agentctl/internal/core/task"
	"github.com/docup/agentctl/internal/infra/fsstore"
)

func setupCreateTask(t *testing.T) (*CreateTask, *fsstore.TaskStore) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".agentctl")
	os.MkdirAll(filepath.Join(dir, "tasks"), 0755)
	store := fsstore.NewTaskStore(dir)
	cfg := loader.DefaultProjectConfig()
	handler := NewCreateTask(store, cfg)
	return handler, store
}

func TestCreateTask_Basic(t *testing.T) {
	handler, _ := setupCreateTask(t)

	tk, err := handler.Execute(dto.CreateTaskRequest{
		Title: "Test task",
		Goal:  "Do something",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if tk.ID != "TASK-001" {
		t.Errorf("expected TASK-001, got %s", tk.ID)
	}
	if tk.Status != task.StatusDraft {
		t.Errorf("expected draft, got %s", tk.Status)
	}
	if tk.Agent != "" {
		t.Errorf("expected empty agent, got %s", tk.Agent)
	}
	if len(tk.PromptTemplates.Builtin) != 0 {
		t.Errorf("expected no default templates, got %v", tk.PromptTemplates.Builtin)
	}
}

func TestCreateTask_CustomAgent(t *testing.T) {
	handler, _ := setupCreateTask(t)

	tk, err := handler.Execute(dto.CreateTaskRequest{
		Title: "Test",
		Goal:  "Goal",
		Agent: "codex",
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if tk.Agent != "codex" {
		t.Errorf("expected codex, got %s", tk.Agent)
	}
}

func TestCreateTask_CustomTemplates(t *testing.T) {
	handler, _ := setupCreateTask(t)

	tk, err := handler.Execute(dto.CreateTaskRequest{
		Title:     "Test",
		Goal:      "Goal",
		Templates: []string{"clarify_if_needed", "plan_before_execution"},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(tk.PromptTemplates.Builtin) != 2 {
		t.Errorf("expected 2 templates, got %d", len(tk.PromptTemplates.Builtin))
	}
}

func TestCreateTask_WithScope(t *testing.T) {
	handler, _ := setupCreateTask(t)

	tk, err := handler.Execute(dto.CreateTaskRequest{
		Title: "Test",
		Goal:  "Goal",
		Scope: dto.ScopeDTO{
			AllowedPaths:   []string{"src/"},
			ForbiddenPaths: []string{"vendor/"},
			MustRead:       []string{"README.md"},
		},
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(tk.Scope.AllowedPaths) != 1 {
		t.Error("expected 1 allowed path")
	}
	if len(tk.Scope.ForbiddenPaths) != 1 {
		t.Error("expected 1 forbidden path")
	}
}

func TestCreateTask_Persisted(t *testing.T) {
	handler, store := setupCreateTask(t)

	handler.Execute(dto.CreateTaskRequest{
		Title: "Persisted",
		Goal:  "Check save",
	})

	loaded, err := store.Load("TASK-001")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Title != "Persisted" {
		t.Errorf("wrong title: %s", loaded.Title)
	}
}

func TestCreateTask_EmptyDraft(t *testing.T) {
	handler, _ := setupCreateTask(t)

	_, err := handler.Execute(dto.CreateTaskRequest{})
	if err == nil {
		t.Fatal("expected error for empty draft (missing title)")
	}
	if !strings.Contains(err.Error(), "title is required") {
		t.Errorf("expected title-required error, got %v", err)
	}
}

func TestCreateTask_MissingGoal(t *testing.T) {
	handler, _ := setupCreateTask(t)

	_, err := handler.Execute(dto.CreateTaskRequest{Title: "Test"})
	if err == nil {
		t.Fatal("expected error for missing goal")
	}
	if !strings.Contains(err.Error(), "goal is required") {
		t.Errorf("expected goal-required error, got %v", err)
	}
}

func TestCreateTask_IncrementingIDs(t *testing.T) {
	handler, _ := setupCreateTask(t)

	t1, _ := handler.Execute(dto.CreateTaskRequest{Title: "A", Goal: "A"})
	t2, _ := handler.Execute(dto.CreateTaskRequest{Title: "B", Goal: "B"})

	if t1.ID != "TASK-001" {
		t.Errorf("first: expected TASK-001, got %s", t1.ID)
	}
	if t2.ID != "TASK-002" {
		t.Errorf("second: expected TASK-002, got %s", t2.ID)
	}
}

func TestCreateTask_ConcurrentExecuteAssignsUniqueIDs(t *testing.T) {
	handler, _ := setupCreateTask(t)

	const total = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	ids := make(chan string, total)
	errs := make(chan error, total)

	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			tk, err := handler.Execute(dto.CreateTaskRequest{
				Title: "Task",
				Goal:  "Concurrent create",
			})
			if err != nil {
				errs <- err
				return
			}
			ids <- tk.ID
		}(i)
	}

	close(start)
	wg.Wait()
	close(ids)
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent create failed: %v", err)
		}
	}

	seen := make(map[string]bool, total)
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate task id allocated: %s", id)
		}
		seen[id] = true
	}
	if len(seen) != total {
		t.Fatalf("expected %d unique tasks, got %d", total, len(seen))
	}
}
