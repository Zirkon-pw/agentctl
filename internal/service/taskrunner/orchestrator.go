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

// Resume resumes a paused live session or continues the next blocked stage.
func (o *Orchestrator) Resume(ctx context.Context, taskID string) error {
	active, err := o.registry.LoadActiveRun(taskID)
	if err != nil {
		return err
	}
	if active != nil {
		if !active.Capabilities.SupportsResume {
			return fmt.Errorf("agent %s does not support resume", active.Agent)
		}
		return o.registry.AppendCommand(rt.ProtocolCommand{
			SessionID: active.RunID,
			TaskID:    taskID,
			RunID:     active.RunID,
			StageID:   active.StageID,
			Seq:       time.Now().UnixNano(),
			Timestamp: time.Now(),
			Type:      rt.CommandTypeResume,
		})
	}
	return o.Run(ctx, taskID)
}

// Stop sends a graceful cancel command to the live adapter session.
func (o *Orchestrator) Stop(taskID string) error {
	active, err := o.registry.LoadActiveRun(taskID)
	if err != nil {
		return err
	}
	if active == nil {
		return fmt.Errorf("task %s is not running", taskID)
	}
	if !active.Capabilities.SupportsCancel {
		return fmt.Errorf("agent %s does not support cancel", active.Agent)
	}
	o.eventSink.Emit(taskID, active.RunID, "cancel_requested", "")
	return o.registry.AppendCommand(rt.ProtocolCommand{
		SessionID: active.RunID,
		TaskID:    taskID,
		RunID:     active.RunID,
		StageID:   active.StageID,
		Seq:       time.Now().UnixNano(),
		Timestamp: time.Now(),
		Type:      rt.CommandTypeCancel,
	})
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
	}
	if err := o.registry.AppendCommand(rt.ProtocolCommand{
		SessionID: active.RunID,
		TaskID:    taskID,
		RunID:     active.RunID,
		StageID:   active.StageID,
		Seq:       time.Now().UnixNano(),
		Timestamp: time.Now(),
		Type:      rt.CommandTypeKill,
	}); err != nil {
		return err
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

// Pause requests a live adapter stage to pause.
func (o *Orchestrator) Pause(taskID string) error {
	t, err := o.taskStore.Load(taskID)
	if err != nil {
		return err
	}
	if !t.Runtime.AllowPause {
		return fmt.Errorf("pause is not allowed for task %s", taskID)
	}

	active, err := o.registry.LoadActiveRun(taskID)
	if err != nil {
		return err
	}
	if active == nil {
		return fmt.Errorf("task %s is not running", taskID)
	}
	if !active.Capabilities.SupportsPause || !active.Capabilities.SupportsResume {
		return fmt.Errorf("agent %s does not support pause/resume", active.Agent)
	}
	o.eventSink.Emit(taskID, active.RunID, "pause_requested", "")
	return o.registry.AppendCommand(rt.ProtocolCommand{
		SessionID: active.RunID,
		TaskID:    taskID,
		RunID:     active.RunID,
		StageID:   active.StageID,
		Seq:       time.Now().UnixNano(),
		Timestamp: time.Now(),
		Type:      rt.CommandTypePause,
	})
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
	if err := o.taskStore.Save(t); err != nil {
		return err
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
	return changed, nil
}
