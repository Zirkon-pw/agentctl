package prompting

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/docup/agentctl/internal/config/builtin_templates"
	"github.com/docup/agentctl/internal/core/task"
	"github.com/docup/agentctl/internal/core/template"
	"github.com/docup/agentctl/internal/infra/fsstore"
	"gopkg.in/yaml.v3"
)

// Builder constructs the final prompt for agent execution.
type Builder struct {
	templateStore *fsstore.TemplateStore
	agentctlDir   string
}

// NewBuilder creates a prompt builder.
func NewBuilder(templateStore *fsstore.TemplateStore, agentctlDir string) *Builder {
	return &Builder{templateStore: templateStore, agentctlDir: agentctlDir}
}

// BuildPrompt creates the final prompt content and writes template lock.
func (b *Builder) BuildPrompt(t *task.Task, contextDir, runDir string) (string, error) {
	templates, err := b.resolveTemplates(t)
	if err != nil {
		return "", err
	}

	// Check compatibility
	for i := 0; i < len(templates); i++ {
		for j := i + 1; j < len(templates); j++ {
			if !templates[i].IsCompatibleWith(templates[j]) {
				return "", fmt.Errorf("incompatible templates: %s and %s", templates[i].ID, templates[j].ID)
			}
		}
	}

	// Build prompt sections
	var sections []string

	// Behavior rules from templates
	sections = append(sections, "# Behavior Rules")
	for _, tmpl := range templates {
		sections = append(sections, fmt.Sprintf("## Template: %s", tmpl.Name))
		sections = append(sections, tmpl.Description)
		sections = append(sections, b.formatBehavior(tmpl.Behavior))
		sections = append(sections, "")
	}

	// Context reference
	contextPath := filepath.Join(contextDir, "context.md")
	if content, err := os.ReadFile(contextPath); err == nil {
		sections = append(sections, string(content))
	}

	// Validation rules
	if len(t.Validation.Commands) > 0 {
		sections = append(sections, "# Validation")
		sections = append(sections, fmt.Sprintf("Mode: %s", t.Validation.Mode))
		if t.Validation.Mode == task.ValidationModeFull {
			sections = append(sections, fmt.Sprintf("Max retries: %d", t.Validation.MaxRetries))
		}
		sections = append(sections, "Commands:")
		for _, cmd := range t.Validation.Commands {
			sections = append(sections, fmt.Sprintf("- %s", cmd))
		}
		sections = append(sections, "")
	}

	// Output expectations
	sections = append(sections, "# Expected Output")
	sections = append(sections, "Execution contract:")
	sections = append(sections, "- Create requested deliverables only inside the workspace/project directory (AGENTCTL_WORK_DIR).")
	sections = append(sections, "- Do NOT write files under .agentctl or any runtime-managed path such as AGENTCTL_SESSION_DIR, AGENTCTL_STAGE_DIR, or AGENTCTL_CONTEXT_DIR.")
	sections = append(sections, "- Do NOT create summary.md, diff.patch, or changed_files.json yourself. The runtime generates those artifacts automatically.")
	sections = append(sections, "- Your responsibility is to make the required workspace changes and return the final structured result envelope.")

	promptContent := strings.Join(sections, "\n")

	// Write template lock
	if err := b.writeTemplateLock(templates, runDir); err != nil {
		return "", err
	}

	return promptContent, nil
}

func (b *Builder) resolveTemplates(t *task.Task) ([]*template.PromptTemplate, error) {
	var templates []*template.PromptTemplate

	for _, id := range t.PromptTemplates.Builtin {
		tmpl := builtin_templates.ByID(id)
		if tmpl == nil {
			return nil, fmt.Errorf("unknown built-in template: %s", id)
		}
		templates = append(templates, tmpl)
	}

	for _, id := range t.PromptTemplates.Custom {
		tmpl, err := b.templateStore.Load(id)
		if err != nil {
			return nil, fmt.Errorf("loading custom template %s: %w", id, err)
		}
		templates = append(templates, tmpl)
	}

	return templates, nil
}

func (b *Builder) formatBehavior(beh template.Behavior) string {
	var rules []string
	if beh.RequireExplicitScope {
		rules = append(rules, "- REQUIRE explicit scope: only modify files within allowed paths")
	}
	if beh.ClarificationIfAmbiguous {
		rules = append(rules, "- If ambiguous: create clarification_request YAML instead of guessing")
	}
	if !beh.AllowNonBlockingAssumptions {
		rules = append(rules, "- Do NOT make assumptions — ask for clarification")
	}
	if beh.PlanBeforeExecution {
		rules = append(rules, "- Create a detailed plan BEFORE making any code changes")
	}
	if !beh.CodeChangesAllowed {
		rules = append(rules, "- NO code changes allowed — analysis/review only")
	}
	if beh.ReviewMode {
		rules = append(rules, "- REVIEW mode: analyze diff, summary, validation and provide feedback")
	}
	if beh.ResearchOnly {
		rules = append(rules, "- RESEARCH only: analyze, propose, but do not modify code")
	}
	return strings.Join(rules, "\n")
}

func (b *Builder) writeTemplateLock(templates []*template.PromptTemplate, runDir string) error {
	if err := os.MkdirAll(runDir, 0755); err != nil {
		return err
	}
	data, err := yaml.Marshal(templates)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(runDir, "prompt_template_lock.yml"), data, 0644)
}
