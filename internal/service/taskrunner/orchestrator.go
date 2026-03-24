package taskrunner

import (
	"context"
	"fmt"
	"syscall"
	"time"

	"github.com/docup/agentctl/internal/config/loader"
	rt "github.com/docup/agentctl/internal/core/runtime"
	"github.com/docup/agentctl/internal/core/task"
	"github.com/docup/agentctl/internal/infra/events"
	"github.com/docup/agentctl/internal/infra/fsstore"
	infrart "github.com/docup/agentctl/internal/infra/runtime"
)

// Orchestrator coordinates task execution, control and routing around the stage-based supervisor.
type Orchestrator struct {
	taskStore  *fsstore.TaskStore
	runStore   *fsstore.RunStore
	registry   *infrart.Registry
	eventSink  *events.Sink
	config     *loader.ProjectConfig
	supervisor *TaskSupervisor
}

// NewOrchestrator creates a task runner orchestrator.
func NewOrchestrator(
	taskStore *fsstore.TaskStore,
	runStore *fsstore.RunStore,
	registry *infrart.Registry,
	eventSink *events.Sink,
	config *loader.ProjectConfig,
	supervisor *TaskSupervisor,
) *Orchestrator {
	return &Orchestrator{
		taskStore:  taskStore,
		runStore:   runStore,
		registry:   registry,
		eventSink:  eventSink,
		config:     config,
		supervisor: supervisor,
	}
}

// Run executes or continues the stage-based pipeline for a task.
func (o *Orchestrator) Run(ctx context.Context, taskID string) error {
	t, err := o.taskStore.Load(taskID)
	if err != nil {
		return err
	}

	if t.Status.IsTerminal() {
		return fmt.Errorf("cannot run task %s: task is already %s", taskID, t.Status)
	}
	if t.Status == task.StatusStageRunning || t.Status == task.StatusQueued {
		return fmt.Errorf("cannot run task %s: task is already in progress (status: %s)", taskID, t.Status)
	}

	changed, err := o.validateAndNormalizeForRun(t)
	if err != nil {
		return err
	}
	if changed {
		if err := o.taskStore.Save(t); err != nil {
			return fmt.Errorf("saving normalized task: %w", err)
		}
	}

	if t.Status == task.StatusDraft || t.Status == task.StatusReadyToResume ||
		t.Status == task.StatusRejected || t.Status == task.StatusFailed ||
		t.Status == task.StatusPaused || t.Status == task.StatusHandoffPending ||
		(t.Status == task.StatusWaitingClarification && t.Clarifications.PendingRequest == nil) {
		t.Status = task.StatusQueued
		t.UpdatedAt = time.Now()
		if err := o.taskStore.Save(t); err != nil {
			return err
		}
	}

	o.eventSink.Emit(taskID, "", "queued", "")
	_, err = o.supervisor.Run(ctx, t)
	return err
}

// Stop sends SIGTERM to the live CLI process group.
func (o *Orchestrator) Stop(taskID string) error {
	active, err := o.registry.LoadActiveRun(taskID)
	if err != nil {
		return err
	}
	if active == nil {
		return fmt.Errorf("task %s is not running", taskID)
	}
	if active.ProcessGroupID > 0 {
		if err := syscall.Kill(-active.ProcessGroupID, syscall.SIGTERM); err != nil {
			return err
		}
	} else if active.PID > 0 {
		if err := syscall.Kill(active.PID, syscall.SIGTERM); err != nil {
			return err
		}
	}
	o.eventSink.Emit(taskID, active.RunID, "cancel_requested", "")
	return nil
}

// Kill force-kills a live adapter process group.
func (o *Orchestrator) Kill(taskID string) error {
	active, err := o.registry.LoadActiveRun(taskID)
	if err != nil {
		return err
	}
	if active == nil {
		return fmt.Errorf("task %s is not running", taskID)
	}
	if active.ProcessGroupID > 0 {
		if err := syscall.Kill(-active.ProcessGroupID, syscall.SIGKILL); err != nil {
			return err
		}
	} else if active.PID > 0 {
		if err := syscall.Kill(active.PID, syscall.SIGKILL); err != nil {
			return err
		}
	}
	t, err := o.taskStore.Load(taskID)
	if err == nil {
		t.Status = task.StatusCanceled
		t.UpdatedAt = time.Now()
		_ = o.taskStore.Save(t)
	}
	o.eventSink.Emit(taskID, active.RunID, "killed", "")
	return nil
}

