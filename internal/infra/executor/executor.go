package executor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/docup/agentctl/internal/config/loader"
)

// AgentExecutor runs an agent CLI tool with a prompt.
type AgentExecutor struct {
	agents map[string]loader.AgentDef
}

// NewAgentExecutor creates an executor with agent definitions.
func NewAgentExecutor(agentsCfg *loader.AgentsConfig) *AgentExecutor {
	agents := make(map[string]loader.AgentDef)
	for _, a := range agentsCfg.Agents {
		agents[a.ID] = a
	}
	return &AgentExecutor{agents: agents}
}

// ExecuteResult holds the result of an agent execution.
type ExecuteResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	PID      int
}

// Execute runs the agent CLI with the given prompt in the specified working directory.
func (e *AgentExecutor) Execute(ctx context.Context, agentID, prompt, workDir string) (*ExecuteResult, error) {
	agent, ok := e.agents[agentID]
	if !ok {
		return nil, fmt.Errorf("unknown agent: %s", agentID)
	}

	args := make([]string, len(agent.Args))
	copy(args, agent.Args)
	args = append(args, prompt)

	cmd := exec.CommandContext(ctx, agent.Command, args...)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	for key, value := range agent.Env {
		cmd.Env = append(cmd.Env, key+"="+value)
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting agent %s: %w", agentID, err)
	}

	pid := cmd.Process.Pid
	err := cmd.Wait()

	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("waiting for agent %s: %w", agentID, err)
		}
	}

	return &ExecuteResult{
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		PID:      pid,
	}, nil
}

// ExecuteWithPromptFile writes the prompt to a file and passes a short contract to the agent.
func (e *AgentExecutor) ExecuteWithPromptFile(ctx context.Context, agentID, promptContent, workDir, taskID, runID, agentctlDir string) (*ExecuteResult, error) {
	// Write prompt file
	runDir := filepath.Join(agentctlDir, "runs", taskID, runID)
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return nil, err
	}
	promptPath := filepath.Join(runDir, "prompt.md")
	if err := os.WriteFile(promptPath, []byte(promptContent), 0644); err != nil {
		return nil, err
	}

	// Build short execution contract
	contract := buildContract(taskID, runID, agentctlDir)
	return e.Execute(ctx, agentID, contract, workDir)
}

// buildContract creates the short instruction the agent receives.
func buildContract(taskID, runID, agentctlDir string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Execute task: %s/tasks/%s.yml\n", agentctlDir, taskID))
	sb.WriteString(fmt.Sprintf("Context available in: %s/context/%s/\n", agentctlDir, taskID))
	sb.WriteString(fmt.Sprintf("Prompt: %s/runs/%s/%s/prompt.md\n", agentctlDir, taskID, runID))
	sb.WriteString("\nIMPORTANT: Read the prompt.md file for full instructions. ")
	sb.WriteString("Read the task YAML for scope, constraints, and validation rules. ")
	sb.WriteString("Modify only workspace files. ")
	sb.WriteString("Do NOT modify any files inside .agentctl/. ")
	sb.WriteString("Do NOT create runtime-owned artifacts such as diff.patch, summary.md, or changed_files.json; the runtime generates them.")
	return sb.String()
}
