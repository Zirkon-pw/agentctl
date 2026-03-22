package loader

import (
	"fmt"
	"os"
	"path/filepath"

	rt "github.com/docup/agentctl/internal/core/runtime"
	"gopkg.in/yaml.v3"
)

// ProjectConfig is the root configuration loaded from .agentctl/config.yaml.
type ProjectConfig struct {
	Project        ProjectInfo      `yaml:"project"`
	Execution      ExecutionConfig  `yaml:"execution"`
	Prompting      PromptingConfig  `yaml:"prompting"`
	Clarifications ClarificationCfg `yaml:"clarifications"`
	Runtime        RuntimeCfg       `yaml:"runtime"`
	Validation     ValidationCfg    `yaml:"validation"`
	Artifacts      ArtifactsCfg     `yaml:"artifacts"`
}

type ProjectInfo struct {
	Name     string `yaml:"name"`
	Language string `yaml:"language"`
}

type ExecutionConfig struct {
	DefaultAgent string `yaml:"default_agent"`
	Mode         string `yaml:"mode"`
}

type PromptingConfig struct {
	BuiltinTemplates       []string `yaml:"builtin_templates"`
	DefaultTemplate        string   `yaml:"default_template"`
	AllowMultipleTemplates bool     `yaml:"allow_multiple_templates"`
}

type ClarificationCfg struct {
	Dir                string `yaml:"dir"`
	Strategy           string `yaml:"strategy"`
	AllowMultipleFiles bool   `yaml:"allow_multiple_files"`
}

type RuntimeCfg struct {
	MaxParallelTasks       int  `yaml:"max_parallel_tasks"`
	HeartbeatIntervalSec   int  `yaml:"heartbeat_interval_sec"`
	StaleAfterSec          int  `yaml:"stale_after_sec"`
	GracefulStopTimeoutSec int  `yaml:"graceful_stop_timeout_sec"`
	AllowForceKill         bool `yaml:"allow_force_kill"`
}

type ValidationCfg struct {
	DefaultMode       string   `yaml:"default_mode"`
	DefaultMaxRetries int      `yaml:"default_max_retries"`
	DefaultCommands   []string `yaml:"default_commands"`
}

type ArtifactsCfg struct {
	RunsDir    string `yaml:"runs_dir"`
	ContextDir string `yaml:"context_dir"`
	ReviewsDir string `yaml:"reviews_dir"`
}

// AgentDef describes an available agent from agents.yaml.
type AgentDef struct {
	ID              string                 `yaml:"id"`
	Role            string                 `yaml:"role"`
	Specialization  []string               `yaml:"specialization"`
	Strengths       []string               `yaml:"strengths"`
	Speed           string                 `yaml:"speed"`
	Cost            string                 `yaml:"cost"`
	ContextLimit    string                 `yaml:"context_limit"`
	Modes           []string               `yaml:"modes"`
	Tools           []string               `yaml:"tools"`
	Command         string                 `yaml:"command"`
	Args            []string               `yaml:"args"`
	Transport       string                 `yaml:"transport"`
	AdapterCommand  string                 `yaml:"adapter_command"`
	AdapterArgs     []string               `yaml:"adapter_args"`
	Capabilities    rt.AdapterCapabilities `yaml:"capabilities"`
	ChildCLICommand string                 `yaml:"child_cli_command"`
	ChildCLIArgs    []string               `yaml:"child_cli_args"`
}

// AgentsConfig wraps the list of agents.
type AgentsConfig struct {
	Agents []AgentDef `yaml:"agents"`
}

// RoutingRule defines an agent routing rule.
type RoutingRule struct {
	When  string `yaml:"when"`
	Agent string `yaml:"agent"`
}

// RoutingConfig wraps routing rules.
type RoutingConfig struct {
	Routing []RoutingRule `yaml:"routing"`
}

// LoadProjectConfig reads config.yaml from the .agentctl directory.
func LoadProjectConfig(agentctlDir string) (*ProjectConfig, error) {
	path := filepath.Join(agentctlDir, "config.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config.yaml: %w", err)
	}
	var cfg ProjectConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config.yaml: %w", err)
	}
	return &cfg, nil
}

// LoadAgentsConfig reads agents.yaml.
func LoadAgentsConfig(agentctlDir string) (*AgentsConfig, error) {
	path := filepath.Join(agentctlDir, "agents.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading agents.yaml: %w", err)
	}
	var cfg AgentsConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing agents.yaml: %w", err)
	}
	return &cfg, nil
}

