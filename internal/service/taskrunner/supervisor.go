package taskrunner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/docup/agentctl/internal/config/loader"
	"github.com/docup/agentctl/internal/core/clarification"
	rt "github.com/docup/agentctl/internal/core/runtime"
	"github.com/docup/agentctl/internal/core/task"
	"github.com/docup/agentctl/internal/core/validation"
	"github.com/docup/agentctl/internal/infra/events"
	"github.com/docup/agentctl/internal/infra/fsstore"
	infrart "github.com/docup/agentctl/internal/infra/runtime"
	"github.com/docup/agentctl/internal/service/contextpack"
	"github.com/docup/agentctl/internal/service/prompting"
	"gopkg.in/yaml.v3"
)

// TaskSupervisor runs stage-based task sessions using built-in CLI drivers.
type TaskSupervisor struct {
	taskStore      *fsstore.TaskStore
	runStore       *fsstore.RunStore
	clarStore      *fsstore.ClarificationStore
	registry       *infrart.Registry
	heartbeatMgr   *infrart.HeartbeatManager
	eventSink      *events.Sink
	contextBuilder *contextpack.Builder
	promptBuilder  *prompting.Builder
	config         *loader.ProjectConfig
	drivers        *AgentRuntimeRegistry
	projectRoot    string
}

// NewTaskSupervisor constructs a new stage-based task supervisor.
func NewTaskSupervisor(
	taskStore *fsstore.TaskStore,
	runStore *fsstore.RunStore,
	clarStore *fsstore.ClarificationStore,
	registry *infrart.Registry,
	heartbeatMgr *infrart.HeartbeatManager,
	eventSink *events.Sink,
	contextBuilder *contextpack.Builder,
	promptBuilder *prompting.Builder,
	config *loader.ProjectConfig,
	drivers *AgentRuntimeRegistry,
	projectRoot string,
) *TaskSupervisor {
	return &TaskSupervisor{
		taskStore:      taskStore,
		runStore:       runStore,
		clarStore:      clarStore,
		registry:       registry,
		heartbeatMgr:   heartbeatMgr,
		eventSink:      eventSink,
		contextBuilder: contextBuilder,
		promptBuilder:  promptBuilder,
		config:         config,
		drivers:        drivers,
		projectRoot:    projectRoot,
	}
}

// Run executes or resumes the session pipeline for a task until it blocks or reaches review/completion.
func (s *TaskSupervisor) Run(ctx context.Context, t *task.Task) (*rt.RunSession, error) {
	session, err := s.loadOrCreateSession(t)
	if err != nil {
		return nil, err
	}

	for {
		if _, err := s.mergePersistedControlState(t, session); err != nil {
			return nil, err
		}
		if session.PendingHandoff != nil {
			if err := s.runSyntheticHandoff(t, session); err != nil {
				return nil, err
			}
			continue
		}

		lastStage := session.LastStage()
		if (session.ReviewReport != nil || (lastStage != nil && lastStage.Type == rt.StageTypeReview && lastStage.State == rt.StageStateCompleted)) &&
			session.Status != rt.SessionStatusReviewing {
			session.Status = rt.SessionStatusReviewing
			t.Status = task.StatusReviewing
			t.UpdatedAt = time.Now()
			if err := s.persistSession(t, session); err != nil {
				return nil, err
			}
		}

		stageType, err := s.nextStageType(t, session)
		if err != nil {
			return nil, err
		}
		if stageType == "" {
			if err := s.persistSession(t, session); err != nil {
				return nil, err
			}
			return session, nil
		}

		spec, err := s.prepareStageSpec(t, session, stageType)
		if err != nil {
			return nil, err
		}
		if err := s.runAdapterStage(ctx, t, session, spec); err != nil {
			return nil, err
		}

		if _, err := s.mergePersistedControlState(t, session); err != nil {
			return nil, err
		}

		if session.Status == rt.SessionStatusWaitingClarification ||
			session.Status == rt.SessionStatusCanceled ||
			session.Status == rt.SessionStatusReviewing ||
			session.Status == rt.SessionStatusCompleted ||
			session.Status == rt.SessionStatusFailed {
			if err := s.persistSession(t, session); err != nil {
				return nil, err
			}
			return session, nil
		}

		if stageType == rt.StageTypeExecute || stageType == rt.StageTypeValidateFix {
			report, err := s.runValidation(ctx, t, session)
			if err != nil {
				return nil, err
			}
			if report != nil && !report.AllPassed {
				if t.Validation.Mode == task.ValidationModeFull && session.Validation.Attempt < t.Validation.MaxRetries {
					session.Status = rt.SessionStatusQueued
					session.UpdatedAt = time.Now()
					if err := s.persistSession(t, session); err != nil {
						return nil, err
					}
					continue
				}
				session.Status = rt.SessionStatusFailed
				t.Status = task.StatusFailed
				t.UpdatedAt = time.Now()
				if err := s.persistSession(t, session); err != nil {
					return nil, err
				}
				return session, nil
			}
		}
	}
}

