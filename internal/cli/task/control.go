package task

import (
	"fmt"

	"github.com/docup/agentctl/internal/service/taskrunner"
	"github.com/spf13/cobra"
)

// NewStopCmd creates the task stop command.
func NewStopCmd(orch *taskrunner.Orchestrator) *cobra.Command {
	return &cobra.Command{
		Use:   "stop <task-id>",
		Short: "Gracefully stop a running task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := orch.Stop(args[0]); err != nil {
				return err
			}
			fmt.Printf("Stop signal sent to task %s\n", args[0])
			return nil
		},
	}
}

// NewKillCmd creates the task kill command.
func NewKillCmd(orch *taskrunner.Orchestrator) *cobra.Command {
	return &cobra.Command{
		Use:   "kill <task-id>",
		Short: "Force-kill a running task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := orch.Kill(args[0]); err != nil {
				return err
			}
			fmt.Printf("Task %s killed\n", args[0])
			return nil
		},
	}
}

// NewPauseCmd creates the task pause command.
func NewPauseCmd(orch *taskrunner.Orchestrator) *cobra.Command {
	return &cobra.Command{
		Use:   "pause <task-id>",
		Short: "Pause a running task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := orch.Pause(args[0]); err != nil {
				return err
			}
			fmt.Printf("Pause signal sent to task %s\n", args[0])
			return nil
		},
	}
}

// NewCancelCmd creates the task cancel command.
func NewCancelCmd(orch *taskrunner.Orchestrator) *cobra.Command {
	return &cobra.Command{
		Use:   "cancel <task-id>",
		Short: "Cancel a task that is not running",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := orch.Cancel(args[0]); err != nil {
				return err
			}
			fmt.Printf("Task %s canceled\n", args[0])
			return nil
		},
	}
}

// NewResumeCmd creates the task resume command.
func NewResumeCmd(runHandler *taskrunner.Orchestrator) *cobra.Command {
	return &cobra.Command{
		Use:   "resume <task-id>",
		Short: "Resume a live paused stage or continue a blocked session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("Resuming task %s...\n", args[0])
			if err := runHandler.Resume(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Printf("Resume signal sent or pipeline continued for task %s.\n", args[0])
			return nil
		},
	}
}

// NewAcceptCmd creates the task accept command.
func NewAcceptCmd(orch *taskrunner.Orchestrator) *cobra.Command {
	return &cobra.Command{
		Use:   "accept <task-id>",
		Short: "Accept task results after review",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := orch.Accept(args[0]); err != nil {
				return err
			}
			fmt.Printf("Task %s accepted\n", args[0])
			return nil
		},
	}
}

// NewRejectCmd creates the task reject command.
func NewRejectCmd(orch *taskrunner.Orchestrator) *cobra.Command {
	var reason string

	cmd := &cobra.Command{
		Use:   "reject <task-id>",
		Short: "Reject task results after review",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := orch.Reject(args[0], reason); err != nil {
				return err
			}
			fmt.Printf("Task %s rejected\n", args[0])
			return nil
		},
	}

	cmd.Flags().StringVar(&reason, "reason", "", "Rejection reason")
	return cmd
}

// NewRerunCmd creates the task rerun command.
func NewRerunCmd(orch *taskrunner.Orchestrator) *cobra.Command {
	return &cobra.Command{
		Use:   "rerun <task-id>",
		Short: "Re-run a task from scratch",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("Re-running task %s...\n", args[0])
			if err := orch.Run(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Printf("Task %s rerun completed.\n", args[0])
			return nil
		},
	}
}
