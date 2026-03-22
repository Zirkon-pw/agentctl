package task

import (
	. "github.com/docup/agentctl/internal/core/task"
	"testing"
)

func TestCanTransitionTo_ValidTransitions(t *testing.T) {
	cases := []struct {
		from, to TaskStatus
	}{
		{StatusDraft, StatusQueued},
		{StatusDraft, StatusCanceled},
		{StatusQueued, StatusStageRunning},
		{StatusQueued, StatusWaitingClarification},
		{StatusQueued, StatusHandoffPending},
		{StatusQueued, StatusCanceled},
		{StatusStageRunning, StatusWaitingClarification},
		{StatusStageRunning, StatusPaused},
		{StatusStageRunning, StatusHandoffPending},
		{StatusStageRunning, StatusReviewing},
		{StatusStageRunning, StatusFailed},
		{StatusStageRunning, StatusCanceled},
		{StatusWaitingClarification, StatusReadyToResume},
		{StatusWaitingClarification, StatusQueued},
		{StatusWaitingClarification, StatusCanceled},
		{StatusReadyToResume, StatusQueued},
		{StatusReadyToResume, StatusCanceled},
		{StatusPaused, StatusQueued},
		{StatusPaused, StatusCanceled},
		{StatusHandoffPending, StatusQueued},
		{StatusHandoffPending, StatusFailed},
		{StatusReviewing, StatusCompleted},
		{StatusReviewing, StatusRejected},
		{StatusReviewing, StatusQueued},
		{StatusFailed, StatusQueued},
		{StatusRejected, StatusQueued},
	}
	for _, c := range cases {
		if !c.from.CanTransitionTo(c.to) {
			t.Errorf("expected %s → %s to be valid", c.from, c.to)
		}
	}
}

func TestCanTransitionTo_InvalidTransitions(t *testing.T) {
	cases := []struct {
		from, to TaskStatus
	}{
		{StatusDraft, StatusRunning},
		{StatusDraft, StatusCompleted},
		{StatusCompleted, StatusQueued},
		{StatusCompleted, StatusRunning},
		{StatusCanceled, StatusQueued},
		{StatusCanceled, StatusDraft},
		{StatusStageRunning, StatusDraft},
		{StatusStageRunning, StatusQueued},
		{StatusReviewing, StatusStageRunning},
		{StatusPaused, StatusStageRunning},
	}
	for _, c := range cases {
		if c.from.CanTransitionTo(c.to) {
			t.Errorf("expected %s → %s to be invalid", c.from, c.to)
		}
	}
}

func TestValidateTransition_ReturnsError(t *testing.T) {
	err := StatusCompleted.ValidateTransition(StatusRunning)
	if err == nil {
		t.Fatal("expected error for invalid transition")
	}
}

func TestValidateTransition_NoError(t *testing.T) {
	err := StatusDraft.ValidateTransition(StatusQueued)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIsTerminal(t *testing.T) {
	terminals := []TaskStatus{StatusCompleted, StatusCanceled}
	for _, s := range terminals {
		if !s.IsTerminal() {
			t.Errorf("expected %s to be terminal", s)
		}
	}

	nonTerminals := []TaskStatus{StatusDraft, StatusRunning, StatusFailed, StatusRejected, StatusReview}
	for _, s := range nonTerminals {
		if s.IsTerminal() {
			t.Errorf("expected %s to NOT be terminal", s)
		}
	}
}

func TestIsActive(t *testing.T) {
	actives := []TaskStatus{StatusQueued, StatusStageRunning, StatusHandoffPending}
	for _, s := range actives {
		if !s.IsActive() {
			t.Errorf("expected %s to be active", s)
		}
	}

	inactives := []TaskStatus{StatusDraft, StatusPaused, StatusCompleted, StatusWaitingClarification}
	for _, s := range inactives {
		if s.IsActive() {
			t.Errorf("expected %s to NOT be active", s)
		}
	}
}

func TestCanCancel(t *testing.T) {
	cancelable := []TaskStatus{StatusDraft, StatusQueued, StatusWaitingClarification, StatusReadyToResume, StatusPaused, StatusHandoffPending, StatusFailed}
	for _, s := range cancelable {
		if !s.CanCancel() {
			t.Errorf("expected %s to be cancelable", s)
		}
	}

	notCancelable := []TaskStatus{StatusCompleted, StatusCanceled}
	for _, s := range notCancelable {
		if s.CanCancel() {
			t.Errorf("expected %s to NOT be cancelable", s)
		}
	}
}

func TestCanResume(t *testing.T) {
	resumable := []TaskStatus{StatusReadyToResume, StatusPaused, StatusWaitingClarification, StatusHandoffPending}
	for _, s := range resumable {
		if !s.CanResume() {
			t.Errorf("expected %s to be resumable", s)
		}
	}

	notResumable := []TaskStatus{StatusDraft, StatusStageRunning, StatusCompleted}
	for _, s := range notResumable {
		if s.CanResume() {
			t.Errorf("expected %s to NOT be resumable", s)
		}
	}
}

func TestString(t *testing.T) {
	if StatusDraft.String() != "draft" {
		t.Errorf("expected 'draft', got %q", StatusDraft.String())
	}
}

func TestUnknownStatus_CanTransitionTo(t *testing.T) {
	unknown := TaskStatus("unknown_status")
	if unknown.CanTransitionTo(StatusQueued) {
		t.Error("unknown status should not transition to anything")
	}
}
