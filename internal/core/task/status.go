package task

import "fmt"

// TaskStatus represents the current state of a task in its lifecycle.
type TaskStatus string

const (
	StatusDraft                TaskStatus = "draft"
	StatusQueued               TaskStatus = "queued"
	StatusStageRunning         TaskStatus = "stage_running"
	StatusWaitingClarification TaskStatus = "waiting_clarification"
	StatusPaused               TaskStatus = "paused"
	StatusReviewing            TaskStatus = "reviewing"
	StatusHandoffPending       TaskStatus = "handoff_pending"
	StatusCompleted            TaskStatus = "completed"
	StatusFailed               TaskStatus = "failed"
	StatusRejected             TaskStatus = "rejected"
	StatusCanceled             TaskStatus = "canceled"

	// Compatibility aliases for older code paths and tests.
	StatusPreparingContext   TaskStatus = "preparing_context"
	StatusRunning            TaskStatus = StatusStageRunning
	StatusNeedsClarification TaskStatus = StatusWaitingClarification
	StatusReadyToResume      TaskStatus = "ready_to_resume"
	StatusPausing            TaskStatus = "pausing"
	StatusStopping           TaskStatus = "stopping"
	StatusStopped            TaskStatus = "stopped"
	StatusKilled             TaskStatus = "killed"
	StatusValidating         TaskStatus = "validating"
	StatusReview             TaskStatus = StatusReviewing
)

var validTransitions = map[TaskStatus][]TaskStatus{
	StatusDraft:                {StatusQueued, StatusCanceled},
	StatusQueued:               {StatusStageRunning, StatusWaitingClarification, StatusHandoffPending, StatusCanceled, StatusFailed},
	StatusStageRunning:         {StatusWaitingClarification, StatusPaused, StatusHandoffPending, StatusReviewing, StatusCompleted, StatusFailed, StatusCanceled},
	StatusWaitingClarification: {StatusReadyToResume, StatusQueued, StatusCanceled},
	StatusReadyToResume:        {StatusQueued, StatusCanceled},
	StatusPaused:               {StatusQueued, StatusCanceled},
	StatusHandoffPending:       {StatusQueued, StatusCanceled, StatusFailed},
	StatusReviewing:            {StatusCompleted, StatusRejected, StatusQueued},
	StatusCompleted:            {},
	StatusFailed:               {StatusQueued, StatusCanceled},
	StatusRejected:             {StatusQueued, StatusCanceled},
	StatusCanceled:             {},

	// Compatibility transitions.
	StatusPreparingContext: {StatusStageRunning, StatusFailed},
	StatusPausing:          {StatusPaused},
	StatusStopping:         {StatusStopped},
	StatusStopped:          {StatusQueued, StatusCanceled},
	StatusKilled:           {StatusQueued, StatusCanceled},
	StatusValidating:       {StatusQueued, StatusReviewing, StatusFailed},
}

// CanTransitionTo checks whether the transition from current status to target is allowed.
func (s TaskStatus) CanTransitionTo(target TaskStatus) bool {
	allowed, ok := validTransitions[s]
	if !ok {
		return false
	}
	for _, next := range allowed {
		if next == target {
			return true
		}
	}
	return false
}

// ValidateTransition returns an error if the transition is not allowed.
func (s TaskStatus) ValidateTransition(target TaskStatus) error {
	if s == target {
		if s.IsTerminal() {
			return fmt.Errorf("task is already %s", s)
		}
		return nil
	}
	if !s.CanTransitionTo(target) {
		return fmt.Errorf("invalid transition: %s → %s", s, target)
	}
	return nil
}

// IsTerminal returns true if no further transitions are possible.
func (s TaskStatus) IsTerminal() bool {
	return s == StatusCompleted || s == StatusCanceled
}

// IsActive returns true if the task currently has a live stage or control handoff in progress.
func (s TaskStatus) IsActive() bool {
	return s == StatusStageRunning || s == StatusQueued || s == StatusHandoffPending
}

// CanCancel returns true if the task can be canceled from its current status.
func (s TaskStatus) CanCancel() bool {
	return s.CanTransitionTo(StatusCanceled)
}

// CanResume returns true if the task can be resumed.
func (s TaskStatus) CanResume() bool {
	return s == StatusWaitingClarification || s == StatusReadyToResume || s == StatusPaused || s == StatusHandoffPending
}

// String returns the string representation of the status.
func (s TaskStatus) String() string {
	return string(s)
}
