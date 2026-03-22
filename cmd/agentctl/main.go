package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/docup/agentctl/internal/bootstrap"
	"github.com/docup/agentctl/internal/cli"
	cliclar "github.com/docup/agentctl/internal/cli/clarification"
	"github.com/docup/agentctl/internal/cli/guidelines"
	"github.com/docup/agentctl/internal/cli/help"
	"github.com/docup/agentctl/internal/cli/result"
	"github.com/docup/agentctl/internal/cli/root"
	clitask "github.com/docup/agentctl/internal/cli/task"
	clitmpl "github.com/docup/agentctl/internal/cli/template"
	"github.com/docup/agentctl/internal/infra/logging"
)

func main() {
	logging.Setup(false)

	rootCmd := root.NewRootCmd()

	// Init doesn't need workspace
	rootCmd.AddCommand(cli.NewInitCmd())
	rootCmd.AddCommand(help.NewHelpCmd())

	// Try to load workspace for other commands
	app, appErr := bootstrap.NewApp()
	if appErr != nil {
		// Workspace not initialized — only init and help are available
		rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
			name := cmd.Name()
			if name == "init" || name == "help" || name == "agentctl" {
				return nil
			}
			return fmt.Errorf("workspace not initialized: %v\nRun 'agentctl init' first", appErr)
		}
	}

	if app != nil {
		// Task commands
		taskCmd := &cobra.Command{
			Use:   "task",
			Short: "Manage tasks",
		}
		taskCmd.AddCommand(clitask.NewCreateCmd(app.CreateTask))
		taskCmd.AddCommand(clitask.NewRunCmd(app.RunTask))
		taskCmd.AddCommand(clitask.NewListCmd(app.ListTasks))
		taskCmd.AddCommand(clitask.NewInspectCmd(app.InspectTask, app.RuntimeMgr))
		taskCmd.AddCommand(clitask.NewPsCmd(app.RuntimeMgr))
		taskCmd.AddCommand(clitask.NewStopCmd(app.Orchestrator))
		taskCmd.AddCommand(clitask.NewKillCmd(app.Orchestrator))
		taskCmd.AddCommand(clitask.NewPauseCmd(app.Orchestrator))
		taskCmd.AddCommand(clitask.NewCancelCmd(app.Orchestrator))
		taskCmd.AddCommand(clitask.NewResumeCmd(app.Orchestrator))
		taskCmd.AddCommand(clitask.NewAcceptCmd(app.Orchestrator))
		taskCmd.AddCommand(clitask.NewRejectCmd(app.Orchestrator))
		taskCmd.AddCommand(clitask.NewRerunCmd(app.Orchestrator))
		taskCmd.AddCommand(clitask.NewUpdateCmd(app.UpdateTask))
		taskCmd.AddCommand(clitask.NewLogsCmd(app.RunStore, app.AgentctlDir))
		taskCmd.AddCommand(clitask.NewEventsCmd(app.RuntimeMgr))
		taskCmd.AddCommand(clitask.NewWatchCmd(app.InspectTask, app.RuntimeMgr))
		taskCmd.AddCommand(clitask.NewRouteCmd(app.Orchestrator))
		rootCmd.AddCommand(taskCmd)

		// Template commands
		rootCmd.AddCommand(clitmpl.NewTemplateCmd(app.TemplateStore))

		// Clarification commands
		rootCmd.AddCommand(cliclar.NewClarificationCmd(app.ClarMgr))

		// Guidelines commands
		rootCmd.AddCommand(guidelines.NewGuidelinesCmd(app.AgentctlDir))

		// Result commands
		rootCmd.AddCommand(result.NewResultCmd(app.RunStore))
	}

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
