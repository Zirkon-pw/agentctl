package task

import (
	"fmt"

	"github.com/docup/agentctl/internal/service/runtimecontrol"
	"github.com/spf13/cobra"
)

// NewEventsCmd creates the task events command.
func NewEventsCmd(rtMgr *runtimecontrol.Manager) *cobra.Command {
	var tail int
	var stageFilter string
	var raw bool
	var showThinking bool

	cmd := &cobra.Command{
		Use:   "events <task-id>",
		Short: "Show task lifecycle events",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			taskID := args[0]

			events, err := rtMgr.TaskEvents(taskID, tail)
			if err != nil {
				return err
			}

			if len(events) == 0 {
				fmt.Println("No events found.")
				return nil
			}

			for _, ev := range events {
				if stageFilter != "" && ev.StageID != stageFilter {
					continue
				}
				line, ok := formatRuntimeEvent(ev, raw, showThinking)
				if !ok {
					continue
				}
				fmt.Println(line)
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&tail, "tail", 0, "Show last N events")
	cmd.Flags().StringVar(&stageFilter, "stage", "", "Filter events by stage ID")
	cmd.Flags().BoolVar(&raw, "raw", false, "Show raw line events")
	cmd.Flags().BoolVar(&showThinking, "show-thinking", false, "Show thinking events when available")
	return cmd
}
