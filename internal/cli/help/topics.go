package help

import (
	"fmt"

	"github.com/spf13/cobra"
)

// NewHelpCmd creates the help command with topic support.
func NewHelpCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "topics [topic]",
		Short: "Help about workflows and concepts",
		Long:  "Get help about agentctl commands, workflows, and concepts.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Root().Help()
			}
			topic := args[0]
			text, ok := topics[topic]
			if !ok {
				return fmt.Errorf("unknown help topic: %s\nAvailable topics: task, template, clarification, validation, workflow", topic)
			}
			fmt.Println(text)
			return nil
		},
	}
	return cmd
}

var topics = map[string]string{
	"task": `Task Management
===============
Tasks are the central unit of work in agentctl.

Create:   agentctl task create
Configure: agentctl task update TASK-001 --title "..." --goal "..."
Run:      agentctl task run TASK-001
List:     agentctl task list
Inspect:  agentctl task inspect TASK-001
Stop:     agentctl task stop TASK-001
Kill:     agentctl task kill TASK-001
Resume:   agentctl task resume TASK-001
Accept:   agentctl task accept TASK-001
Reject:   agentctl task reject TASK-001`,

	"template": `Prompt Templates
================
Templates control agent behavior during execution.

Built-in templates:
  - clarify_if_needed:     Ask questions instead of guessing
  - plan_before_execution: Plan before coding
  - strict_executor:       Follow scope strictly
  - research_only:         Analyze, no code changes
  - review_only:           Review completed work

Commands:
  agentctl template list --builtin
  agentctl template show <name>
  agentctl template add <path>`,

	"clarification": `Clarification Flow
==================
When a task has ambiguities, the agent can request clarification.

1. Adapter emits clarification_requested via NDJSON protocol
2. Supervisor materializes clarification_request_*.yml
3. Task transitions to waiting_clarification
4. User fills answers in clarification file
5. User attaches: agentctl clarification attach TASK-001 <path>
6. Resume: agentctl task resume TASK-001`,

	"validation": `Validation
==========
After agent execution, validation commands run automatically.

Modes:
  simple: Run commands, exit 0 = pass. No retries.
  full:   Run commands, if fail — agent fixes, retry up to N times (default 3).

Configure in task YAML:
  validation:
    mode: full
    max_retries: 3
    commands:
      - go build ./...
      - go test ./...`,

	"workflow": `Typical Workflow
================
1. agentctl init                    — Initialize project
2. agentctl task create             — Create a draft task
3. agentctl task update TASK-001 ... — Fill in title, goal, scope, templates
4. agentctl task run TASK-001       — Start/continue a session pipeline
5. agentctl task inspect TASK-001   — Check results
6. agentctl task accept TASK-001    — Approve results

With clarification:
4. agentctl task run TASK-001
5. (adapter requests clarification)
6. agentctl clarification show TASK-001
7. (edit clarification YAML)
8. agentctl clarification attach TASK-001 <path>
9. agentctl task resume TASK-001`,
}
