package taskrunner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	. "github.com/docup/agentctl/internal/service/taskrunner"

	"github.com/docup/agentctl/internal/config/loader"
	rt "github.com/docup/agentctl/internal/core/runtime"
	"github.com/docup/agentctl/internal/core/task"
	"github.com/docup/agentctl/internal/infra/events"
	"github.com/docup/agentctl/internal/infra/fsstore"
	infrart "github.com/docup/agentctl/internal/infra/runtime"
	"github.com/docup/agentctl/internal/service/contextpack"
	"github.com/docup/agentctl/internal/service/prompting"
)

func setupOrchestrator(t *testing.T, script string) (*Orchestrator, *fsstore.TaskStore, *fsstore.RunStore, *infrart.Registry, string) {
	t.Helper()
	root := t.TempDir()
	agentctlDir := filepath.Join(root, ".agentctl")
	for _, d := range []string{"tasks", "runs", "runtime", "context", "templates/prompts", "guidelines", "clarifications"} {
		if err := os.MkdirAll(filepath.Join(agentctlDir, d), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	adapterPath := filepath.Join(root, "fake-adapter.sh")
	if err := os.WriteFile(adapterPath, []byte(script), 0755); err != nil {
		t.Fatalf("write adapter: %v", err)
	}

	taskStore := fsstore.NewTaskStore(agentctlDir)
	runStore := fsstore.NewRunStore(agentctlDir)
	clarStore := fsstore.NewClarificationStore(agentctlDir)
	registry := infrart.NewRegistry(agentctlDir)
	heartbeatMgr := infrart.NewHeartbeatManager(agentctlDir)
	eventSink := events.NewSink(filepath.Join(agentctlDir, "runtime"))
	ctxBuilder := contextpack.NewBuilder(agentctlDir, root)
	templateStore := fsstore.NewTemplateStore(agentctlDir)
	promptBuilder := prompting.NewBuilder(templateStore, agentctlDir)

	cfg := loader.DefaultProjectConfig()
	cfg.Execution.DefaultAgent = "fake"

	adapters := NewAgentAdapterRegistry(&loader.AgentsConfig{
		Agents: []loader.AgentDef{
			{
				ID:             "fake",
				Transport:      "ndjson_stdio",
				AdapterCommand: adapterPath,
				Capabilities: rt.AdapterCapabilities{
					ProtocolVersion:       "v1",
					SupportsCancel:        true,
					SupportsPause:         true,
					SupportsResume:        true,
					SupportsKill:          true,
					SupportsHeartbeat:     true,
					SupportsClarification: true,
					SupportsReview:        true,
					SupportsHandoff:       true,
				},
			},
		},
	})
	supervisor := NewTaskSupervisor(
		taskStore, runStore, clarStore, registry, heartbeatMgr, eventSink,
		ctxBuilder, promptBuilder, cfg, adapters, root,
	)
	orch := NewOrchestrator(taskStore, runStore, registry, eventSink, cfg, supervisor)
	return orch, taskStore, runStore, registry, agentctlDir
}

func createDraftTask(store *fsstore.TaskStore) {
	now := time.Now()
	_ = store.Save(&task.Task{
		ID:     "TASK-001",
		Title:  "Test task",
		Goal:   "Test goal",
		Status: task.StatusDraft,
		Agent:  "fake",
		PromptTemplates: task.PromptTemplates{
			Builtin: []string{"strict_executor"},
		},
		Clarifications: task.Clarifications{Attached: []string{}},
		Runtime:        task.DefaultRuntimeConfig(),
		Validation:     task.ValidationConfig{Mode: task.ValidationModeSimple, Commands: []string{}},
		CreatedAt:      now,
		UpdatedAt:      now,
	})
}

func TestOrchestrator_Run_FullPipeline(t *testing.T) {
	orch, store, runStore, _, _ := setupOrchestrator(t, fullPipelineAdapterScript())
	createDraftTask(store)

	if err := orch.Run(context.Background(), "TASK-001"); err != nil {
		t.Fatalf("run: %v", err)
	}

	tk, _ := store.Load("TASK-001")
	if tk.Status != task.StatusReviewing {
		t.Fatalf("expected reviewing status, got %s", tk.Status)
	}

	session, err := runStore.LatestSession("TASK-001")
	if err != nil {
		t.Fatalf("latest session: %v", err)
	}
	if session.ReviewReport == nil {
		t.Fatal("expected review report to be persisted")
	}
	if len(session.StageHistory) != 2 {
		t.Fatalf("expected execute + review stages, got %d", len(session.StageHistory))
	}
	if _, err := os.Stat(filepath.Join(runStore.RunDir("TASK-001", session.ID), "protocol.ndjson")); err != nil {
		t.Fatalf("expected protocol log: %v", err)
	}
}

func TestOrchestrator_Run_WithValidationFailure(t *testing.T) {
	orch, store, _, _, _ := setupOrchestrator(t, fullPipelineAdapterScript())

	now := time.Now()
	_ = store.Save(&task.Task{
		ID:     "TASK-001",
		Title:  "Fail validation",
		Goal:   "Test",
		Status: task.StatusDraft,
		Agent:  "fake",
		PromptTemplates: task.PromptTemplates{
			Builtin: []string{"strict_executor"},
		},
		Clarifications: task.Clarifications{Attached: []string{}},
		Runtime:        task.DefaultRuntimeConfig(),
		Validation: task.ValidationConfig{
			Mode:     task.ValidationModeSimple,
			Commands: []string{"false"},
		},
		CreatedAt: now,
		UpdatedAt: now,
	})

	if err := orch.Run(context.Background(), "TASK-001"); err != nil {
		t.Fatalf("run: %v", err)
	}

	tk, _ := store.Load("TASK-001")
	if tk.Status != task.StatusFailed {
		t.Fatalf("expected failed, got %s", tk.Status)
	}
}

func TestOrchestrator_Run_NormalizesAgentAndTemplate(t *testing.T) {
	orch, store, _, _, _ := setupOrchestrator(t, fullPipelineAdapterScript())

	now := time.Now()
	_ = store.Save(&task.Task{
		ID:             "TASK-001",
		Title:          "Needs defaults",
		Goal:           "Run with normalized defaults",
		Status:         task.StatusDraft,
		Clarifications: task.Clarifications{Attached: []string{}},
		Runtime:        task.DefaultRuntimeConfig(),
		Validation:     task.ValidationConfig{Mode: task.ValidationModeSimple},
		CreatedAt:      now,
		UpdatedAt:      now,
	})

	if err := orch.Run(context.Background(), "TASK-001"); err != nil {
		t.Fatalf("run: %v", err)
	}

	tk, _ := store.Load("TASK-001")
	if tk.Agent != "fake" {
		t.Fatalf("expected normalized agent fake, got %s", tk.Agent)
	}
	if len(tk.PromptTemplates.Builtin) != 1 || tk.PromptTemplates.Builtin[0] != "strict_executor" {
		t.Fatalf("expected normalized template strict_executor, got %v", tk.PromptTemplates.Builtin)
	}
}

func TestOrchestrator_Run_ClarificationFlow(t *testing.T) {
	orch, store, runStore, _, agentctlDir := setupOrchestrator(t, clarificationAdapterScript())
	createDraftTask(store)

	if err := orch.Run(context.Background(), "TASK-001"); err != nil {
		t.Fatalf("run: %v", err)
	}

	tk, _ := store.Load("TASK-001")
	if tk.Status != task.StatusWaitingClarification {
		t.Fatalf("expected waiting clarification, got %s", tk.Status)
	}
	if tk.Clarifications.PendingRequest == nil {
		t.Fatal("expected pending clarification request")
	}

	reqPath := filepath.Join(agentctlDir, "clarifications", "TASK-001", "clarification_request_"+*tk.Clarifications.PendingRequest+".yml")
	if _, err := os.Stat(reqPath); err != nil {
		t.Fatalf("expected clarification request file: %v", err)
	}

	session, err := runStore.LatestSession("TASK-001")
	if err != nil {
		t.Fatalf("latest session: %v", err)
	}
	if session.PendingClarificationID == nil {
		t.Fatal("expected session to track pending clarification")
	}
}

func TestOrchestrator_Run_RequiresTitleAndGoal(t *testing.T) {
	orch, store, _, _, _ := setupOrchestrator(t, fullPipelineAdapterScript())

	now := time.Now()
	_ = store.Save(&task.Task{
		ID:             "TASK-001",
		Goal:           "Goal only",
		Status:         task.StatusDraft,
		Clarifications: task.Clarifications{Attached: []string{}},
		Runtime:        task.DefaultRuntimeConfig(),
		Validation:     task.ValidationConfig{Mode: task.ValidationModeSimple},
		CreatedAt:      now,
		UpdatedAt:      now,
	})

	if err := orch.Run(context.Background(), "TASK-001"); err == nil {
		t.Fatal("expected missing title error")
	}
}

func TestOrchestrator_Run_TaskNotFound(t *testing.T) {
	orch, _, _, _, _ := setupOrchestrator(t, fullPipelineAdapterScript())
	if err := orch.Run(context.Background(), "NONEXISTENT"); err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

func TestOrchestrator_Stop_QueuesCancelCommand(t *testing.T) {
	orch, store, _, registry, _ := setupOrchestrator(t, fullPipelineAdapterScript())

	now := time.Now()
	_ = store.Save(&task.Task{
		ID:        "TASK-001",
		Status:    task.StatusStageRunning,
		Agent:     "fake",
		Runtime:   task.DefaultRuntimeConfig(),
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err := registry.RegisterRun(rt.ActiveRun{
		TaskID: "TASK-001",
		RunID:  "RUN-001",
		Agent:  "fake",
		Status: rt.SessionStatusStageRunning,
		Capabilities: rt.AdapterCapabilities{
			SupportsCancel: true,
			SupportsPause:  true,
			SupportsResume: true,
			SupportsKill:   true,
		},
		StartedAt: now,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := orch.Stop("TASK-001"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	commands, err := registry.CommandsAfter("TASK-001", 0)
	if err != nil {
		t.Fatalf("commands: %v", err)
	}
	if len(commands) != 1 || commands[0].Type != rt.CommandTypeCancel {
		t.Fatalf("expected one cancel command, got %+v", commands)
	}
}

func TestOrchestrator_Pause_NotAllowed(t *testing.T) {
	orch, store, _, _, _ := setupOrchestrator(t, fullPipelineAdapterScript())

	now := time.Now()
	_ = store.Save(&task.Task{
		ID:        "TASK-001",
		Status:    task.StatusStageRunning,
		Runtime:   task.RuntimeConfig{AllowPause: false},
		CreatedAt: now,
		UpdatedAt: now,
	})

	if err := orch.Pause("TASK-001"); err == nil {
		t.Fatal("expected error when pause not allowed")
	}
}

func TestOrchestrator_Kill_LiveProcess(t *testing.T) {
	orch, store, _, registry, _ := setupOrchestrator(t, fullPipelineAdapterScript())

	cmd := exec.Command("sh", "-c", "sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })

	pgid, _ := syscall.Getpgid(cmd.Process.Pid)
	now := time.Now()
	_ = store.Save(&task.Task{
		ID:        "TASK-001",
		Status:    task.StatusStageRunning,
		Agent:     "fake",
		Runtime:   task.DefaultRuntimeConfig(),
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err := registry.RegisterRun(rt.ActiveRun{
		TaskID:         "TASK-001",
		RunID:          "RUN-001",
		Agent:          "fake",
		Status:         rt.SessionStatusStageRunning,
		PID:            cmd.Process.Pid,
		ProcessGroupID: pgid,
		Capabilities:   rt.AdapterCapabilities{SupportsKill: true},
		StartedAt:      now,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := orch.Kill("TASK-001"); err != nil {
		t.Fatalf("kill: %v", err)
	}
}

func TestOrchestrator_Cancel(t *testing.T) {
	orch, store, _, _, _ := setupOrchestrator(t, fullPipelineAdapterScript())

	now := time.Now()
	_ = store.Save(&task.Task{
		ID:        "TASK-001",
		Status:    task.StatusDraft,
		CreatedAt: now,
		UpdatedAt: now,
	})

	if err := orch.Cancel("TASK-001"); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	tk, _ := store.Load("TASK-001")
	if tk.Status != task.StatusCanceled {
		t.Fatalf("expected canceled, got %s", tk.Status)
	}
}

func TestOrchestrator_Cancel_Running_Fails(t *testing.T) {
	orch, store, _, registry, _ := setupOrchestrator(t, fullPipelineAdapterScript())

	now := time.Now()
	_ = store.Save(&task.Task{
		ID:        "TASK-001",
		Status:    task.StatusStageRunning,
		CreatedAt: now,
		UpdatedAt: now,
	})
	_ = registry.RegisterRun(rt.ActiveRun{TaskID: "TASK-001", RunID: "RUN-001", StartedAt: now})

	if err := orch.Cancel("TASK-001"); err == nil {
		t.Fatal("expected error canceling running task")
	}
}

func TestOrchestrator_AcceptRejectAndRoute(t *testing.T) {
	orch, store, runStore, _, _ := setupOrchestrator(t, fullPipelineAdapterScript())

	now := time.Now()
	_ = store.Save(&task.Task{
		ID:        "TASK-001",
		Status:    task.StatusReviewing,
		Agent:     "fake",
		CreatedAt: now,
		UpdatedAt: now,
	})
	if err := orch.Accept("TASK-001"); err != nil {
		t.Fatalf("accept: %v", err)
	}
	tk, _ := store.Load("TASK-001")
	if tk.Status != task.StatusCompleted {
		t.Fatalf("expected completed, got %s", tk.Status)
	}

	tk.Status = task.StatusReviewing
	_ = store.Save(tk)
	if err := orch.Reject("TASK-001", "not good enough"); err != nil {
		t.Fatalf("reject: %v", err)
	}
	tk, _ = store.Load("TASK-001")
	if tk.Status != task.StatusRejected {
		t.Fatalf("expected rejected, got %s", tk.Status)
	}

	session := &rt.RunSession{
		ID:             "RUN-001",
		TaskID:         "TASK-001",
		Status:         rt.SessionStatusQueued,
		CurrentAgentID: "fake",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := runStore.SaveSession(session); err != nil {
		t.Fatalf("save session: %v", err)
	}
	tk.Status = task.StatusQueued
	_ = store.Save(tk)
	if err := orch.Route("TASK-001", "fake", "handoff test"); err != nil {
		t.Fatalf("route: %v", err)
	}
	session, err := runStore.LoadSession("TASK-001", "RUN-001")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if session.PendingHandoff == nil {
		t.Fatal("expected pending handoff")
	}
}

func fullPipelineAdapterScript() string {
	return `#!/bin/sh
spec="$1"
stage_type="execute"
if grep -q '"type":[[:space:]]*"review"' "$spec"; then
  stage_type="review"
elif grep -q '"type":[[:space:]]*"validate_fix"' "$spec"; then
  stage_type="validate_fix"
fi
emit() { printf '%s\n' "$1"; }
base="{\"session_id\":\"$AGENTCTL_SESSION_ID\",\"task_id\":\"$AGENTCTL_TASK_ID\",\"run_id\":\"$AGENTCTL_SESSION_ID\",\"stage_id\":\"$AGENTCTL_STAGE_ID\",\"ts\":\"2026-03-22T00:00:00Z\""
emit "${base},\"seq\":1,\"type\":\"hello\",\"payload\":{\"adapter_id\":\"fake\",\"capabilities\":{\"protocol_version\":\"v1\",\"supports_cancel\":true,\"supports_pause\":true,\"supports_resume\":true,\"supports_kill\":true,\"supports_heartbeat\":true,\"supports_clarification\":true,\"supports_review\":true,\"supports_handoff\":true}}}"
emit "${base},\"seq\":2,\"type\":\"stage_started\",\"payload\":{\"type\":\"$stage_type\"}}"
if [ "$stage_type" = "review" ]; then
  printf '{"summary":"LGTM","findings":[]}\n' > "$AGENTCTL_SESSION_DIR/review_report.json"
  emit "${base},\"seq\":3,\"type\":\"review_report\",\"payload\":{\"summary\":\"LGTM\",\"artifact_path\":\"$AGENTCTL_SESSION_DIR/review_report.json\"}}"
  emit "${base},\"seq\":4,\"type\":\"stage_completed\",\"payload\":{\"result\":{\"outcome\":\"completed\",\"review_path\":\"$AGENTCTL_SESSION_DIR/review_report.json\"}}}"
else
  printf '# Summary\n' > "$AGENTCTL_SESSION_DIR/summary.md"
  printf 'diff --git a/a b/a\n' > "$AGENTCTL_SESSION_DIR/diff.patch"
  printf '[]\n' > "$AGENTCTL_SESSION_DIR/changed_files.json"
  emit "${base},\"seq\":3,\"type\":\"artifact\",\"payload\":{\"name\":\"summary.md\",\"kind\":\"summary\",\"path\":\"$AGENTCTL_SESSION_DIR/summary.md\",\"media_type\":\"text/markdown\"}}"
  emit "${base},\"seq\":4,\"type\":\"artifact\",\"payload\":{\"name\":\"diff.patch\",\"kind\":\"diff\",\"path\":\"$AGENTCTL_SESSION_DIR/diff.patch\",\"media_type\":\"text/x-diff\"}}"
  emit "${base},\"seq\":5,\"type\":\"artifact\",\"payload\":{\"name\":\"changed_files.json\",\"kind\":\"changed_files\",\"path\":\"$AGENTCTL_SESSION_DIR/changed_files.json\",\"media_type\":\"application/json\"}}"
  emit "${base},\"seq\":6,\"type\":\"stage_completed\",\"payload\":{\"result\":{\"outcome\":\"completed\",\"summary_path\":\"$AGENTCTL_SESSION_DIR/summary.md\",\"diff_path\":\"$AGENTCTL_SESSION_DIR/diff.patch\"}}}"
fi
`
}

func clarificationAdapterScript() string {
	return `#!/bin/sh
emit() { printf '%s\n' "$1"; }
base="{\"session_id\":\"$AGENTCTL_SESSION_ID\",\"task_id\":\"$AGENTCTL_TASK_ID\",\"run_id\":\"$AGENTCTL_SESSION_ID\",\"stage_id\":\"$AGENTCTL_STAGE_ID\",\"ts\":\"2026-03-22T00:00:00Z\""
emit "${base},\"seq\":1,\"type\":\"hello\",\"payload\":{\"adapter_id\":\"fake\",\"capabilities\":{\"protocol_version\":\"v1\",\"supports_cancel\":true,\"supports_pause\":true,\"supports_resume\":true,\"supports_kill\":true,\"supports_heartbeat\":true,\"supports_clarification\":true,\"supports_review\":true,\"supports_handoff\":true}}}"
emit "${base},\"seq\":2,\"type\":\"stage_started\",\"payload\":{\"type\":\"execute\"}}"
emit "${base},\"seq\":3,\"type\":\"clarification_requested\",\"payload\":{\"request_id\":\"CLAR-REQ-001\",\"reason\":\"Need more input\",\"questions\":[{\"id\":\"q1\",\"text\":\"What edge case should be handled?\"}]}}"
emit "${base},\"seq\":4,\"type\":\"stage_completed\",\"payload\":{\"result\":{\"outcome\":\"clarification_requested\"}}}"
`
}
