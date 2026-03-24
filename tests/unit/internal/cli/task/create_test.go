package task

import (
	"bytes"
	. "github.com/docup/agentctl/internal/cli/task"
	"os"
	"path/filepath"
	"testing"

	"github.com/docup/agentctl/internal/app/command"
	"github.com/docup/agentctl/internal/config/loader"
	"github.com/docup/agentctl/internal/infra/fsstore"
)

func setupCreateCmd(t *testing.T) *command.CreateTask {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".agentctl")
	os.MkdirAll(filepath.Join(dir, "tasks"), 0755)
	store := fsstore.NewTaskStore(dir)
	return command.NewCreateTask(store, loader.DefaultProjectConfig())
}

func TestCreateCmd_Flags(t *testing.T) {
	handler := setupCreateCmd(t)
	cmd := NewCreateCmd(handler)

	if cmd.Use != "create" {
		t.Errorf("expected use 'create', got %q", cmd.Use)
	}

	// Check flags exist
	flags := []string{"title", "goal", "agent", "template", "guideline", "allowed-path", "forbidden-path", "must-read"}
	for _, f := range flags {
		if cmd.Flags().Lookup(f) == nil {
			t.Errorf("flag --%s should exist", f)
		}
	}
}

func TestCreateCmd_RejectsEmptyDraft(t *testing.T) {
	handler := setupCreateCmd(t)
	cmd := NewCreateCmd(handler)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error for empty draft (missing title and goal)")
	}
}

func TestCreateCmd_Success(t *testing.T) {
	handler := setupCreateCmd(t)
	cmd := NewCreateCmd(handler)
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"--title", "Test", "--goal", "Do things"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
}
