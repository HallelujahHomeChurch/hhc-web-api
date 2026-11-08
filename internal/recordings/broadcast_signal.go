package recordings

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/diagnostics"
	"log/slog"
	"time"
)

func (b Broadcast) awaitsDirectSignal() bool {
	return b.SourceManaged && b.View.ScheduledAt == nil && b.View.Policy.AutoStart && b.View.Policy.AutoEnd && b.StartedAt != nil && !b.Revoked && b.View.Phase == "live" && b.View.Boundaries.EndSequenceExclusive == nil
}
func (b *Broadcast) applyDirectSignal(signal assetclient.BroadcastSignal) (bool, error) {
	end := signal.EndSequenceExclusive
	if end == nil {
		return false, nil
	}
	if b.View.Boundaries.StartSequence == nil || *end <= *b.View.Boundaries.StartSequence {
		return false, ErrConflict
	}
	if previous := b.View.Boundaries.EndSequenceExclusive; previous != nil {
		if *previous != *end {
			return false, ErrConflict
		}
		return false, nil
	}
	b.View.Boundaries.EndSequenceExclusive = end
	reason := "unknown"
	if signal.RecoveryVersion == BroadcastRecoveryVersion && signal.Reason == "idle_timeout" {
		reason = "idle_timeout"
	}
	b.recordPublicEnd(reason, end)
	b.View.Boundaries.Revision++
	b.RangePending = true
	b.View.Phase = "end_pending"
	return true, nil
}

func (b Broadcast) canRecoverSignalHealth(c *Capture, now time.Time) bool {
	if !b.awaitsDirectSignal() || b.View.Health.State != "interrupted" || b.View.Health.Reason == nil || *b.View.Health.Reason != "signal_grace" || c == nil || c.Terminal() || c.Status.StopAcceptedAt != nil || !now.Before(c.Status.ExpiresAt) {
		return false
	}
	binding := b.View.Binding
	return binding != nil && c.IntentID == b.CaptureIntentID && c.Status.CaptureID == binding.CaptureID && c.ActorID == binding.ActorID && c.BroadcastEpoch == binding.Epoch && b.Epoch == binding.Epoch
}

func (s *Service) recoverBroadcastSignal(ctx context.Context, repo BroadcastRepository, id string, snapshot Broadcast) (Broadcast, error) {
	if s.packages == nil || !snapshot.awaitsDirectSignal() || snapshot.View.Health.State != "interrupted" || snapshot.View.Health.Reason == nil || *snapshot.View.Health.Reason != "signal_grace" {
		return snapshot, nil
	}
	// Producer control polls recover display health without running projection, cover or cleanup work.
	refreshCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	captures, err := s.captureRepository()
	var observed Capture
	if err == nil {
		observed, err = captures.GetCapture(refreshCtx, snapshot.CaptureIntentID, id)
	}
	if err == nil && !snapshot.canRecoverSignalHealth(&observed, s.now()) {
		return repo.GetBroadcast(ctx, id)
	}
	var signal assetclient.BroadcastSignal
	if err == nil {
		owner := auditclient.WithPrincipalProvenance(refreshCtx, observed.ActorType, observed.ActorID, observed.RequestID)
		stageCtx, finish := diagnostics.Start(owner, diagnostics.SignalReconcile)
		signal, err = s.packages.ReconcileRecordingBroadcastSignal(stageCtx, observed.Status.CaptureID, false)
		finish(err)
	}
	if err == nil && (signal.State != "healthy" || signal.EndSequenceExclusive != nil) {
		return repo.GetBroadcast(ctx, id)
	}
	var updated Broadcast
	if err == nil {
		updated, err = repo.UpdateBroadcast(refreshCtx, id, "", s.now().UTC(), func(commitCtx context.Context, b *Broadcast, _ *Recording, c *Capture) error {
			if !b.canRecoverSignalHealth(c, s.now()) {
				return nil
			}
			// RecordingVersion is a read projection, not capture authority.
			current := *c
			current.Status.RecordingVersion = observed.Status.RecordingVersion
			if captureDigest(*b) != captureDigest(snapshot) || captureDigest(current) != captureDigest(observed) {
				diagnostics.Discard(commitCtx, diagnostics.SignalReconcile)
				return nil
			}
			b.View.Health.State = "healthy"
			b.View.Health.Reason = nil
			b.View.Health.LastConfirmedAt = signal.LastMediaConfirmedAt
			b.View.Revision++
			return nil
		})
	}
	if err != nil {
		// A health dependency outage must not block the producer's Stop/control polling.
		slog.Warn("recording signal health refresh failed", "error", err)
		return repo.GetBroadcast(ctx, id)
	}
	return updated, nil
}