func (s *TaskSupervisor) loadOrCreateSession(t *task.Task) (*rt.RunSession, error) {
	if t.Status == task.StatusWaitingClarification || t.Status == task.StatusHandoffPending || t.Status == task.StatusPaused {
		session, err := s.runStore.LatestSession(t.ID)
		if err == nil {
			return session, nil
		}
	}

	// Check for orphaned active runs and clean them up before creating a new session.
	if active, err := s.registry.LoadActiveRun(t.ID); err == nil && active != nil {
		s.registry.UnregisterRun(t.ID, active.RunID)
	}

	runID, err := s.runStore.NextRunID(t.ID)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	session := &rt.RunSession{
		ID:             runID,
		TaskID:         t.ID,
		Status:         rt.SessionStatusQueued,
		CurrentAgentID: t.Agent,
		Validation: rt.ValidationState{
			MaxRetries: t.Validation.MaxRetries,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.runStore.SaveSession(session); err != nil {
		return nil, err
	}
	if err := s.runStore.SaveArtifactManifest(t.ID, session.ID, &session.ArtifactManifest); err != nil {
		return nil, err
	}
	return session, nil
}

func (s *TaskSupervisor) nextStageType(t *task.Task, session *rt.RunSession) (rt.StageType, error) {
	if session.ReviewReport != nil || session.Status == rt.SessionStatusReviewing {
		return "", nil
	}
	if session.BlockedStageType != "" {
		return session.BlockedStageType, nil
	}
	if session.Status == rt.SessionStatusWaitingClarification {
		if t.Clarifications.PendingRequest != nil {
			return "", nil
		}
		return rt.StageTypeClarify, nil
	}
	if session.Status == rt.SessionStatusCanceled ||
		session.Status == rt.SessionStatusCompleted || session.Status == rt.SessionStatusFailed {
		return "", nil
	}
	if session.Validation.Attempt > 0 && !allPassed(session.Validation.LastResults) {
		return rt.StageTypeValidateFix, nil
	}
	last := session.LastStage()
	if last == nil {
		return rt.StageTypeExecute, nil
	}
	if last.Type == rt.StageTypeReview && last.State == rt.StageStateCompleted {
		return "", nil
	}
	if last.State == rt.StageStateCompleted && (last.Type == rt.StageTypeExecute || last.Type == rt.StageTypeClarify || last.Type == rt.StageTypeValidateFix) {
		return rt.StageTypeReview, nil
	}
	return rt.StageTypeExecute, nil
}

func (s *TaskSupervisor) prepareStageSpec(t *task.Task, session *rt.RunSession, stageType rt.StageType) (*rt.StageSpec, error) {
	stageID := fmt.Sprintf("STAGE-%03d", len(session.StageHistory)+1)
	stageDir := s.runStore.StageDir(t.ID, session.ID, stageID)
	if err := os.MkdirAll(stageDir, 0755); err != nil {
		return nil, fmt.Errorf("creating stage dir: %w", err)
	}

	contextDir := ""
	promptPath := ""

	switch stageType {
	case rt.StageTypeExecute:
		var err error
		contextDir, err = s.contextBuilder.Build(t)
		if err != nil {
			return nil, err
		}
		promptContent, err := s.promptBuilder.BuildPrompt(t, contextDir, stageDir)
		if err != nil {
			return nil, err
		}
		promptPath = filepath.Join(stageDir, "prompt.md")
		if err := os.WriteFile(promptPath, []byte(promptContent), 0644); err != nil {
			return nil, err
		}
	case rt.StageTypeClarify:
		var err error
		contextDir, err = s.contextBuilder.Build(t)
		if err != nil {
			return nil, err
		}
		promptContent, err := s.promptBuilder.BuildPrompt(t, contextDir, stageDir)
		if err != nil {
			return nil, err
		}
		promptPath = filepath.Join(stageDir, "clarification_prompt.md")
		if err := os.WriteFile(promptPath, []byte(promptContent), 0644); err != nil {
			return nil, err
		}
	case rt.StageTypeValidateFix:
		var err error
		contextDir, err = s.contextBuilder.Build(t)
		if err != nil {
			return nil, err
		}
		promptPath = filepath.Join(stageDir, "prompt.md")
		if err := os.WriteFile(promptPath, []byte(s.buildValidationFixPrompt(t, session.Validation.LastResults, session.Validation.Attempt, t.Validation.MaxRetries)), 0644); err != nil {
			return nil, err
		}
	case rt.StageTypeReview:
		var err error
		contextDir, err = s.contextBuilder.Build(t)
		if err != nil {
			return nil, err
		}
		promptPath = filepath.Join(stageDir, "review_prompt.md")
		if err := os.WriteFile(promptPath, []byte(s.buildReviewPrompt(t)), 0644); err != nil {
			return nil, err
		}
	}

	taskPath := filepath.Join(stageDir, "task_snapshot.yml")
	taskData, err := yaml.Marshal(t)
	if err != nil {
		return nil, fmt.Errorf("marshaling task snapshot: %w", err)
	}
	if err := os.WriteFile(taskPath, taskData, 0644); err != nil {
		return nil, fmt.Errorf("writing task snapshot: %w", err)
	}

	spec := &rt.StageSpec{
		SessionID:  session.ID,
		TaskID:     t.ID,
		RunID:      session.ID,
		StageID:    stageID,
		Type:       stageType,
		AgentID:    session.CurrentAgentID,
		WorkDir:    s.projectRoot,
		SessionDir: s.runStore.RunDir(t.ID, session.ID),
		StageDir:   stageDir,
		TaskPath:   taskPath,
		ContextDir: contextDir,
		PromptPath: promptPath,
		Input: rt.StageInput{
			Task:             t,
			ArtifactManifest: session.ArtifactManifest,
			Clarifications:   append([]string{}, t.Clarifications.Attached...),
		},
	}

	switch stageType {
	case rt.StageTypeValidateFix:
		spec.Input.Validation = &rt.ValidationStageInput{
			Attempt:        session.Validation.Attempt,
			MaxRetries:     t.Validation.MaxRetries,
			FailedChecks:   failedChecks(session.Validation.LastResults),
			ValidationPath: session.Validation.LastReportPath,
		}
	case rt.StageTypeReview:
		spec.Input.Review = &rt.ReviewStageInput{
			SummaryPath:    s.findArtifactPath(session.ArtifactManifest, "summary.md"),
			DiffPath:       s.findArtifactPath(session.ArtifactManifest, "diff.patch"),
			ValidationPath: session.Validation.LastReportPath,
			ContextPath:    filepath.Join(contextDir, "context.md"),
		}
	}

	specPath := filepath.Join(stageDir, "stage_spec.json")
	if err := writeJSON(specPath, spec); err != nil {
		return nil, err
	}
	return spec, nil
}

func (s *TaskSupervisor) runAdapterStage(ctx context.Context, t *task.Task, session *rt.RunSession, spec *rt.StageSpec) error {
	stage := rt.StageRun{
		StageID: spec.StageID,
		Type:    spec.Type,
		AgentID: spec.AgentID,
		State:   rt.StageStatePending,
		Attempt: s.stageAttempt(session, spec.Type),
	}
	session.StageHistory = append(session.StageHistory, stage)
	now := time.Now()
	current := &session.StageHistory[len(session.StageHistory)-1]
	current.State = rt.StageStateRunning
	current.StartedAt = &now

	if session.BlockedStageType == spec.Type {
		session.BlockedStageType = ""
	}
	session.CurrentStageID = spec.StageID
	session.Status = rt.SessionStatusStageRunning
	session.CurrentAgentID = spec.AgentID
	session.UpdatedAt = now
	t.Status = task.StatusStageRunning
	t.UpdatedAt = now

	runtimeErrLog, err := s.ensureRuntimeErrorArtifact(t.ID, session, spec)
	if err != nil {
		return err
	}

	if err := s.persistSession(t, session); err != nil {
		return err
	}

	profile, driver, err := s.drivers.Resolve(spec.AgentID)
	if err != nil {
		return s.failStage(t, session, current, runtimeErrLog, err, false)
	}
	if !profile.IsEnabled() {
		return s.failStage(t, session, current, runtimeErrLog, fmt.Errorf("agent %s is disabled", spec.AgentID), false)
	}
	if err := s.validateDriver(driver, spec.Type); err != nil {
		return s.failStage(t, session, current, runtimeErrLog, err, true)
	}

	basePrompt := ""
	if spec.PromptPath != "" {
		data, err := os.ReadFile(spec.PromptPath)
		if err != nil {
			return s.failStage(t, session, current, runtimeErrLog, fmt.Errorf("reading base prompt: %w", err), false)
		}
		basePrompt = string(data)
	}
	finalPrompt, err := driver.BuildStagePrompt(basePrompt, spec, session)
	if err != nil {
		return s.failStage(t, session, current, runtimeErrLog, err, false)
	}
	if spec.PromptPath == "" {
		spec.PromptPath = filepath.Join(spec.StageDir, "prompt.md")
	}
	if err := os.WriteFile(spec.PromptPath, []byte(finalPrompt), 0644); err != nil {
		return s.failStage(t, session, current, runtimeErrLog, fmt.Errorf("writing driver prompt: %w", err), false)
	}

	stdoutLog, stderrLog, err := s.ensureStageIOArtifacts(t.ID, session, spec)
	if err != nil {
		return s.failStage(t, session, current, runtimeErrLog, err, false)
	}

	invocation, err := driver.BuildInvocation(profile, spec, session, finalPrompt)
	if err != nil {
		return s.failStage(t, session, current, runtimeErrLog, err, false)
	}

	sessionLog := filepath.Join(spec.SessionDir, "session.log")
	if err := touchFile(sessionLog); err != nil {
		return s.failStage(t, session, current, runtimeErrLog, err, false)
	}
	protocolLog := filepath.Join(spec.SessionDir, "protocol.ndjson")
	if err := touchFile(protocolLog); err != nil {
		return s.failStage(t, session, current, runtimeErrLog, err, false)
	}

	handle, err := StartCLIProcess(ctx, spec, invocation, profile)
	if err != nil {
		return s.failStage(t, session, current, runtimeErrLog, err, false)
	}

	stdoutCh := handle.Stdout()
	stderrCh := handle.Stderr()
	doneCh := handle.Done()

	session.Recovery.AdapterPID = handle.PID()
	session.Recovery.ProcessGroupID = handle.ProcessGroupID()
	session.Recovery.LastHeartbeatAt = now
	session.UpdatedAt = time.Now()
	if err := s.persistSession(t, session); err != nil {
		return err
	}

	active := rt.ActiveRun{
		TaskID:         t.ID,
		RunID:          session.ID,
		SessionID:      session.ID,
		StageID:        spec.StageID,
		Agent:          spec.AgentID,
		Status:         session.Status,
		PID:            handle.PID(),
		ProcessGroupID: handle.ProcessGroupID(),
		StartedAt:      now,
		UpdatedAt:      now,
	}
	if err := s.registry.RegisterRun(active); err != nil {
		return err
	}
	defer s.registry.UnregisterRun(t.ID, session.ID)

	guardSnapshot, err := captureRuntimeGuardSnapshot(
		spec,
		runtimeErrLog,
		stdoutLog,
		stderrLog,
		sessionLog,
		protocolLog,
		filepath.Join(spec.SessionDir, "session.json"),
		filepath.Join(spec.SessionDir, "metadata.json"),
	)
	if err != nil {
		return s.failStage(t, session, current, runtimeErrLog, fmt.Errorf("capturing runtime guard snapshot: %w", err), false)
	}

	// Emit enriched stage_started event.
	s.eventSink.EmitEvent(rt.Event{
		Timestamp: time.Now(),
		TaskID:    t.ID,
		RunID:     session.ID,
		SessionID: session.ID,
		StageID:   spec.StageID,
		AgentID:   spec.AgentID,
		EventType: "stage_started",
		Details:   string(spec.Type),
	})
	// Emit agent_started with PID.
	s.eventSink.EmitEvent(rt.Event{
		Timestamp: time.Now(),
		TaskID:    t.ID,
		RunID:     session.ID,
		SessionID: session.ID,
		StageID:   spec.StageID,
		AgentID:   spec.AgentID,
		EventType: "agent_started",
		Details:   fmt.Sprintf("pid=%d pgid=%d cmd=%s", handle.PID(), handle.ProcessGroupID(), invocation.Command),
	})
	if err := s.heartbeatMgr.Write(t.ID, session.ID); err != nil {
		_ = appendFile(runtimeErrLog, fmt.Sprintf("heartbeat write error: %v\n", err))
	}

	ticker := time.NewTicker(time.Duration(t.Runtime.HeartbeatIntervalSec) * time.Second)
	defer ticker.Stop()

	processExited := false
	var processErr error
	var stdoutBuf strings.Builder
	var stderrBuf strings.Builder

	for !(processExited && stdoutCh == nil && stderrCh == nil) {
		select {
		case <-ctx.Done():
			_ = handle.Kill()
			// Drain channels to allow goroutines to finish.
			s.drainChannels(stdoutCh, stderrCh)
			return s.failStage(t, session, current, runtimeErrLog, ctx.Err(), false)

		case line, ok := <-stdoutCh:
			if ok && stdoutLog != "" {
				stdoutBuf.WriteString(line)
				stdoutBuf.WriteByte('\n')
				if err := appendFile(stdoutLog, line+"\n"); err != nil {
					_ = appendFile(runtimeErrLog, fmt.Sprintf("stdout log write error: %v\n", err))
				}
				_ = appendFile(sessionLog, fmt.Sprintf("[%s] [%s] [stdout] %s\n", time.Now().Format("15:04:05.000"), spec.StageID, line))
				if err := appendFile(protocolLog, line+"\n"); err != nil {
					_ = appendFile(runtimeErrLog, fmt.Sprintf("protocol log write error: %v\n", err))
				}
				rawEventType := "stdout_line"
				if profile.Driver == loader.AgentDriverQwen {
					rawEventType = "protocol_line"
				}
				_ = s.eventSink.EmitEvent(buildStageEvent(spec, rawEventType, line))
				if profile.Driver == loader.AgentDriverQwen && strings.HasPrefix(strings.TrimSpace(line), "{") {
					liveEvents, err := ParseQwenLiveEvents(spec, line)
					if err != nil {
						_ = appendFile(runtimeErrLog, fmt.Sprintf("qwen live parse error: %v\n", err))
					} else {
						for _, ev := range liveEvents {
							_ = s.eventSink.EmitEvent(ev)
						}
					}
				}
			}
			if !ok {
				stdoutCh = nil
			}

		case line, ok := <-stderrCh:
			if ok && stderrLog != "" {
				stderrBuf.WriteString(line)
				stderrBuf.WriteByte('\n')
				if err := appendFile(stderrLog, line+"\n"); err != nil {
					_ = appendFile(runtimeErrLog, fmt.Sprintf("stderr log write error: %v\n", err))
				}
				_ = appendFile(sessionLog, fmt.Sprintf("[%s] [%s] [stderr] %s\n", time.Now().Format("15:04:05.000"), spec.StageID, line))
				_ = s.eventSink.EmitEvent(buildStageEvent(spec, "stderr_line", line))
			}
			if !ok {
				stderrCh = nil
			}

		case err, ok := <-doneCh:
			if !ok {
				doneCh = nil
				continue
			}
			processExited = true
			processErr = err
			doneCh = nil

		case <-ticker.C:
			now := time.Now()
			session.Recovery.LastHeartbeatAt = now
			if err := s.heartbeatMgr.Write(t.ID, session.ID); err != nil {
				_ = appendFile(runtimeErrLog, fmt.Sprintf("heartbeat write error: %v\n", err))
			}
			active.UpdatedAt = now
			if err := s.registry.UpdateRun(active); err != nil {
				_ = appendFile(runtimeErrLog, fmt.Sprintf("registry update error: %v\n", err))
			}
		}
	}

	finished := time.Now()
	current.FinishedAt = &finished
	session.CurrentStageID = ""
	session.UpdatedAt = finished
	stageDuration := ""
	if current.StartedAt != nil {
		stageDuration = fmt.Sprintf(" duration=%s", finished.Sub(*current.StartedAt).Round(time.Millisecond))
	}

	if terminatedBySignal(processErr, syscall.SIGTERM) || terminatedBySignal(processErr, syscall.SIGKILL) {
		current.State = rt.StageStateCanceled
		current.Result = &rt.StageResult{Outcome: "canceled", Message: "process terminated by control signal"}
		session.Status = rt.SessionStatusCanceled
		t.Status = task.StatusCanceled
		t.UpdatedAt = finished
		_ = s.writeStageResultArtifact(t.ID, session, spec.StageID, current.Result)
		s.eventSink.EmitEvent(rt.Event{
			Timestamp: finished,
			TaskID:    t.ID,
			RunID:     session.ID,
			SessionID: session.ID,
			StageID:   spec.StageID,
			AgentID:   spec.AgentID,
			EventType: "stage_completed",
			Details:   "canceled" + stageDuration,
		})
		return s.persistSession(t, session)
	}
	if processErr != nil {
		s.eventSink.EmitEvent(rt.Event{
			Timestamp: finished,
			TaskID:    t.ID,
			RunID:     session.ID,
			SessionID: session.ID,
			StageID:   spec.StageID,
			AgentID:   spec.AgentID,
			EventType: "stage_failed",
			Details:   processErr.Error() + stageDuration,
		})
		return s.failStage(t, session, current, runtimeErrLog, stageProcessError(processErr), false)
	}
	violations, err := detectRuntimeViolations(guardSnapshot)
	if err != nil {
		s.eventSink.EmitEvent(rt.Event{
			Timestamp: finished,
			TaskID:    t.ID,
			RunID:     session.ID,
			SessionID: session.ID,
			StageID:   spec.StageID,
			AgentID:   spec.AgentID,
			EventType: "stage_failed",
			Details:   "runtime guard error: " + err.Error() + stageDuration,
		})
		return s.failStage(t, session, current, runtimeErrLog, fmt.Errorf("checking runtime guard: %w", err), false)
	}
	if len(violations) > 0 {
		msg := fmt.Sprintf("agent wrote forbidden files under .agentctl: %s", strings.Join(violations, ", "))
		s.eventSink.EmitEvent(rt.Event{
			Timestamp: finished,
			TaskID:    t.ID,
			RunID:     session.ID,
			SessionID: session.ID,
			StageID:   spec.StageID,
			AgentID:   spec.AgentID,
			EventType: "runtime_violation",
			Details:   strings.Join(violations, ", "),
		})
		s.eventSink.EmitEvent(rt.Event{
			Timestamp: finished,
			TaskID:    t.ID,
			RunID:     session.ID,
			SessionID: session.ID,
			StageID:   spec.StageID,
			AgentID:   spec.AgentID,
			EventType: "stage_failed",
			Details:   msg + stageDuration,
		})
		return s.failStage(t, session, current, runtimeErrLog, errors.New(msg), false)
	}

	parsed, err := driver.ParseStageOutput(spec, &StageCapture{
		Stdout:     stdoutBuf.String(),
		Stderr:     stderrBuf.String(),
		ProcessErr: processErr,
	})
	if err != nil {
		s.eventSink.EmitEvent(rt.Event{
			Timestamp: finished,
			TaskID:    t.ID,
			RunID:     session.ID,
			SessionID: session.ID,
			StageID:   spec.StageID,
			AgentID:   spec.AgentID,
			EventType: "stage_failed",
			Details:   "parse error: " + err.Error() + stageDuration,
		})
		return s.failStage(t, session, current, runtimeErrLog, err, false)
	}
	if parsed.ExternalSessionID != "" {
		session.DriverState.ExternalSessionID = parsed.ExternalSessionID
	}
	if err := s.materializeParsedOutput(t, session, spec, parsed); err != nil {
		return s.failStage(t, session, current, runtimeErrLog, err, false)
	}
	if _, err := s.mergePersistedControlState(t, session); err != nil {
		return s.failStage(t, session, current, runtimeErrLog, err, false)
	}

	current.Result = &parsed.Result
	switch parsed.Result.Outcome {
	case "completed":
		current.State = rt.StageStateCompleted
		if session.PendingHandoff != nil {
			session.Status = rt.SessionStatusHandoffPending
			t.Status = task.StatusHandoffPending
		} else if spec.Type == rt.StageTypeReview {
			session.Status = rt.SessionStatusReviewing
			t.Status = task.StatusReviewing
		} else {
			session.Status = rt.SessionStatusQueued
		}
	case "clarification_requested":
		current.State = rt.StageStateCompleted
		session.Status = rt.SessionStatusWaitingClarification
		t.Status = task.StatusWaitingClarification
	case "handoff_requested", "handoff_pending":
		current.State = rt.StageStateCompleted
		session.Status = rt.SessionStatusHandoffPending
		t.Status = task.StatusHandoffPending
		session.PendingHandoff = &rt.PendingHandoff{
			NextAgentID: parsed.Result.NextAgentID,
			RequestedAt: time.Now(),
		}
	case "canceled":
		current.State = rt.StageStateCanceled
		session.Status = rt.SessionStatusCanceled
		t.Status = task.StatusCanceled
	default:
		current.State = rt.StageStateFailed
		session.Status = rt.SessionStatusFailed
		t.Status = task.StatusFailed
	}
	t.UpdatedAt = finished
	if err := s.writeStageResultArtifact(t.ID, session, spec.StageID, current.Result); err != nil {
		return s.failStage(t, session, current, runtimeErrLog, err, false)
	}
	s.eventSink.EmitEvent(rt.Event{
		Timestamp: finished,
		TaskID:    t.ID,
		RunID:     session.ID,
		SessionID: session.ID,
		StageID:   spec.StageID,
		AgentID:   spec.AgentID,
		EventType: "stage_completed",
		Details:   parsed.Result.Outcome + stageDuration,
	})
	return s.persistSession(t, session)
}

func (s *TaskSupervisor) runValidation(ctx context.Context, t *task.Task, session *rt.RunSession) (*validation.Report, error) {
	if len(t.Validation.Commands) == 0 {
		session.Validation.LastResults = nil
		session.Validation.LastReportPath = ""
		session.Validation.Attempt = 0
		session.UpdatedAt = time.Now()
		return nil, s.persistSession(t, session)
	}

	s.eventSink.EmitEvent(rt.Event{
		Timestamp: time.Now(),
		TaskID:    t.ID,
		RunID:     session.ID,
		SessionID: session.ID,
		AgentID:   session.CurrentAgentID,
		EventType: "validation_started",
		Details:   fmt.Sprintf("commands=%d attempt=%d", len(t.Validation.Commands), session.Validation.Attempt+1),
	})

	results := make([]validation.CheckResult, 0, len(t.Validation.Commands))
	for _, cmdStr := range t.Validation.Commands {
		results = append(results, runValidationCommand(ctx, s.projectRoot, cmdStr))
	}

	passed := allPassed(results)
	s.eventSink.EmitEvent(rt.Event{
		Timestamp: time.Now(),
		TaskID:    t.ID,
		RunID:     session.ID,
		SessionID: session.ID,
		AgentID:   session.CurrentAgentID,
		EventType: "validation_completed",
		Details:   fmt.Sprintf("passed=%v", passed),
	})

	if !passed {
		session.Validation.Attempt++
	} else {
		session.Validation.Attempt = 0
	}
	session.Validation.LastResults = results
	report := &validation.Report{
		TaskID:     t.ID,
		RunID:      session.ID,
		Mode:       string(t.Validation.Mode),
		MaxRetries: t.Validation.MaxRetries,
		CreatedAt:  time.Now(),
		Results:    results,
		AllPassed:  passed,
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := s.runStore.WriteArtifact(t.ID, session.ID, "validation.json", data); err != nil {
		return nil, err
	}
	session.Validation.LastReportPath = filepath.Join(s.runStore.RunDir(t.ID, session.ID), "validation.json")
	session.ArtifactManifest.Add(rt.ArtifactRecord{
		Name:      "validation.json",
		Kind:      "validation_report",
		Path:      session.Validation.LastReportPath,
		MediaType: "application/json",
		CreatedAt: time.Now(),
	})
	if err := s.runStore.SaveArtifactManifest(t.ID, session.ID, &session.ArtifactManifest); err != nil {
		return nil, err
	}
	if err := s.persistSession(t, session); err != nil {
		return nil, err
	}
	return report, nil
}

func (s *TaskSupervisor) runSyntheticHandoff(t *task.Task, session *rt.RunSession) error {
	if session.PendingHandoff == nil {
		return nil
	}
	stageID := fmt.Sprintf("STAGE-%03d", len(session.StageHistory)+1)
	now := time.Now()
	stage := rt.StageRun{
		StageID:    stageID,
		Type:       rt.StageTypeHandoff,
		AgentID:    session.CurrentAgentID,
		State:      rt.StageStateCompleted,
		Attempt:    s.stageAttempt(session, rt.StageTypeHandoff),
		StartedAt:  &now,
		FinishedAt: &now,
		Result: &rt.StageResult{
			Outcome:     "completed",
			Message:     "handoff completed",
			NextAgentID: session.PendingHandoff.NextAgentID,
		},
	}
	session.StageHistory = append(session.StageHistory, stage)
	handoffPath := filepath.Join(s.runStore.RunDir(t.ID, session.ID), "handoff.json")
	if err := writeJSON(handoffPath, session.PendingHandoff); err != nil {
		return err
	}
	session.ArtifactManifest.Add(rt.ArtifactRecord{
		Name:      "handoff.json",
		Kind:      "handoff",
		Path:      handoffPath,
		StageID:   stageID,
		MediaType: "application/json",
		CreatedAt: now,
	})
	t.Agent = session.PendingHandoff.NextAgentID
	t.Status = task.StatusQueued
	t.UpdatedAt = now
	session.CurrentAgentID = t.Agent
	session.PendingHandoff = nil
	session.Status = rt.SessionStatusQueued
	session.UpdatedAt = now
	s.eventSink.Emit(t.ID, session.ID, "handoff_completed", t.Agent)
	return s.persistSession(t, session)
}

func (s *TaskSupervisor) persistSession(t *task.Task, session *rt.RunSession) error {
	session.UpdatedAt = time.Now()
	if err := s.runStore.SaveSession(session); err != nil {
		return err
	}
	if err := s.runStore.SaveArtifactManifest(t.ID, session.ID, &session.ArtifactManifest); err != nil {
		return err
	}
	return s.taskStore.Save(t)
}

func (s *TaskSupervisor) mergePersistedControlState(t *task.Task, session *rt.RunSession) (bool, error) {
	persisted, err := s.runStore.LoadSession(session.TaskID, session.ID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}

	if session.PendingHandoff == nil && persisted.PendingHandoff != nil {
		handoff := *persisted.PendingHandoff
		session.PendingHandoff = &handoff
		if session.Status != rt.SessionStatusCanceled &&
			session.Status != rt.SessionStatusCompleted &&
			session.Status != rt.SessionStatusFailed {
			session.Status = rt.SessionStatusHandoffPending
			t.Status = task.StatusHandoffPending
			t.UpdatedAt = time.Now()
		}
		return true, nil
	}

	return false, nil
}

func (s *TaskSupervisor) ensureRuntimeErrorArtifact(taskID string, session *rt.RunSession, spec *rt.StageSpec) (string, error) {
	path := filepath.Join(spec.StageDir, "runtime_errors.log")
	if err := touchFile(path); err != nil {
		return "", err
	}
	session.ArtifactManifest.Add(rt.ArtifactRecord{
		Name:      "runtime_errors.log",
		Kind:      "runtime_error_log",
		Path:      path,
		StageID:   spec.StageID,
		MediaType: "text/plain",
		CreatedAt: time.Now(),
	})
	if err := s.runStore.SaveArtifactManifest(taskID, session.ID, &session.ArtifactManifest); err != nil {
		return "", err
	}
	return path, nil
}

func (s *TaskSupervisor) ensureStageIOArtifacts(taskID string, session *rt.RunSession, spec *rt.StageSpec) (string, string, error) {
	stdoutLog := filepath.Join(spec.StageDir, "stdout.log")
	stderrLog := filepath.Join(spec.StageDir, "stderr.log")
	if err := touchFile(stdoutLog); err != nil {
		return "", "", err
	}
	if err := touchFile(stderrLog); err != nil {
		return "", "", err
	}
	session.ArtifactManifest.Add(rt.ArtifactRecord{
		Name:      "stdout.log",
		Kind:      "stdout_log",
		Path:      stdoutLog,
		StageID:   spec.StageID,
		MediaType: "text/plain",
		CreatedAt: time.Now(),
	})
	session.ArtifactManifest.Add(rt.ArtifactRecord{
		Name:      "stderr.log",
		Kind:      "stderr_log",
		Path:      stderrLog,
		StageID:   spec.StageID,
		MediaType: "text/plain",
		CreatedAt: time.Now(),
	})
	if err := s.runStore.SaveArtifactManifest(taskID, session.ID, &session.ArtifactManifest); err != nil {
		return "", "", err
	}
	return stdoutLog, stderrLog, nil
}

func (s *TaskSupervisor) failStage(
	t *task.Task,
	session *rt.RunSession,
	stage *rt.StageRun,
	runtimeErrorsPath string,
	err error,
	blockStage bool,
) error {
	if err == nil {
		return s.persistSession(t, session)
	}
	if runtimeErrorsPath != "" {
		_ = appendFile(runtimeErrorsPath, err.Error()+"\n")
	}
	now := time.Now()
	stage.State = rt.StageStateFailed
	stage.Result = &rt.StageResult{Outcome: "failed", Message: err.Error()}
	stage.FinishedAt = &now
	session.Status = rt.SessionStatusFailed
	session.CurrentStageID = ""
	session.UpdatedAt = now
	if blockStage {
		session.BlockedStageType = stage.Type
	}
	t.Status = task.StatusFailed
	t.UpdatedAt = now
	return s.persistSession(t, session)
}

// drainChannels reads remaining items from stdout/stderr channels to unblock goroutines.
func (s *TaskSupervisor) drainChannels(stdoutCh, stderrCh <-chan string) {
	for {
		select {
		case _, ok := <-stdoutCh:
			if !ok {
				stdoutCh = nil
			}
		case _, ok := <-stderrCh:
			if !ok {
				stderrCh = nil
			}
		}
		if stdoutCh == nil && stderrCh == nil {
			return
		}
	}
}

func (s *TaskSupervisor) validateDriver(driver AgentCLIDriver, stageType rt.StageType) error {
	if !driver.SupportsStage(stageType) {
		return fmt.Errorf("driver %s does not support %s stage", driver.Name(), stageType)
	}
	return nil
}

func (s *TaskSupervisor) stageAttempt(session *rt.RunSession, stageType rt.StageType) int {
	attempt := 0
	for _, stage := range session.StageHistory {
		if stage.Type == stageType {
			attempt++
		}
	}
	return attempt + 1
}

func stageProcessError(processErr error) error {
	if processErr == nil {
		return nil
	}
	if exitCode, ok := processExitCode(processErr); ok {
		return fmt.Errorf("cli exited with code %d", exitCode)
	}
	return fmt.Errorf("cli process failed: %w", processErr)
}

func processExitCode(err error) (int, bool) {
	type exitCoder interface {
		ExitCode() int
	}
	if err == nil {
		return 0, false
	}
	if exitErr, ok := err.(exitCoder); ok {
		return exitErr.ExitCode(), true
	}
	return 0, false
}

func (s *TaskSupervisor) buildValidationFixPrompt(t *task.Task, results []validation.CheckResult, attempt, maxRetries int) string {
	var buf bytes.Buffer
	buf.WriteString("# Validation Fix Required\n\n")
	buf.WriteString(fmt.Sprintf("Task: %s\n", t.ID))
	buf.WriteString(fmt.Sprintf("Goal: %s\n", t.Goal))
	buf.WriteString(fmt.Sprintf("Attempt: %d/%d\n\n", attempt, maxRetries))
	buf.WriteString("Fix the failing validation checks described in the structured stage input.\n\n")
	for _, res := range results {
		if !res.Passed {
			buf.WriteString(fmt.Sprintf("- %s (exit=%d)\n", res.Command, res.ExitCode))
		}
	}
	return buf.String()
}

func (s *TaskSupervisor) buildReviewPrompt(t *task.Task) string {
	return strings.TrimSpace(fmt.Sprintf(`
# Reviewer Stage

Review the produced summary, diff and validation report for task %s.
Return a structured review report with findings and an overall summary.
`, t.ID))
}

func (s *TaskSupervisor) findArtifactPath(manifest rt.ArtifactManifest, name string) string {
	for i := len(manifest.Items) - 1; i >= 0; i-- {
		if manifest.Items[i].Name == name {
			return manifest.Items[i].Path
		}
	}
	return ""
}

func failedChecks(results []validation.CheckResult) []validation.CheckResult {
	var failed []validation.CheckResult
	for _, result := range results {
		if !result.Passed {
			failed = append(failed, result)
		}
	}
	return failed
}

func allPassed(results []validation.CheckResult) bool {
	for _, result := range results {
		if !result.Passed {
			return false
		}
	}
	return true
}

func runValidationCommand(ctx context.Context, projectRoot, cmdStr string) validation.CheckResult {
	start := time.Now()
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	cmd.Dir = projectRoot

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	duration := time.Since(start)
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}
	return validation.CheckResult{
		Command:  cmdStr,
		ExitCode: exitCode,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		Duration: duration,
		Passed:   exitCode == 0,
	}
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func appendFile(path, content string) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(content)
	return err
}

func touchFile(path string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	return f.Close()
}

func terminatedBySignal(err error, sig syscall.Signal) bool {
	if err == nil {
		return false
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return false
	}
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	return ok && status.Signaled() && status.Signal() == sig
}

func (s *TaskSupervisor) materializeParsedOutput(
	t *task.Task,
	session *rt.RunSession,
	spec *rt.StageSpec,
	parsed *ParsedStageOutput,
) error {
	now := time.Now()

	if parsed.StructuredLogName != "" && len(parsed.StructuredLog) > 0 {
		path := filepath.Join(spec.StageDir, parsed.StructuredLogName)
		if err := os.WriteFile(path, parsed.StructuredLog, 0644); err != nil {
			return err
		}
		s.addArtifact(session, rt.ArtifactRecord{
			Name:      parsed.StructuredLogName,
			Kind:      "structured_log",
			Path:      path,
			StageID:   spec.StageID,
			MediaType: "application/json",
			CreatedAt: now,
		})
	}

	if parsed.Summary != "" {
		if err := s.runStore.WriteArtifact(t.ID, session.ID, "summary.md", []byte(parsed.Summary)); err != nil {
			return err
		}
		parsed.Result.SummaryPath = filepath.Join(s.runStore.RunDir(t.ID, session.ID), "summary.md")
		s.addArtifact(session, rt.ArtifactRecord{
			Name:      "summary.md",
			Kind:      "summary",
			Path:      parsed.Result.SummaryPath,
			StageID:   spec.StageID,
			MediaType: "text/markdown",
			CreatedAt: now,
		})
	}

	if spec.Type == rt.StageTypeExecute || spec.Type == rt.StageTypeClarify || spec.Type == rt.StageTypeValidateFix {
		diffPath, changedFilesPath, err := s.captureWorkspaceDiffArtifacts(t.ID, session.ID)
		if err != nil {
			return err
		}
		if diffPath != "" {
			parsed.Result.DiffPath = diffPath
			s.addArtifact(session, rt.ArtifactRecord{
				Name:      "diff.patch",
				Kind:      "diff",
				Path:      diffPath,
				StageID:   spec.StageID,
				MediaType: "text/x-diff",
				CreatedAt: now,
			})
		}
		if changedFilesPath != "" {
			s.addArtifact(session, rt.ArtifactRecord{
				Name:      "changed_files.json",
				Kind:      "changed_files",
				Path:      changedFilesPath,
				StageID:   spec.StageID,
				MediaType: "application/json",
				CreatedAt: now,
			})
		}
	}

	if parsed.ReviewReport != nil {
		parsed.ReviewReport.StageID = spec.StageID
		parsed.ReviewReport.CreatedAt = now
		data, err := json.MarshalIndent(parsed.ReviewReport, "", "  ")
		if err != nil {
			return err
		}
		if err := s.runStore.WriteArtifact(t.ID, session.ID, "review_report.json", data); err != nil {
			return err
		}
		path := filepath.Join(s.runStore.RunDir(t.ID, session.ID), "review_report.json")
		parsed.Result.ReviewPath = path
		parsed.ReviewReport.ArtifactPath = path
		session.ReviewReport = parsed.ReviewReport
		s.addArtifact(session, rt.ArtifactRecord{
			Name:      "review_report.json",
			Kind:      "review_report",
			Path:      path,
			StageID:   spec.StageID,
			MediaType: "application/json",
			CreatedAt: now,
		})
	}

	if parsed.Clarification != nil {
		reqID := parsed.Clarification.RequestID
		if reqID == "" {
			reqID = fmt.Sprintf("CLAR-REQ-%03d", len(t.Clarifications.Attached)+1)
		}
		req := &clarification.Request{
			TaskID:      t.ID,
			RequestID:   reqID,
			CreatedBy:   spec.AgentID,
			Reason:      parsed.Clarification.Reason,
			Questions:   parsed.Clarification.Questions,
			ContextRefs: parsed.Clarification.ContextRefs,
			CreatedAt:   now,
		}
		path, err := s.clarStore.SaveRequest(req)
		if err != nil {
			return err
		}
		t.SetPendingClarification(reqID)
		session.PendingClarificationID = &reqID
		s.addArtifact(session, rt.ArtifactRecord{
			Name:      filepath.Base(path),
			Kind:      "clarification_request",
			Path:      path,
			StageID:   spec.StageID,
			MediaType: "application/yaml",
			CreatedAt: now,
		})
	}

	if err := s.runStore.SaveArtifactManifest(t.ID, session.ID, &session.ArtifactManifest); err != nil {
		return err
	}
	return nil
}

func (s *TaskSupervisor) captureWorkspaceDiffArtifacts(taskID, sessionID string) (string, string, error) {
	if _, err := os.Stat(filepath.Join(s.projectRoot, ".git")); err != nil {
		return "", "", nil
	}

	trackedFiles, err := gitLinesAllowEmpty(s.projectRoot, "diff", "--name-only")
	if err != nil {
		return "", "", nil
	}
	trackedFiles = filterChangedFiles(trackedFiles)

	trackedDiff, err := buildTrackedDiff(s.projectRoot, trackedFiles)
	if err != nil {
		return "", "", nil
	}

	untrackedFiles, err := gitLinesAllowEmpty(s.projectRoot, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return "", "", nil
	}
	untrackedFiles = filterChangedFiles(untrackedFiles)

	untrackedDiff, err := buildUntrackedDiff(s.projectRoot, untrackedFiles)
	if err != nil {
		return "", "", err
	}

	diffOut := append([]byte{}, trackedDiff...)
	if len(untrackedDiff) > 0 {
		if len(diffOut) > 0 && !bytes.HasSuffix(diffOut, []byte("\n")) {
			diffOut = append(diffOut, '\n')
		}
		diffOut = append(diffOut, untrackedDiff...)
	}

	diffPath := ""
	if len(diffOut) > 0 {
		if err := s.runStore.WriteArtifact(taskID, sessionID, "diff.patch", diffOut); err != nil {
			return "", "", err
		}
		diffPath = filepath.Join(s.runStore.RunDir(taskID, sessionID), "diff.patch")
	}

	fileNames := mergeChangedFiles(trackedFiles, untrackedFiles)
	if len(fileNames) == 0 {
		return diffPath, "", nil
	}

	data, err := json.MarshalIndent(fileNames, "", "  ")
	if err != nil {
		return diffPath, "", err
	}
	if err := s.runStore.WriteArtifact(taskID, sessionID, "changed_files.json", data); err != nil {
		return diffPath, "", err
	}
	return diffPath, filepath.Join(s.runStore.RunDir(taskID, sessionID), "changed_files.json"), nil
}

func gitOutputAllowChanges(root string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return out, nil
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return out, nil
	}
	return nil, err
}

func gitLinesAllowEmpty(root string, args ...string) ([]string, error) {
	out, err := gitOutputAllowChanges(root, args...)
	if err != nil {
		return nil, err
	}

	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" {
		return nil, nil
	}
	return strings.Split(trimmed, "\n"), nil
}

