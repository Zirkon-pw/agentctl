package task

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/docup/agentctl/internal/app/query"
	"github.com/docup/agentctl/internal/service/runtimecontrol"
	"github.com/spf13/cobra"
)

// NewWatchCmd creates the task watch command.
func NewWatchCmd(inspectQuery *query.InspectTask, rtMgr *runtimecontrol.Manager) *cobra.Command {
	var interval int
	var raw bool
	var showThinking bool

	cmd := &cobra.Command{
		Use:   "watch <task-id>",
		Short: "Live view of task status, heartbeat, and events",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			taskID := args[0]
			dur := time.Duration(interval) * time.Second

			sigCh := make(chan os.Signal, 1)
			signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
			defer signal.Stop(sigCh)

			fmt.Printf("Watching task %s (Ctrl+C to stop)\n", taskID)

			if inspectQuery != nil {
				if detail, err := inspectQuery.Execute(taskID); err == nil {
					fmt.Printf("Task: %s | Status: %s | Agent: %s\n", detail.ID, detail.Status, detail.Agent)
					if detail.LatestSession != nil {
						sess := detail.LatestSession
						fmt.Printf("Session: %s | Status: %s | Agent: %s\n", sess.ID, sess.Status, sess.Agent)
						if sess.LastStageID != "" {
							fmt.Printf("Last stage: %s (%s) — %s\n", sess.LastStageID, sess.LastStageType, sess.LastOutcome)
						}
					}
				}
			}

			var lastSeq int64
			if rtMgr != nil {
				if existing, err := rtMgr.TaskEvents(taskID, 10); err == nil {
					for _, ev := range existing {
						if ev.Sequence > lastSeq {
							lastSeq = ev.Sequence
						}
						line, ok := formatRuntimeEvent(ev, raw, showThinking)
						if !ok {
							continue
						}
						fmt.Println(line)
					}
				}
			}

			for {
				select {
				case <-sigCh:
					fmt.Println("\nStopped watching.")
					return nil
				case <-time.After(dur):
				}

				if rtMgr == nil {
					continue
				}
				events, err := rtMgr.TaskEventsAfter(taskID, lastSeq, 0)
				if err != nil {
					fmt.Printf("watch error: %v\n", err)
					continue
				}
				for _, ev := range events {
					if ev.Sequence > lastSeq {
						lastSeq = ev.Sequence
					}
					line, ok := formatRuntimeEvent(ev, raw, showThinking)
					if !ok {
						continue
					}
					fmt.Println(line)
				}
			}
		},
	}

	cmd.Flags().IntVar(&interval, "interval", 2, "Refresh interval in seconds")
	cmd.Flags().BoolVar(&raw, "raw", false, "Show raw line events")
	cmd.Flags().BoolVar(&showThinking, "show-thinking", false, "Show thinking events when available")
	return cmd
}
