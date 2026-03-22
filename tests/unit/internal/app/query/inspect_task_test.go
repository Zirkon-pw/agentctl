package query

import (
	. "github.com/docup/agentctl/internal/app/query"
	"testing"
	"time"

	"github.com/docup/agentctl/internal/core/task"
)

func TestInspectTask_Found(t *testing.T) {
	store := setupStore(t)
	now := time.Now()

	store.Save(&task.Task{
		ID:     "TASK-001",
		Title:  "Inspect me",
		Goal:   "Test goal",
		Status: task.StatusRunning,
		Agent:  "claude",
		PromptTemplates: task.PromptTemplates{
			Builtin: []string{"strict_executor", "clarify_if_needed"},
			Custom:  []string{"my_custom"},
		},
		Guidelines: []string{"backend-guidelines"},
		Scope: task.Scope{
			AllowedPaths:   []string{"src/"},
			ForbiddenPaths: []string{"vendor/"},
		},
		Validation: task.ValidationConfig{
			Mode:       task.ValidationModeFull,
			MaxRetries: 5,
			Commands:   []string{"go test"},
		},
		Runtime:   task.DefaultRuntimeConfig(),
		CreatedAt: now,
		UpdatedAt: now,
	})

	q := NewInspectTask(store)
	detail, err := q.Execute("TASK-001")
	if err != nil {
		t.Fatalf("inspect: %v", err)
	}

	if detail.ID != "TASK-001" {
		t.Errorf("wrong ID: %s", detail.ID)
	}
	if detail.Status != "stage_running" {
		t.Errorf("wrong status: %s", detail.Status)
	}
	if len(detail.Templates) != 3 {
		t.Errorf("expected 3 templates (2 builtin + 1 custom), got %d", len(detail.Templates))
	}
	if detail.Validation.Mode != "full" {
		t.Errorf("expected full mode, got %s", detail.Validation.Mode)
	}
	if detail.Validation.MaxRetries != 5 {
		t.Errorf("expected 5 retries, got %d", detail.Validation.MaxRetries)
	}
	if len(detail.Scope.AllowedPaths) != 1 {
		t.Error("expected 1 allowed path")
	}
}

func TestInspectTask_NotFound(t *testing.T) {
	store := setupStore(t)
	q := NewInspectTask(store)
	_, err := q.Execute("NONEXISTENT")
	if err == nil {
		t.Fatal("expected error")
	}
}