// LoadRoutingConfig reads routing.yaml.
func LoadRoutingConfig(agentctlDir string) (*RoutingConfig, error) {
	path := filepath.Join(agentctlDir, "routing.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading routing.yaml: %w", err)
	}
	var cfg RoutingConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing routing.yaml: %w", err)
	}
	return &cfg, nil
}

// DefaultProjectConfig returns a config with sensible defaults.
func DefaultProjectConfig() *ProjectConfig {
	return &ProjectConfig{
		Project: ProjectInfo{
			Name:     "my-project",
			Language: "go",
		},
		Execution: ExecutionConfig{
			DefaultAgent: "claude",
			Mode:         "strict",
		},
		Prompting: PromptingConfig{
			BuiltinTemplates: []string{
				"clarify_if_needed",
				"plan_before_execution",
				"strict_executor",
				"research_only",
				"review_only",
			},
			DefaultTemplate:        "strict_executor",
			AllowMultipleTemplates: true,
		},
		Clarifications: ClarificationCfg{
			Dir:                ".agentctl/clarifications",
			Strategy:           "by_yml_files",
			AllowMultipleFiles: true,
		},
		Runtime: RuntimeCfg{
			MaxParallelTasks:       4,
			HeartbeatIntervalSec:   5,
			StaleAfterSec:          30,
			GracefulStopTimeoutSec: 20,
			AllowForceKill:         true,
		},
		Validation: ValidationCfg{
			DefaultMode:       "simple",
			DefaultMaxRetries: 3,
			DefaultCommands:   []string{},
		},
		Artifacts: ArtifactsCfg{
			RunsDir:    ".agentctl/runs",
			ContextDir: ".agentctl/context",
			ReviewsDir: ".agentctl/reviews",
		},
	}
}

// DefaultAgentsConfig returns default agent definitions.
func DefaultAgentsConfig() *AgentsConfig {
	return &AgentsConfig{
		Agents: []AgentDef{
			{
				ID:             "claude",
				Role:           "executor",
				Specialization: []string{"architecture_refactor", "deep_analysis"},
				Strengths:      []string{"large_context_reasoning", "architecture_review"},
				Speed:          "medium",
				Cost:           "high",
				ContextLimit:   "large",
				Modes:          []string{"strict", "research"},
				Tools:          []string{"filesystem", "git"},
				Command:        "claude",
				Args:           []string{"-p"},
				Transport:      "ndjson_stdio",
				AdapterCommand: "claude",
				AdapterArgs:    []string{"-p"},
				Capabilities: rt.AdapterCapabilities{
					ProtocolVersion: "v1",
					SupportsCancel:  true,
					SupportsKill:    true,
				},
				ChildCLICommand: "claude",
				ChildCLIArgs:    []string{"-p"},
			},
			{
				ID:             "codex",
				Role:           "executor",
				Specialization: []string{"code_generation", "task_execution"},
				Strengths:      []string{"code_edits", "terminal_workflow"},
				Speed:          "high",
				Cost:           "medium",
				ContextLimit:   "medium",
				Modes:          []string{"strict", "fast"},
				Tools:          []string{"filesystem", "terminal"},
				Command:        "codex",
				Args:           []string{"-q"},
				Transport:      "ndjson_stdio",
				AdapterCommand: "codex",
				AdapterArgs:    []string{"-q"},
				Capabilities: rt.AdapterCapabilities{
					ProtocolVersion: "v1",
					SupportsCancel:  true,
					SupportsKill:    true,
				},
				ChildCLICommand: "codex",
				ChildCLIArgs:    []string{"-q"},
			},
			{
				ID:             "qwen",
				Role:           "executor",
				Specialization: []string{"bulk_tests", "code_generation"},
				Strengths:      []string{"fast_generation", "code_edits"},
				Speed:          "high",
				Cost:           "low",
				ContextLimit:   "medium",
				Modes:          []string{"strict", "fast"},
				Tools:          []string{"filesystem", "terminal"},
				Command:        "qwen",
				Args:           []string{},
				Transport:      "ndjson_stdio",
				AdapterCommand: "qwen",
				AdapterArgs:    []string{},
				Capabilities: rt.AdapterCapabilities{
					ProtocolVersion: "v1",
					SupportsCancel:  true,
					SupportsKill:    true,
				},
				ChildCLICommand: "qwen",
				ChildCLIArgs:    []string{},
			},
		},
	}
}

// DefaultRoutingConfig returns default routing rules.
func DefaultRoutingConfig() *RoutingConfig {
	return &RoutingConfig{
		Routing: []RoutingRule{
			{When: "task.type == \"architecture_refactor\"", Agent: "claude"},
			{When: "task.type == \"code_generation\"", Agent: "codex"},
			{When: "task.type == \"bulk_tests\"", Agent: "qwen"},
		},
	}
}
