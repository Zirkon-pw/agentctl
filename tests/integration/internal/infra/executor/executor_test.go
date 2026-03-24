package executor

import (
	"context"
	. "github.com/docup/agentctl/internal/infra/executor"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/docup/agentctl/internal/config/loader"
)

func TestNewAgentExecutor(t *testing.T) {
	cfg := &loader.AgentsConfig{
		Agents: []loader.AgentDef{
			{ID: "claude", Driver: loader.AgentDriverClaude, Command: "claude", Args: []string{"-p"}},
			{ID: "codex", Driver: loader.AgentDriverCodex, Command: "codex", Args: []string{"-q"}},
		},
	}
	exec := NewAgentExecutor(cfg)
	if exec == nil {
		t.Fatal("executor should not be nil")
	}
}

func TestExecute_UnknownAgent(t *testing.T) {
	exec := NewAgentExecutor(&loader.AgentsConfig{})
	_, err := exec.Execute(context.Background(), "unknown", "test", ".")
	if err == nil {
		t.Fatal("expected error for unknown agent")
	}
}

func TestExecute_Echo(t *testing.T) {
	cfg := &loader.AgentsConfig{
		Agents: []loader.AgentDef{
			{ID: "echo", Driver: loader.AgentDriverClaude, Command: "echo", Args: []string{}},
		},
	}
	exec := NewAgentExecutor(cfg)

	result, err := exec.Execute(context.Background(), "echo", "hello world", ".")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("expected exit 0, got %d", result.ExitCode)
	}
	if result.PID == 0 {
		t.Error("PID should be set")
	}
	if result.Stdout == "" {
		t.Error("stdout should not be empty")
	}
}

func TestExecute_NonZeroExit(t *testing.T) {
	cfg := &loader.AgentsConfig{
		Agents: []loader.AgentDef{
			{ID: "fail", Driver: loader.AgentDriverClaude, Command: "sh", Args: []string{"-c", "exit 42; #"}},
		},
	}
	exec := NewAgentExecutor(cfg)

	result, err := exec.Execute(context.Background(), "fail", "", ".")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if result.ExitCode != 42 {
		t.Errorf("expected exit 42, got %d", result.ExitCode)
	}
}

func TestExecute_ContextCancel(t *testing.T) {
	cfg := &loader.AgentsConfig{
		Agents: []loader.AgentDef{
			{ID: "sleep", Driver: loader.AgentDriverClaude, Command: "sleep", Args: []string{}},
		},
	}
	exec := NewAgentExecutor(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err := exec.Execute(ctx, "sleep", "10", ".")
	if err == nil {
		t.Fatal("expected error for canceled context")
	}
}

func TestExecuteWithPromptFile_CreatesPromptAndPassesContract(t *testing.T) {
	root := t.TempDir()
	agentctlDir := filepath.Join(root, ".agentctl")

	exec := NewAgentExecutor(&loader.AgentsConfig{
		Agents: []loader.AgentDef{
			{ID: "echo", Driver: loader.AgentDriverClaude, Command: "echo"},
		},
	})

	result, err := exec.ExecuteWithPromptFile(
		context.Background(),
		"echo",
		"# Prompt body",
		root,
		"TASK-001",
		"RUN-001",
		agentctlDir,
	)
	if err != nil {
		t.Fatalf("execute with prompt file: %v", err)
	}

	promptPath := filepath.Join(agentctlDir, "runs", "TASK-001", "RUN-001", "prompt.md")
	data, err := os.ReadFile(promptPath)
	if err != nil {
		t.Fatalf("read prompt: %v", err)
	}
	if string(data) != "# Prompt body" {
		t.Fatalf("unexpected prompt content: %q", string(data))
	}

	for _, check := range []string{
		"TASK-001",
		"RUN-001",
		"prompt.md",
		"Do NOT modify",
	} {
		if !strings.Contains(result.Stdout, check) {
			t.Fatalf("expected contract to contain %q, got %q", check, result.Stdout)
		}
	}
	if strings.Contains(result.Stdout, "Save artifacts in") {
		t.Fatalf("contract should not ask the agent to save artifacts in .agentctl, got %q", result.Stdout)
	}
	if !strings.Contains(result.Stdout, "Do NOT create runtime-owned artifacts") {
		t.Fatalf("contract should mention runtime-owned artifacts, got %q", result.Stdout)
	}
}
