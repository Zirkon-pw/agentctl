package contextpack

import (
	. "github.com/docup/agentctl/internal/service/contextpack"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docup/agentctl/internal/core/task"
)

func setup(t *testing.T) (string, string, *Builder) {
	t.Helper()
	root := t.TempDir()
	agentctlDir := filepath.Join(root, ".agentctl")
	os.MkdirAll(filepath.Join(agentctlDir, "context"), 0755)
	os.MkdirAll(filepath.Join(agentctlDir, "guidelines"), 0755)
	builder := NewBuilder(agentctlDir, root)
	return root, agentctlDir, builder
}

func TestBuild_Basic(t *testing.T) {
	_, _, builder := setup(t)

	tk := &task.Task{
		ID:    "TASK-001",
		Title: "Test",
		Goal:  "Build context",
		Agent: "claude",
		Constraints: task.Constraints{
			NoBreakingChanges: true,
			RequireTests:      true,
		},
		CreatedAt: time.Now(),
	}

	dir, err := builder.Build(tk)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	contextPath := filepath.Join(dir, "context.md")
	data, err := os.ReadFile(contextPath)
	if err != nil {
		t.Fatalf("read context.md: %v", err)
	}

	content := string(data)
	if !strings.Contains(content, "TASK-001") {
		t.Error("should contain task ID")
	}
	if !strings.Contains(content, "Build context") {
		t.Error("should contain goal")
	}
	if !strings.Contains(content, "No breaking changes: true") {
		t.Error("should contain constraints")
	}
}

func TestBuild_WithScope(t *testing.T) {
	_, _, builder := setup(t)

	tk := &task.Task{
		ID:    "TASK-001",
		Title: "Scoped",
		Goal:  "Test scope",
		Agent: "claude",
		Scope: task.Scope{
			AllowedPaths:   []string{"src/auth/**"},
			ForbiddenPaths: []string{"src/billing/**"},
		},
		CreatedAt: time.Now(),
	}

	dir, err := builder.Build(tk)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "context.md"))
	content := string(data)
	if !strings.Contains(content, "src/auth/**") {
		t.Error("should contain allowed paths")
	}
	if !strings.Contains(content, "src/billing/**") {
		t.Error("should contain forbidden paths")
	}
}

func TestBuild_WithGuidelines(t *testing.T) {
	_, agentctlDir, builder := setup(t)

	os.WriteFile(
		filepath.Join(agentctlDir, "guidelines", "backend-guidelines.md"),
		[]byte("# Backend Rules\nUse DDD patterns."),
		0644,
	)

	tk := &task.Task{
		ID:         "TASK-001",
		Title:      "With guidelines",
		Goal:       "Test",
		Agent:      "claude",
		Guidelines: []string{"backend-guidelines"},
		CreatedAt:  time.Now(),
	}

	dir, err := builder.Build(tk)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "context.md"))
	content := string(data)
	if !strings.Contains(content, "Use DDD patterns") {
		t.Error("should contain guideline content")
	}
}

func TestBuild_WithMustReadFiles(t *testing.T) {
	root, _, builder := setup(t)

	os.WriteFile(filepath.Join(root, "important.txt"), []byte("important content"), 0644)

	tk := &task.Task{
		ID:    "TASK-001",
		Title: "Must read",
		Goal:  "Test",
		Agent: "claude",
		Scope: task.Scope{
			MustRead: []string{"important.txt"},
		},
		CreatedAt: time.Now(),
	}

	dir, err := builder.Build(tk)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, "context.md"))
	if !strings.Contains(string(data), "important content") {
		t.Error("should contain must-read file content")
	}
}

func TestBuild_MissingGuideline(t *testing.T) {
	_, _, builder := setup(t)

	tk := &task.Task{
		ID:         "TASK-001",
		Title:      "Missing",
		Goal:       "Test",
		Agent:      "claude",
		Guidelines: []string{"nonexistent"},
		CreatedAt:  time.Now(),
	}

	_, err := builder.Build(tk)
	if err == nil {
		t.Fatal("build should fail for missing guidelines")
	}
	if !strings.Contains(err.Error(), "nonexistent") {
		t.Errorf("error should mention the missing guideline name, got: %v", err)
	}
}
