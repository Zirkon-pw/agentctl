package task

import (
	"fmt"

	"github.com/docup/agentctl/internal/service/taskrunner"
	"github.com/spf13/cobra"
)

// NewRouteCmd creates the task route command.
func NewRouteCmd(orch *taskrunner.Orchestrator) *cobra.Command {
	var agent string
	var reason string

	cmd := &cobra.Command{
		Use:   "route <task-id>",
		Short: "Schedule a stage-level handoff to a different agent",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if agent == "" {
				return fmt.Errorf("--agent is required")
			}
			taskID := args[0]
			if err := orch.Route(taskID, agent, reason); err != nil {
				return err
			}
			fmt.Printf("Task %s routed to agent %s\n", taskID, agent)
			return nil
		},
	}

	cmd.Flags().StringVar(&agent, "agent", "", "Target agent (required)")
	cmd.Flags().StringVar(&reason, "reason", "", "Why the handoff is needed")
	return cmd
}