// Cancel cancels a task that is not actively running.
func (o *Orchestrator) Cancel(taskID string) error {
	if active, err := o.registry.LoadActiveRun(taskID); err == nil && active != nil {
		return fmt.Errorf("cannot cancel running task %s; use stop instead", taskID)
	}
	t, err := o.taskStore.Load(taskID)
	if err != nil {
		return err
	}
	if err := t.TransitionTo(task.StatusCanceled); err != nil {
		return fmt.Errorf("cannot cancel task: %w", err)
	}
	if err := o.taskStore.Save(t); err != nil {
		return err
	}
	o.eventSink.Emit(taskID, "", "canceled", "")
	return nil
}

// Accept marks a reviewed task as completed.
func (o *Orchestrator) Accept(taskID string) error {
	t, err := o.taskStore.Load(taskID)
	if err != nil {
		return err
	}
	if err := t.TransitionTo(task.StatusCompleted); err != nil {
		return fmt.Errorf("cannot accept task: %w", err)
	}

	now := time.Now()
	var session *rt.RunSession
	if latest, err := o.runStore.LatestSession(taskID); err == nil && latest != nil {
		session = latest
		session.Status = rt.SessionStatusCompleted
		session.UpdatedAt = now
		session.CompletedAt = &now
		if err := o.runStore.SaveSession(session); err != nil {
			return fmt.Errorf("saving completed session: %w", err)
		}
	}

	if err := o.taskStore.Save(t); err != nil {
		return err
	}
	if session != nil {
		o.eventSink.EmitEvent(rt.Event{
			Timestamp: now,
			TaskID:    taskID,
			RunID:     session.ID,
			SessionID: session.ID,
			AgentID:   session.CurrentAgentID,
			EventType: "completed",
			Details:   "accepted",
		})
		return nil
	}
	o.eventSink.Emit(taskID, "", "completed", "accepted")
	return nil
}

// Reject marks a reviewed task as rejected.
func (o *Orchestrator) Reject(taskID, reason string) error {
	t, err := o.taskStore.Load(taskID)
	if err != nil {
		return err
	}
	if err := t.TransitionTo(task.StatusRejected); err != nil {
		return fmt.Errorf("cannot reject task: %w", err)
	}
	if err := o.taskStore.Save(t); err != nil {
		return err
	}
	o.eventSink.Emit(taskID, "", "rejected", reason)
	return nil
}

// Route schedules a stage-level handoff when a session exists, or updates the draft task agent otherwise.
func (o *Orchestrator) Route(taskID, agentID, reason string) error {
	t, err := o.taskStore.Load(taskID)
	if err != nil {
		return err
	}
	session, err := o.runStore.LatestSession(taskID)
	if err == nil && session != nil && session.Status != rt.SessionStatusCompleted && session.Status != rt.SessionStatusCanceled {
		session.PendingHandoff = &rt.PendingHandoff{
			NextAgentID: agentID,
			Reason:      reason,
			RequestedAt: time.Now(),
		}
		session.Status = rt.SessionStatusHandoffPending
		session.UpdatedAt = time.Now()
		t.Status = task.StatusHandoffPending
		t.UpdatedAt = time.Now()
		if err := o.runStore.SaveSession(session); err != nil {
			return err
		}
		if err := o.taskStore.Save(t); err != nil {
			return err
		}
		o.eventSink.Emit(taskID, session.ID, "handoff_pending", agentID)
		return nil
	}

	t.Agent = agentID
	t.UpdatedAt = time.Now()
	return o.taskStore.Save(t)
}

func (o *Orchestrator) validateAndNormalizeForRun(t *task.Task) (bool, error) {
	changed := false

	if t.Agent == "" {
		t.Agent = o.config.Execution.DefaultAgent
		changed = true
	}
	if len(t.PromptTemplates.Builtin) == 0 {
		t.PromptTemplates.Builtin = []string{o.config.Prompting.DefaultTemplate}
		changed = true
	}
	if t.Title == "" {
		return changed, fmt.Errorf("task %s is missing required field: title", t.ID)
	}
	if t.Goal == "" {
		return changed, fmt.Errorf("task %s is missing required field: goal", t.ID)
	}
	if _, _, err := o.supervisor.drivers.Resolve(t.Agent); err != nil {
		return changed, fmt.Errorf("task %s references unknown agent %q: %w", t.ID, t.Agent, err)
	}
	return changed, nil
}