func buildTrackedDiff(root string, files []string) ([]byte, error) {
	if len(files) == 0 {
		return nil, nil
	}

	args := append([]string{"diff", "--no-ext-diff", "--binary", "--"}, files...)
	return gitOutputAllowChanges(root, args...)
}

func buildUntrackedDiff(root string, files []string) ([]byte, error) {
	if len(files) == 0 {
		return nil, nil
	}

	var combined bytes.Buffer
	for _, file := range files {
		if file == "" {
			continue
		}
		diff, err := gitOutputAllowChanges(root, "diff", "--no-index", "--binary", "--", "/dev/null", file)
		if err != nil {
			return nil, err
		}
		if len(diff) == 0 {
			continue
		}
		if combined.Len() > 0 && !bytes.HasSuffix(combined.Bytes(), []byte("\n")) {
			combined.WriteByte('\n')
		}
		combined.Write(diff)
	}
	return combined.Bytes(), nil
}

func filterChangedFiles(files []string) []string {
	if len(files) == 0 {
		return nil
	}

	filtered := make([]string, 0, len(files))
	for _, file := range files {
		if shouldIgnoreDiffFile(file) {
			continue
		}
		filtered = append(filtered, file)
	}
	return filtered
}

func shouldIgnoreDiffFile(path string) bool {
	clean := filepath.Clean(path)
	if clean == "." || clean == "" {
		return true
	}
	if clean == ".agentctl" || strings.HasPrefix(clean, ".agentctl"+string(filepath.Separator)) {
		return true
	}
	if !strings.Contains(clean, string(filepath.Separator)) {
		switch clean {
		case "summary.md", "diff.patch", "changed_files.json", "review_report.json", "validation.json", "handoff.json":
			return true
		}
	}
	return false
}

