package command

import (
	"fmt"
	"time"

	"github.com/docup/agentctl/internal/app/dto"
	"github.com/docup/agentctl/internal/config/loader"
	"github.com/docup/agentctl/internal/core/task"
	"github.com/docup/agentctl/internal/infra/fsstore"
)

// CreateTask handles the task creation use case.
type CreateTask struct {
	taskStore *fsstore.TaskStore
	config    *loader.ProjectConfig
}

// NewCreateTask creates the use case handler.
func NewCreateTask(taskStore *fsstore.TaskStore, config *loader.ProjectConfig) *CreateTask {
	return &CreateTask{taskStore: taskStore, config: config}
}

// Execute creates a new task.
func (c *CreateTask) Execute(req dto.CreateTaskRequest) (*task.Task, error) {
	now := time.Now()
	t := &task.Task{
		Status: task.StatusDraft,
		PromptTemplates: task.PromptTemplates{
			Builtin: []string{},
			Custom:  []string{},
		},
		Scope:      task.Scope{},
		Guidelines: []string{},
		Context:    task.ContextConfig{},
		Constraints: task.Constraints{
			NoBreakingChanges: false,
			RequireTests:      false,
		},
		Interaction: task.Interaction{
			ClarificationStrategy: "by_yml_files",
		},
		Clarifications: task.Clarifications{
			Attached: []string{},
		},
		Runtime: task.DefaultRuntimeConfig(),
		Validation: task.ValidationConfig{
			Mode:       task.ValidationMode(c.config.Validation.DefaultMode),
			MaxRetries: c.config.Validation.DefaultMaxRetries,
			Commands:   c.config.Validation.DefaultCommands,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	if req.TitleSet || req.Title != "" {
		t.Title = req.Title
	}
	if req.GoalSet || req.Goal != "" {
		t.Goal = req.Goal
	}
	if req.AgentSet || req.Agent != "" {
		t.Agent = req.Agent
	}
	if req.TemplatesSet || len(req.Templates) > 0 {
		t.PromptTemplates.Builtin = append([]string(nil), req.Templates...)
	}
	if req.GuidelinesSet || len(req.Guidelines) > 0 {
		t.Guidelines = append([]string(nil), req.Guidelines...)
	}
	if req.AllowedPathsSet || len(req.Scope.AllowedPaths) > 0 {
		t.Scope.AllowedPaths = append([]string(nil), req.Scope.AllowedPaths...)
	}
	if req.ForbiddenPathsSet || len(req.Scope.ForbiddenPaths) > 0 {
		t.Scope.ForbiddenPaths = append([]string(nil), req.Scope.ForbiddenPaths...)
	}
	if req.MustReadSet || len(req.Scope.MustRead) > 0 {
		t.Scope.MustRead = append([]string(nil), req.Scope.MustRead...)
	}

	if t.Title == "" {
		return nil, fmt.Errorf("title is required: use --title to provide a task title")
	}
	if t.Goal == "" {
		return nil, fmt.Errorf("goal is required: use --goal to provide a task goal")
	}

	if err := c.taskStore.Create(t); err != nil {
		return nil, fmt.Errorf("saving task: %w", err)
	}

	return t, nil
}
