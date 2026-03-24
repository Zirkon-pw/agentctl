package prompting

import (
	. "github.com/docup/agentctl/internal/service/prompting"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docup/agentctl/internal/core/task"
	"github.com/docup/agentctl/internal/infra/fsstore"
)

func setup(t *testing.T) (*Builder, string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".agentctl")
	os.MkdirAll(filepath.Join(dir, "templates", "prompts"), 0755)
	os.MkdirAll(filepath.Join(dir, "context"), 0755)

	templateStore := fsstore.NewTemplateStore(dir)
	builder := NewBuilder(templateStore, dir)
	return builder, dir, t.TempDir()
}

func TestBuildPrompt_Basic(t *testing.T) {
	builder, agentctlDir, _ := setup(t)

	// Create context file
	contextDir := filepath.Join(agentctlDir, "context", "TASK-001")
	os.MkdirAll(contextDir, 0755)
	os.WriteFile(filepath.Join(contextDir, "context.md"), []byte("# Context\nTest context"), 0644)

	runDir := filepath.Join(t.TempDir(), "run")

	tk := &task.Task{
		ID:    "TASK-001",
		Title: "Test",
		Goal:  "Test goal",
		Agent: "claude",
		PromptTemplates: task.PromptTemplates{
			Builtin: []string{"strict_executor"},
		},
		Validation: task.ValidationConfig{
			Mode:       task.ValidationModeSimple,
			MaxRetries: 3,
			Commands:   []string{"go test ./..."},
		},
		CreatedAt: time.Now(),
	}

	prompt, err := builder.BuildPrompt(tk, contextDir, runDir)
	if err != nil {
		t.Fatalf("build: %v", err)
	}

	if !strings.Contains(prompt, "Strict Executor") {
		t.Error("should contain template name")
	}
	if !strings.Contains(prompt, "REQUIRE explicit scope") {
		t.Error("should contain behavior rule")
	}
	if !strings.Contains(prompt, "Test context") {
		t.Error("should contain context")
	}
	if !strings.Contains(prompt, "go test") {
		t.Error("should contain validation commands")
	}
	if strings.Contains(prompt, "Write execution artifacts under") {
		t.Error("should not tell the agent to write artifacts into runtime directories")
	}
	if !strings.Contains(prompt, "Do NOT write files under .agentctl") {
		t.Error("should explicitly forbid writing into .agentctl")
	}
	if !strings.Contains(prompt, "Do NOT create summary.md, diff.patch, or changed_files.json yourself") {
		t.Error("should state that runtime-owned artifacts are generated automatically")
	}

	// Check template lock was written
	lockPath := filepath.Join(runDir, "prompt_template_lock.yml")
	if _, err := os.Stat(lockPath); err != nil {
		t.Error("template lock file should be created")
	}
}

func TestBuildPrompt_IncompatibleTemplates(t *testing.T) {
	builder, _, _ := setup(t)
	runDir := filepath.Join(t.TempDir(), "run")

	tk := &task.Task{
		ID: "TASK-001",
		PromptTemplates: task.PromptTemplates{
			Builtin: []string{"strict_executor", "research_only"},
		},
		CreatedAt: time.Now(),
	}

	_, err := builder.BuildPrompt(tk, "", runDir)
	if err == nil {
		t.Fatal("expected error for incompatible templates")
	}
	if !strings.Contains(err.Error(), "incompatible") {
		t.Errorf("error should mention 'incompatible', got: %v", err)
	}
}

func TestBuildPrompt_UnknownTemplate(t *testing.T) {
	builder, _, _ := setup(t)
	runDir := filepath.Join(t.TempDir(), "run")

	tk := &task.Task{
		ID: "TASK-001",
		PromptTemplates: task.PromptTemplates{
			Builtin: []string{"nonexistent_template"},
		},
		CreatedAt: time.Now(),
	}

	_, err := builder.BuildPrompt(tk, "", runDir)
	if err == nil {
		t.Fatal("expected error for unknown template")
	}
}

func TestBuildPrompt_FullValidationMode(t *testing.T) {
	builder, agentctlDir, _ := setup(t)
	contextDir := filepath.Join(agentctlDir, "context", "TASK-001")
	os.MkdirAll(contextDir, 0755)
	runDir := filepath.Join(t.TempDir(), "run")

	tk := &task.Task{
		ID: "TASK-001",
		PromptTemplates: task.PromptTemplates{
			Builtin: []string{"clarify_if_needed"},
		},
		Validation: task.ValidationConfig{
			Mode:       task.ValidationModeFull,
			MaxRetries: 5,
			Commands:   []string{"make test"},
		},
		CreatedAt: time.Now(),
	}

	prompt, err := builder.BuildPrompt(tk, contextDir, runDir)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(prompt, "full") {
		t.Error("should mention full mode")
	}
	if !strings.Contains(prompt, "5") {
		t.Error("should mention max retries")
	}
}

func TestBuildPrompt_NoValidation(t *testing.T) {
	builder, agentctlDir, _ := setup(t)
	contextDir := filepath.Join(agentctlDir, "context", "TASK-001")
	os.MkdirAll(contextDir, 0755)
	runDir := filepath.Join(t.TempDir(), "run")

	tk := &task.Task{
		ID: "TASK-001",
		PromptTemplates: task.PromptTemplates{
			Builtin: []string{"strict_executor"},
		},
		Validation: task.ValidationConfig{Commands: []string{}},
		CreatedAt:  time.Now(),
	}

	prompt, err := builder.BuildPrompt(tk, contextDir, runDir)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if strings.Contains(prompt, "# Validation") {
		t.Error("should not contain Validation section when no commands")
	}
}
