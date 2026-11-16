package postgres

import (
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"testing"
)

func TestPublicationOutcomeWaitsForAutomaticLayoutRecovery(t *testing.T) {
	for _, status := range []string{"queued", "validating", "failed", "ready"} {
		t.Run(status, func(t *testing.T) {
			state := bulletinProductState{number: 1747, automatic: true, layoutBlocked: true, layoutStatus: status}
			state.classify(true)
			expected := "needs_attention"
			if status == "queued" || status == "validating" {
				expected = "preparing"
			}
			if state.outcome.Status != expected {
				t.Fatalf("status=%s outcome=%+v", status, state.outcome)
			}
			if expected == "needs_attention" && state.outcome.Reason != "review_required" {
				t.Fatalf("lost review reason: %+v", state.outcome)
			}
			if expected == "preparing" && state.outcome.NextAction != "" {
				t.Fatalf("active recovery asks administrator to intervene: %+v", state.outcome)
			}
		})
	}
}

func TestMetadataOutcomeWaitsForAutomaticLayoutRecovery(t *testing.T) {
	for _, status := range []string{"queued", "validating", "failed", "ready"} {
		t.Run(status, func(t *testing.T) {
			state := bulletinProductState{correctionBlocked: true, correctionStatus: status}
			state.outcome.PendingMetadata = &bulletins.PublicationMetadata{IssueNumber: 1747, IssueDate: "2026-11-15", Title: "Weekly"}
			state.classify(true)
			expected := "needs_attention"
			if status == "queued" || status == "validating" {
				expected = "preparing"
			}
			if state.outcome.Status != expected {
				t.Fatalf("status=%s outcome=%+v", status, state.outcome)
			}
			if expected == "needs_attention" && state.outcome.Reason != "review_required" {
				t.Fatalf("lost review reason: %+v", state.outcome)
			}
			if expected == "preparing" && state.outcome.NextAction != "" {
				t.Fatalf("active correction asks administrator to intervene: %+v", state.outcome)
			}
		})
	}
}

func TestPublicationOutcomeOffersRetryAfterUnblockedTerminalFailure(t *testing.T) {
	for _, metadata := range []bool{false, true} {
		state := bulletinProductState{number: 1747, automatic: true, layoutStatus: "failed"}
		stage := "layout"
		if metadata {
			state.outcome.PendingMetadata = &bulletins.PublicationMetadata{Title: "Weekly"}
			state.correctionStatus = "failed"
			stage = "metadata"
		}
		state.classify(true)
		if state.outcome.Status != "needs_attention" || state.outcome.Reason != "processing_failed" || state.outcome.NextAction != "resume" || state.stage != stage {
			t.Fatalf("state=%+v", state)
		}
	}
}

func TestPublicationOutcomeKeepsCurrentChannelsAvailableDuringLayoutRecovery(t *testing.T) {
	state := bulletinProductState{number: 1747, automatic: true, layoutBlocked: true, layoutStatus: "validating"}
	state.outcome.Current = bulletins.PublicationChannels{PDF: true, Traditional: true, Simplified: true}
	state.outcome.Availability = state.outcome.Current
	state.classify(true)
	if state.outcome.Status != "published" || state.outcome.NextAction != "" || !state.outcome.Availability.Traditional {
		t.Fatalf("outcome=%+v", state.outcome)
	}
}
