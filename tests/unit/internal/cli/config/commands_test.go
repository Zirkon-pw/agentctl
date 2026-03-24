package config

import (
	"testing"

	. "github.com/docup/agentctl/internal/cli/config"
	"github.com/docup/agentctl/internal/config/global"
)

func setupConfigEnv(t *testing.T) {
	t.Helper()
	t.Setenv(global.EnvVar, t.TempDir())
}

func TestConfigSetCmd_AcceptsSeparatedKeyValue(t *testing.T) {
	setupConfigEnv(t)

	cmd := NewConfigCmd()
	cmd.SetArgs([]string{"set", "execution.default_agent", "codex"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	cfg, err := global.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Execution.DefaultAgent != "codex" {
		t.Fatalf("expected codex, got %q", cfg.Execution.DefaultAgent)
	}
}

func TestConfigSetCmd_AcceptsEqualsSyntax(t *testing.T) {
	setupConfigEnv(t)

	cmd := NewConfigCmd()
	cmd.SetArgs([]string{"set", "prompting.builtin_templates=clarify_if_needed,review_only"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	cfg, err := global.LoadConfig()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if len(cfg.Prompting.BuiltinTemplates) != 2 {
		t.Fatalf("expected 2 builtin templates, got %d", len(cfg.Prompting.BuiltinTemplates))
	}
	if cfg.Prompting.BuiltinTemplates[0] != "clarify_if_needed" || cfg.Prompting.BuiltinTemplates[1] != "review_only" {
		t.Fatalf("unexpected builtin templates: %v", cfg.Prompting.BuiltinTemplates)
	}
}

func TestConfigSetCmd_RejectsInvalidSyntax(t *testing.T) {
	setupConfigEnv(t)

	cmd := NewConfigCmd()
	cmd.SetArgs([]string{"set", "execution.default_agent"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected error for missing value")
	}
}
