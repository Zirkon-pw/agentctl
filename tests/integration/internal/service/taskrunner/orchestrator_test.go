package taskrunner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

func boolPtr(v bool) *bool { return &v }

type testAgent struct {
	ID     string
	Driver loader.AgentDriver
	Script string
}

func setupOrchestrator(t *testing.T, driver loader.AgentDriver, script string) (*Orchestrator, *fsstore.TaskStore, *fsstore.RunStore, *infrart.Registry, string) {
	return setupOrchestratorWithAgents(t, []testAgent{{
		ID:     "fake",
		Driver: driver,
		Script: script,
	}})
}

func setupOrchestratorWithAgents(t *testing.T, agents []testAgent) (*Orchestrator, *fsstore.TaskStore, *fsstore.RunStore, *infrart.Registry, string) {
	t.Helper()
	root := t.TempDir()
	agentctlDir := filepath.Join(root, ".agentctl")
	for _, d := range []string{"tasks", "runs", "runtime", "context", "templates/prompts", "guidelines", "clarifications"} {
		if err := os.MkdirAll(filepath.Join(agentctlDir, d), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
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

	defs := make([]loader.AgentDef, 0, len(agents))
	for _, agent := range agents {
		cliPath := filepath.Join(root, agent.ID+".sh")
		if err := os.WriteFile(cliPath, []byte(agent.Script), 0755); err != nil {
			t.Fatalf("write cli for %s: %v", agent.ID, err)
		}
		defs = append(defs, loader.AgentDef{
			ID:      agent.ID,
			Driver:  agent.Driver,
			Command: cliPath,
			Enabled: boolPtr(true),
		})
	}

	drivers := NewAgentRuntimeRegistry(&loader.AgentsConfig{Agents: defs})

	supervisor := NewTaskSupervisor(
		taskStore, runStore, clarStore, registry, heartbeatMgr, eventSink,
		ctxBuilder, promptBuilder, cfg, drivers, root,
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

func initGitRepo(t *testing.T, root string) {
	t.Helper()

	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("/usr/bin/git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	if err := os.WriteFile(filepath.Join(root, "tracked.txt"), []byte("base\n"), 0644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}

	run("init")
	run("config", "user.name", "Agentctl Tests")
	run("config", "user.email", "agentctl-tests@example.com")
	run("add", "tracked.txt")
	if scripts, err := filepath.Glob(filepath.Join(root, "*.sh")); err == nil {
		for _, script := range scripts {
			run("add", filepath.Base(script))
		}
	}
	run("commit", "-m", "initial")
}

func waitForCondition(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatal("condition not met before timeout")
}

func TestOrchestrator_Run_FullPipelineWithClaudeDriver(t *testing.T) {
	orch, store, runStore, _, _ := setupOrchestrator(t, loader.AgentDriverClaude, claudeWorkflowScript())
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
	if _, err := os.Stat(filepath.Join(runStore.StageDir("TASK-001", session.ID, "STAGE-001"), "stdout.log")); err != nil {
		t.Fatalf("expected stdout log: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runStore.StageDir("TASK-001", session.ID, "STAGE-002"), "stage_result.json")); err != nil {
		t.Fatalf("expected stage_result.json: %v", err)
	}
}

func TestOrchestrator_Run_QwenDriverPersistsStructuredLogAndContinuationState(t *testing.T) {
	orch, store, runStore, _, _ := setupOrchestrator(t, loader.AgentDriverQwen, qwenWorkflowScript())
	createDraftTask(store)

	if err := orch.Run(context.Background(), "TASK-001"); err != nil {
		t.Fatalf("run: %v", err)
	}

	session, err := runStore.LatestSession("TASK-001")
	if err != nil {
		t.Fatalf("latest session: %v", err)
	}
	if session.DriverState.ExternalSessionID != "qwen-session-1" {
		t.Fatalf("expected qwen session id to be persisted, got %q", session.DriverState.ExternalSessionID)
	}
	structuredPath := filepath.Join(runStore.StageDir("TASK-001", session.ID, "STAGE-001"), "qwen.response.jsonl")
	if _, err := os.Stat(structuredPath); err != nil {
		t.Fatalf("expected qwen structured log: %v", err)
	}
}

func TestOrchestrator_Run_WithValidationFailure(t *testing.T) {
	orch, store, _, _, _ := setupOrchestrator(t, loader.AgentDriverClaude, claudeWorkflowScript())

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
	orch, store, _, _, _ := setupOrchestrator(t, loader.AgentDriverClaude, claudeWorkflowScript())

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
	orch, store, runStore, _, agentctlDir := setupOrchestrator(t, loader.AgentDriverClaude, clarificationWorkflowScript())
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
	orch, store, _, _, _ := setupOrchestrator(t, loader.AgentDriverClaude, claudeWorkflowScript())

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
	orch, _, _, _, _ := setupOrchestrator(t, loader.AgentDriverClaude, claudeWorkflowScript())
	if err := orch.Run(context.Background(), "NONEXISTENT"); err == nil {
		t.Fatal("expected error for nonexistent task")
	}
}

func TestOrchestrator_Stop_SendsSIGTERMToProcessGroup(t *testing.T) {
	orch, store, _, registry, _ := setupOrchestrator(t, loader.AgentDriverClaude, claudeWorkflowScript())

	cmd := exec.Command("sh", "-c", "sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()

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
		StartedAt:      now,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := orch.Stop("TASK-001"); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestOrchestrator_Kill_LiveProcess(t *testing.T) {
	orch, store, _, registry, _ := setupOrchestrator(t, loader.AgentDriverClaude, claudeWorkflowScript())

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
		StartedAt:      now,
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := orch.Kill("TASK-001"); err != nil {
		t.Fatalf("kill: %v", err)
	}
}

func TestOrchestrator_Cancel(t *testing.T) {
	orch, store, _, _, _ := setupOrchestrator(t, loader.AgentDriverClaude, claudeWorkflowScript())

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
	orch, store, _, registry, _ := setupOrchestrator(t, loader.AgentDriverClaude, claudeWorkflowScript())

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
	orch, store, runStore, _, _ := setupOrchestrator(t, loader.AgentDriverClaude, claudeWorkflowScript())

	now := time.Now()
	_ = store.Save(&task.Task{
		ID:        "TASK-001",
		Status:    task.StatusReviewing,
		Agent:     "fake",
		CreatedAt: now,
		UpdatedAt: now,
	})
	acceptedSession := &rt.RunSession{
		ID:             "RUN-001",
		TaskID:         "TASK-001",
		Status:         rt.SessionStatusReviewing,
		CurrentAgentID: "fake",
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := runStore.SaveSession(acceptedSession); err != nil {
		t.Fatalf("save accepted session: %v", err)
	}
	if err := orch.Accept("TASK-001"); err != nil {
		t.Fatalf("accept: %v", err)
	}
	tk, _ := store.Load("TASK-001")
	if tk.Status != task.StatusCompleted {
		t.Fatalf("expected completed, got %s", tk.Status)
	}
	var session *rt.RunSession
	var err error
	session, err = runStore.LoadSession("TASK-001", "RUN-001")
	if err != nil {
		t.Fatalf("load accepted session: %v", err)
	}
	if session.Status != rt.SessionStatusCompleted {
		t.Fatalf("expected completed session status, got %s", session.Status)
	}
	if session.CompletedAt == nil {
		t.Fatal("expected completed_at to be set")
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

	session = &rt.RunSession{
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
	session, err = runStore.LoadSession("TASK-001", "RUN-001")
	if err != nil {
		t.Fatalf("load session: %v", err)
	}
	if session.PendingHandoff == nil {
		t.Fatal("expected pending handoff")
	}
}

func TestOrchestrator_Run_AppliesLiveRouteBeforeNextStage(t *testing.T) {
	orch, store, runStore, registry, _ := setupOrchestratorWithAgents(t, []testAgent{
		{ID: "fake", Driver: loader.AgentDriverClaude, Script: routeWorkflowScript()},
		{ID: "fake-alt", Driver: loader.AgentDriverClaude, Script: routeWorkflowScript()},
	})
	createDraftTask(store)

	done := make(chan error, 1)
	go func() {
		done <- orch.Run(context.Background(), "TASK-001")
	}()

	waitForCondition(t, 3*time.Second, func() bool {
		active, err := registry.LoadActiveRun("TASK-001")
		return err == nil && active != nil
	})

	if err := orch.Route("TASK-001", "fake-alt", "handoff live"); err != nil {
		t.Fatalf("route live session: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for routed run to finish")
	}

	session, err := runStore.LatestSession("TASK-001")
	if err != nil {
		t.Fatalf("latest session: %v", err)
	}
	if session.PendingHandoff != nil {
		t.Fatal("expected pending handoff to be consumed")
	}
	if len(session.StageHistory) < 3 {
		t.Fatalf("expected handoff to add follow-up stages, got %d stages", len(session.StageHistory))
	}
	if session.StageHistory[1].Type != rt.StageTypeHandoff {
		t.Fatalf("expected handoff stage, got %s", session.StageHistory[1].Type)
	}

	usedAltAgent := false
	for _, stage := range session.StageHistory[2:] {
		if stage.AgentID == "fake-alt" && stage.Type != rt.StageTypeHandoff {
			usedAltAgent = true
			break
		}
	}
	if !usedAltAgent {
		t.Fatalf("expected a post-handoff agent stage on fake-alt, got %+v", session.StageHistory)
	}
	if session.CurrentAgentID != "fake-alt" {
		t.Fatalf("expected session current agent fake-alt, got %s", session.CurrentAgentID)
	}

	tk, err := store.Load("TASK-001")
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if tk.Agent != "fake-alt" {
		t.Fatalf("expected task agent fake-alt after handoff, got %s", tk.Agent)
	}
}

func TestOrchestrator_Run_QwenDriverCreatesLiveSessionAndProtocolLogs(t *testing.T) {
	orch, store, runStore, registry, agentctlDir := setupOrchestrator(t, loader.AgentDriverQwen, qwenSlowWorkflowScript())
	createDraftTask(store)

	done := make(chan error, 1)
	go func() {
		done <- orch.Run(context.Background(), "TASK-001")
	}()

	waitForCondition(t, 3*time.Second, func() bool {
		active, err := registry.LoadActiveRun("TASK-001")
		return err == nil && active != nil
	})

	session, err := runStore.LatestSession("TASK-001")
	if err != nil {
		t.Fatalf("latest session while running: %v", err)
	}
	runDir := runStore.RunDir("TASK-001", session.ID)
	sessionLogPath := filepath.Join(runDir, "session.log")
	protocolLogPath := filepath.Join(runDir, "protocol.ndjson")
	if _, err := os.Stat(sessionLogPath); err != nil {
		t.Fatalf("expected session log during running stage: %v", err)
	}
	if _, err := os.Stat(protocolLogPath); err != nil {
		t.Fatalf("expected protocol log during running stage: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for qwen run to finish")
	}

	protocolLog, err := os.ReadFile(protocolLogPath)
	if err != nil {
		t.Fatalf("read protocol log: %v", err)
	}
	if !strings.Contains(string(protocolLog), "qwen-session-1") {
		t.Fatalf("expected qwen session id in protocol log, got %q", string(protocolLog))
	}
	if !strings.Contains(string(protocolLog), "AGENTCTL_RESULT_BEGIN") {
		t.Fatalf("expected structured result markers in protocol log, got %q", string(protocolLog))
	}

	eventSink := events.NewSink(filepath.Join(agentctlDir, "runtime"))
	evs, err := eventSink.Read("TASK-001")
	if err != nil {
		t.Fatalf("read runtime events: %v", err)
	}

	hasProtocolLine := false
	hasThinking := false
	hasAgentMessage := false
	hasToolCall := false
	hasToolResult := false
	for _, ev := range evs {
		switch ev.EventType {
		case "protocol_line":
			hasProtocolLine = true
		case "thinking":
			hasThinking = true
		case "agent_message":
			hasAgentMessage = true
		case "tool_call":
			hasToolCall = true
		case "tool_result":
			hasToolResult = true
		}
	}
	if !hasProtocolLine || !hasThinking || !hasAgentMessage || !hasToolCall || !hasToolResult {
		t.Fatalf("expected live qwen events, got %+v", evs)
	}
}

func TestOrchestrator_Run_CapturesDiffForUntrackedFiles(t *testing.T) {
	orch, store, runStore, _, agentctlDir := setupOrchestrator(t, loader.AgentDriverClaude, untrackedFileWorkflowScript())
	initGitRepo(t, filepath.Dir(agentctlDir))
	createDraftTask(store)

	if err := orch.Run(context.Background(), "TASK-001"); err != nil {
		t.Fatalf("run: %v", err)
	}

	session, err := runStore.LatestSession("TASK-001")
	if err != nil {
		t.Fatalf("latest session: %v", err)
	}

	var diffArtifact *rt.ArtifactRecord
	for i := range session.ArtifactManifest.Items {
		item := &session.ArtifactManifest.Items[i]
		if item.Kind == "diff" || item.Name == "diff.patch" {
			diffArtifact = item
			break
		}
	}
	if diffArtifact == nil {
		t.Fatal("expected diff artifact to be recorded")
	}

	data, err := os.ReadFile(diffArtifact.Path)
	if err != nil {
		t.Fatalf("read diff artifact: %v", err)
	}
	diff := string(data)
	if !strings.Contains(diff, "created.txt") {
		t.Fatalf("expected diff to mention created.txt, got %q", diff)
	}
	if !strings.Contains(diff, "new file mode") {
		t.Fatalf("expected untracked file patch, got %q", diff)
	}
	if strings.Contains(diff, ".agentctl/") {
		t.Fatalf("did not expect runtime files in diff, got %q", diff)
	}
	if strings.Contains(diff, "summary.md") {
		t.Fatalf("did not expect root-level artifact files in diff, got %q", diff)
	}
}

func TestOrchestrator_Run_FailsWhenAgentWritesIntoStageDir(t *testing.T) {
	orch, store, runStore, _, agentctlDir := setupOrchestrator(t, loader.AgentDriverClaude, forbiddenRuntimeWriteWorkflowScript())
	initGitRepo(t, filepath.Dir(agentctlDir))
	createDraftTask(store)

	if err := orch.Run(context.Background(), "TASK-001"); err != nil {
		t.Fatalf("run: %v", err)
	}

	tk, err := store.Load("TASK-001")
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if tk.Status != task.StatusFailed {
		t.Fatalf("expected failed task status, got %s", tk.Status)
	}

	session, err := runStore.LatestSession("TASK-001")
	if err != nil {
		t.Fatalf("latest session: %v", err)
	}
	last := session.LastStage()
	if last == nil || last.Result == nil {
		t.Fatalf("expected failed stage result, got %+v", session.StageHistory)
	}
	if !strings.Contains(last.Result.Message, "forbidden files under .agentctl") {
		t.Fatalf("expected runtime violation message, got %+v", last.Result)
	}

	eventSink := events.NewSink(filepath.Join(agentctlDir, "runtime"))
	evs, err := eventSink.Read("TASK-001")
	if err != nil {
		t.Fatalf("read runtime events: %v", err)
	}
	found := false
	for _, ev := range evs {
		if ev.EventType == "runtime_violation" && strings.Contains(ev.Details, "obsidian-customization-guide.md") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected runtime_violation event, got %+v", evs)
	}
}

func TestOrchestrator_Run_FailsWhenAgentModifiesPrompt(t *testing.T) {
	orch, store, runStore, _, agentctlDir := setupOrchestrator(t, loader.AgentDriverClaude, forbiddenPromptMutationWorkflowScript())
	initGitRepo(t, filepath.Dir(agentctlDir))
	createDraftTask(store)

	if err := orch.Run(context.Background(), "TASK-001"); err != nil {
		t.Fatalf("run: %v", err)
	}

	session, err := runStore.LatestSession("TASK-001")
	if err != nil {
		t.Fatalf("latest session: %v", err)
	}
	last := session.LastStage()
	if last == nil || last.Result == nil {
		t.Fatalf("expected failed stage result, got %+v", session.StageHistory)
	}
	if !strings.Contains(last.Result.Message, "modified .agentctl/runs/TASK-001") {
		t.Fatalf("expected prompt mutation to be reported, got %+v", last.Result)
	}
}

func claudeWorkflowScript() string {
	return `#!/bin/sh
if [ "$AGENTCTL_STAGE_TYPE" = "review" ]; then
cat <<'EOF'
AGENTCTL_RESULT_BEGIN
{"outcome":"completed","summary":"LGTM","findings":[]}
AGENTCTL_RESULT_END
EOF
else
cat <<'EOF'
AGENTCTL_RESULT_BEGIN
{"outcome":"completed","summary":"# Summary\nDone"}
AGENTCTL_RESULT_END
EOF
fi
printf 'driver stderr\n' >&2
`
}

func qwenWorkflowScript() string {
	return `#!/bin/sh
cat <<'EOF'
{"type":"system","session_id":"qwen-session-1","subtype":"init"}
{"type":"assistant","session_id":"qwen-session-1","message":{"content":[{"type":"text","text":"Done from qwen"}]}}
{"type":"result","session_id":"qwen-session-1","result":"AGENTCTL_RESULT_BEGIN\n{\"outcome\":\"completed\",\"summary\":\"# Summary\\nDone from qwen\"}\nAGENTCTL_RESULT_END"}
EOF
`
}

func qwenSlowWorkflowScript() string {
	return `#!/bin/sh
if [ "$AGENTCTL_STAGE_TYPE" = "review" ]; then
printf '%s\n' '{"type":"system","session_id":"qwen-session-1","subtype":"init"}'
printf '%s\n' '{"type":"assistant","session_id":"qwen-session-1","message":{"content":[{"type":"text","text":"Review completed"}]}}'
printf '%s\n' '{"type":"result","session_id":"qwen-session-1","result":"AGENTCTL_RESULT_BEGIN\n{\"outcome\":\"completed\",\"summary\":\"LGTM\",\"findings\":[]}\nAGENTCTL_RESULT_END"}'
exit 0
fi
printf '%s\n' '{"type":"system","session_id":"qwen-session-1","subtype":"init"}'
/bin/sleep 1
printf '%s\n' '{"type":"assistant","session_id":"qwen-session-1","message":{"content":[{"type":"thinking","thinking":"reasoning about filesystem"}]}}'
/bin/sleep 1
printf '%s\n' '{"type":"assistant","session_id":"qwen-session-1","message":{"content":[{"type":"text","text":"I will create the file now"},{"type":"tool_use","id":"call_1","name":"write_file","input":{"file_path":"created.txt"}}]}}'
/bin/sleep 1
printf '%s\n' '{"type":"user","session_id":"qwen-session-1","content":[{"type":"tool_result","tool_use_id":"call_1","is_error":false,"content":"created created.txt"}]}'
/bin/sleep 1
printf '%s\n' '{"type":"result","session_id":"qwen-session-1","result":"AGENTCTL_RESULT_BEGIN\n{\"outcome\":\"completed\",\"summary\":\"# Summary\\nDone from qwen slow\"}\nAGENTCTL_RESULT_END"}'
`
}

func clarificationWorkflowScript() string {
	return `#!/bin/sh
cat <<'EOF'
AGENTCTL_RESULT_BEGIN
{"outcome":"clarification_requested","reason":"Need more input","request_id":"CLAR-REQ-001","questions":[{"id":"q1","text":"What edge case should be handled?"}]}
AGENTCTL_RESULT_END
EOF
`
}

func routeWorkflowScript() string {
	return `#!/bin/sh
if [ "$AGENTCTL_STAGE_TYPE" = "review" ]; then
cat <<EOF
AGENTCTL_RESULT_BEGIN
{"outcome":"completed","summary":"reviewed by $AGENTCTL_AGENT_ID","findings":[]}
AGENTCTL_RESULT_END
EOF
exit 0
fi

/bin/sleep 1
cat <<EOF
AGENTCTL_RESULT_BEGIN
{"outcome":"completed","summary":"executed by $AGENTCTL_AGENT_ID"}
AGENTCTL_RESULT_END
EOF
`
}

func untrackedFileWorkflowScript() string {
	return `#!/bin/sh
printf 'hello from new file\n' > created.txt
printf '# Summary from agent\n' > summary.md
cat <<'EOF'
AGENTCTL_RESULT_BEGIN
{"outcome":"completed","summary":"# Summary\nCreated untracked file"}
AGENTCTL_RESULT_END
EOF
`
}

func forbiddenRuntimeWriteWorkflowScript() string {
	return `#!/bin/sh
printf 'bad write\n' > "$AGENTCTL_STAGE_DIR/obsidian-customization-guide.md"
cat <<'EOF'
AGENTCTL_RESULT_BEGIN
{"outcome":"completed","summary":"# Summary\nWrote file into stage dir"}
AGENTCTL_RESULT_END
EOF
`
}

func forbiddenPromptMutationWorkflowScript() string {
	return `#!/bin/sh
printf '\n# tampered\n' >> "$AGENTCTL_PROMPT_PATH"
cat <<'EOF'
AGENTCTL_RESULT_BEGIN
{"outcome":"completed","summary":"# Summary\nTampered prompt"}
AGENTCTL_RESULT_END
EOF
`
}
