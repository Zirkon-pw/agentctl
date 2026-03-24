package task

import (
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	rt "github.com/docup/agentctl/internal/core/runtime"
	"github.com/docup/agentctl/internal/infra/fsstore"
	"github.com/spf13/cobra"
)

// NewLogsCmd creates the task logs command.
func NewLogsCmd(runStore *fsstore.RunStore) *cobra.Command {
	var follow bool
	var stageID string
	var protocol bool
	var showStdout bool
	var showStderr bool
	var showSession bool

	cmd := &cobra.Command{
		Use:   "logs <task-id>",
		Short: "Show execution logs for a task",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			taskID := args[0]

			session, err := runStore.LatestSession(taskID)
			if err != nil {
				return fmt.Errorf("no sessions found for task %s", taskID)
			}

			logsPath, err := resolveLogsPath(runStore, taskID, session, stageID, protocol, showStdout, showStderr, showSession, follow)
			if err != nil {
				return err
			}

			if follow {
				return followLogs(logsPath)
			}

			data, err := os.ReadFile(logsPath)
			if err != nil {
				return fmt.Errorf("reading logs: %w", err)
			}
			fmt.Print(string(data))
			return nil
		},
	}

	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Follow log output")
	cmd.Flags().StringVar(&stageID, "stage", "", "Read logs for a specific stage ID")
	cmd.Flags().BoolVar(&protocol, "protocol", false, "Show raw protocol.ndjson log for the latest session")
	cmd.Flags().BoolVar(&showStdout, "stdout", false, "Show agent stdout log")
	cmd.Flags().BoolVar(&showStderr, "stderr", false, "Show agent stderr log")
	cmd.Flags().BoolVar(&showSession, "session", false, "Show unified session log (all stages)")
	return cmd
}

func resolveLogsPath(runStore *fsstore.RunStore, taskID string, session *rt.RunSession, stageID string, protocol, showStdout, showStderr, showSession, follow bool) (string, error) {
	if protocol {
		path := protocolLogPath(runStore, taskID, session.ID)
		if existsAndNotEmpty(path) || exists(path) || (follow && isLiveSession(session)) {
			return path, nil
		}
		if fallback := latestStructuredLogPath(session); fallback != "" {
			return fallback, nil
		}
		return "", fmt.Errorf("no protocol log found for session %s", session.ID)
	}

	if showSession {
		path := filepath.Join(runStore.RunDir(taskID, session.ID), "session.log")
		if exists(path) || (follow && isLiveSession(session)) {
			return path, nil
		}
		return "", fmt.Errorf("no session log found for session %s", session.ID)
	}

	stage := resolveStage(session, stageID)
	if stage == nil {
		return "", fmt.Errorf("no stage found for task %s", taskID)
	}

	stageDir := runStore.StageDir(taskID, session.ID, stage.StageID)

	// Explicit stream selection.
	if showStdout {
		path := filepath.Join(stageDir, "stdout.log")
		if exists(path) || (follow && isLiveSession(session)) {
			return path, nil
		}
		return "", fmt.Errorf("no stdout.log found for stage %s", stage.StageID)
	}
	if showStderr {
		path := filepath.Join(stageDir, "stderr.log")
		if exists(path) || (follow && isLiveSession(session)) {
			return path, nil
		}
		return "", fmt.Errorf("no stderr.log found for stage %s", stage.StageID)
	}

	// Default: prefer stdout (main agent output), fallback to stderr, then errors.
	candidates := []string{
		filepath.Join(stageDir, "stdout.log"),
		filepath.Join(stageDir, "stderr.log"),
		filepath.Join(stageDir, "runtime_errors.log"),
	}
	for _, candidate := range candidates {
		if existsAndNotEmpty(candidate) {
			return candidate, nil
		}
	}
	for _, candidate := range candidates {
		if exists(candidate) {
			return candidate, nil
		}
	}
	if follow && isLiveSession(session) {
		return candidates[0], nil
	}
	return "", fmt.Errorf("no logs found for stage %s; try --session for unified session log", stage.StageID)
}

func resolveStage(session *rt.RunSession, stageID string) *rt.StageRun {
	if stageID == "" {
		return session.LastStage()
	}
	for i := range session.StageHistory {
		if session.StageHistory[i].StageID == stageID {
			return &session.StageHistory[i]
		}
	}
	return nil
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func existsAndNotEmpty(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Size() > 0
}

func isLiveSession(session *rt.RunSession) bool {
	return session.Status == rt.SessionStatusStageRunning || session.Status == rt.SessionStatusHandoffPending
}

func protocolLogPath(runStore *fsstore.RunStore, taskID, sessionID string) string {
	return filepath.Join(runStore.RunDir(taskID, sessionID), "protocol.ndjson")
}

func latestStructuredLogPath(session *rt.RunSession) string {
	for i := len(session.ArtifactManifest.Items) - 1; i >= 0; i-- {
		item := session.ArtifactManifest.Items[i]
		if item.Kind == "structured_log" && item.Path != "" {
			return item.Path
		}
	}
	return ""
}

func followLogs(path string) error {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	// Wait for file to appear.
	for !exists(path) {
		select {
		case <-sigCh:
			return nil
		case <-time.After(100 * time.Millisecond):
		}
	}

	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	buf := make([]byte, 4096)
	for {
		select {
		case <-sigCh:
			return nil
		default:
		}

		n, readErr := f.Read(buf)
		if n > 0 {
			fmt.Print(string(buf[:n]))
			continue
		}
		if readErr != nil && readErr != io.EOF {
			return fmt.Errorf("reading log file: %w", readErr)
		}

		// No data available, wait briefly.
		select {
		case <-sigCh:
			return nil
		case <-time.After(100 * time.Millisecond):
		}
	}
}