func mergeChangedFiles(trackedFiles, untrackedFiles []string) []string {
	if len(trackedFiles) == 0 && len(untrackedFiles) == 0 {
		return nil
	}

	merged := make([]string, 0, len(trackedFiles)+len(untrackedFiles))
	seen := make(map[string]struct{}, len(trackedFiles)+len(untrackedFiles))
	for _, file := range trackedFiles {
		if file == "" {
			continue
		}
		if _, ok := seen[file]; ok {
			continue
		}
		seen[file] = struct{}{}
		merged = append(merged, file)
	}
	for _, file := range untrackedFiles {
		if file == "" {
			continue
		}
		if _, ok := seen[file]; ok {
			continue
		}
		seen[file] = struct{}{}
		merged = append(merged, file)
	}
	return merged
}

func (s *TaskSupervisor) writeStageResultArtifact(taskID string, session *rt.RunSession, stageID string, result *rt.StageResult) error {
	if result == nil {
		return nil
	}
	path := filepath.Join(s.runStore.StageDir(taskID, session.ID, stageID), "stage_result.json")
	if err := writeJSON(path, result); err != nil {
		return err
	}
	s.addArtifact(session, rt.ArtifactRecord{
		Name:      "stage_result.json",
		Kind:      "stage_result",
		Path:      path,
		StageID:   stageID,
		MediaType: "application/json",
		CreatedAt: time.Now(),
	})
	return s.runStore.SaveArtifactManifest(taskID, session.ID, &session.ArtifactManifest)
}

func (s *TaskSupervisor) addArtifact(session *rt.RunSession, item rt.ArtifactRecord) {
	session.ArtifactManifest.Add(item)
}
