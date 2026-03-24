package task

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	. "github.com/docup/agentctl/internal/cli/task"
	rt "github.com/docup/agentctl/internal/core/runtime"
	"github.com/docup/agentctl/internal/infra/fsstore"
	"github.com/docup/agentctl/tests/support/testio"
)

func setupLogsStore(t *testing.T) *fsstore.RunStore {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".agentctl")
	if err := os.MkdirAll(filepath.Join(dir, "runs"), 0755); err != nil {
		t.Fatalf("mkdir runs: %v", err)
	}
	return fsstore.NewRunStore(dir)
}

func TestLogsCommand_ProtocolFallsBackToStructuredArtifact(t *testing.T) {
	store := setupLogsStore(t)
	now := time.Now()

	structuredPath := filepath.Join(store.StageDir("TASK-001", "RUN-001", "STAGE-001"), "qwen.response.json")
	if err := os.MkdirAll(filepath.Dir(structuredPath), 0755); err != nil {
		t.Fatalf("mkdir structured dir: %v", err)
	}
	if err := os.WriteFile(structuredPath, []byte("{\"ok\":true}\n"), 0644); err != nil {
		t.Fatalf("write structured log: %v", err)
	}

	session := &rt.RunSession{
		ID:             "RUN-001",
		TaskID:         "TASK-001",
		Status:         rt.SessionStatusReviewing,
		CurrentAgentID: "qwen",
		ArtifactManifest: rt.ArtifactManifest{
			Items: []rt.ArtifactRecord{{
				Name:      "qwen.response.json",
				Kind:      "structured_log",
				Path:      structuredPath,
				StageID:   "STAGE-001",
				CreatedAt: now,
			}},
		},
		StageHistory: []rt.StageRun{{
			StageID: "STAGE-001",
			Type:    rt.StageTypeExecute,
		}},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := store.SaveSession(session); err != nil {
		t.Fatalf("save session: %v", err)
	}

	cmd := NewLogsCmd(store)
	cmd.SetArgs([]string{"TASK-001", "--protocol"})
	output := testio.CaptureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("execute: %v", err)
		}
	})

	if !strings.Contains(output, "{\"ok\":true}") {
		t.Fatalf("expected structured log fallback output, got %q", output)
	}
}
