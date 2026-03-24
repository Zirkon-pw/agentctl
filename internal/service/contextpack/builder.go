package contextpack

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/docup/agentctl/internal/core/task"
	"github.com/docup/agentctl/internal/service/workspace"
)

// Builder assembles the context pack for a task.
type Builder struct {
	agentctlDir string
	projectRoot string
}

// NewBuilder creates a context pack builder.
func NewBuilder(agentctlDir, projectRoot string) *Builder {
	return &Builder{agentctlDir: agentctlDir, projectRoot: projectRoot}
}

// Build assembles the context pack directory for a task.
func (b *Builder) Build(t *task.Task) (string, error) {
	contextDir := filepath.Join(b.agentctlDir, "context", t.ID)
	if err := os.MkdirAll(contextDir, 0755); err != nil {
		return "", fmt.Errorf("creating context dir: %w", err)
	}

	var sections []string

	// Task specification
	sections = append(sections, "# Task Specification")
	sections = append(sections, fmt.Sprintf("ID: %s", t.ID))
	sections = append(sections, fmt.Sprintf("Title: %s", t.Title))
	sections = append(sections, fmt.Sprintf("Goal: %s", t.Goal))
	sections = append(sections, fmt.Sprintf("Agent: %s", t.Agent))
	sections = append(sections, "")

	// Scope
	if len(t.Scope.AllowedPaths) > 0 || len(t.Scope.ForbiddenPaths) > 0 {
		sections = append(sections, "# Scope")
		if len(t.Scope.AllowedPaths) > 0 {
			sections = append(sections, "## Allowed Paths")
			for _, p := range t.Scope.AllowedPaths {
				sections = append(sections, fmt.Sprintf("- %s", p))
			}
		}
		if len(t.Scope.ForbiddenPaths) > 0 {
			sections = append(sections, "## Forbidden Paths")
			for _, p := range t.Scope.ForbiddenPaths {
				sections = append(sections, fmt.Sprintf("- %s", p))
			}
		}
		sections = append(sections, "")
	}

	// Constraints
	sections = append(sections, "# Constraints")
	sections = append(sections, fmt.Sprintf("- No breaking changes: %v", t.Constraints.NoBreakingChanges))
	sections = append(sections, fmt.Sprintf("- Require tests: %v", t.Constraints.RequireTests))
	sections = append(sections, "")

	// Guidelines
	if len(t.Guidelines) > 0 {
		sections = append(sections, "# Guidelines")
		for _, name := range t.Guidelines {
			content, err := workspace.LoadGuideline(b.agentctlDir, name)
			if err != nil {
				return "", fmt.Errorf("loading guideline %q: %w", name, err)
			}
			sections = append(sections, fmt.Sprintf("## %s\n%s", name, content))
		}
		sections = append(sections, "")
	}

	// Must-read files
	if len(t.Scope.MustRead) > 0 {
		sections = append(sections, "# Must-Read Files")
		for _, path := range t.Scope.MustRead {
			fullPath := filepath.Join(b.projectRoot, path)
			content, err := os.ReadFile(fullPath)
			if err != nil {
				return "", fmt.Errorf("reading must-read file %q: %w", path, err)
			}
			sections = append(sections, fmt.Sprintf("## %s\n```\n%s\n```", path, string(content)))
		}
		sections = append(sections, "")
	}

	// Include files from context config
	if len(t.Context.IncludeFiles) > 0 {
		sections = append(sections, "# Included Files")
		for _, path := range t.Context.IncludeFiles {
			fullPath := filepath.Join(b.projectRoot, path)
			content, err := os.ReadFile(fullPath)
			if err != nil {
				return "", fmt.Errorf("reading include file %q: %w", path, err)
			}
			sections = append(sections, fmt.Sprintf("## %s\n```\n%s\n```", path, string(content)))
		}
		sections = append(sections, "")
	}

	// Clarifications
	if len(t.Clarifications.Attached) > 0 {
		sections = append(sections, "# Attached Clarifications")
		for _, path := range t.Clarifications.Attached {
			content, err := os.ReadFile(path)
			if err != nil {
				sections = append(sections, fmt.Sprintf("(error reading clarification: %v)", err))
			} else {
				sections = append(sections, fmt.Sprintf("```yaml\n%s\n```", string(content)))
			}
		}
		sections = append(sections, "")
	}

	// Write context.md
	contextContent := strings.Join(sections, "\n")
	contextPath := filepath.Join(contextDir, "context.md")
	if err := os.WriteFile(contextPath, []byte(contextContent), 0644); err != nil {
		return "", fmt.Errorf("writing context.md: %w", err)
	}

	return contextDir, nil
}
