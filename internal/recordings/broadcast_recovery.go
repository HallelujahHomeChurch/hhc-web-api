package recordings

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/diagnostics"
	"time"
)

const BroadcastControlD2Version = "d2-2026-10-23.rc1"

type BroadcastRecovery struct {
	Version                             *string    `json:"version"`
	State                               string     `json:"state"`
	Deadline                            *time.Time `json:"deadline"`
	LastCommonConfirmedAt               *time.Time `json:"lastCommonConfirmedAt"`
	CommonConfirmedEndSequenceExclusive *int       `json:"commonConfirmedEndSequenceExclusive"`
}
type BroadcastPublicEnd struct {
	Reason               string `json:"reason"`
	EndSequenceExclusive *int   `json:"endSequenceExclusive"`
}
type BroadcastControlD2 struct {
	BroadcastControlD1
	Recovery  BroadcastRecovery   `json:"recovery"`
	PublicEnd *BroadcastPublicEnd `json:"publicEnd"`
}

func (b *Broadcast) recordPublicEnd(reason string, end *int) {
	if b.PublicEnd != nil {
		return
	}
	b.PublicEnd = &BroadcastPublicEnd{Reason: reason, EndSequenceExclusive: end}
}
func (b *Broadcast) applyRecoverySignal(signal assetclient.BroadcastSignal, requested string) {
	if requested != BroadcastRecoveryVersion || signal.RecoveryVersion != BroadcastRecoveryVersion {
		b.Recovery.Version = nil
		b.Recovery.State = "unsupported"
		return
	}
	switch signal.State {
	case "waiting", "healthy", "interrupted", "ended":
	default:
		b.Recovery.Version = nil
		b.Recovery.State = "unsupported"
		return
	}
	old := b.Recovery
	if (old.State == "ended" || b.PublicEnd != nil) && signal.State != "ended" {
		return
	}
	if old.CommonConfirmedEndSequenceExclusive != nil && (signal.CommonConfirmedEndSequenceExclusive == nil || *signal.CommonConfirmedEndSequenceExclusive < *old.CommonConfirmedEndSequenceExclusive) {
		return
	}
	if old.Deadline != nil && (signal.Deadline == nil || signal.Deadline.Before(*old.Deadline)) {
		return
	}
	if old.LastCommonConfirmedAt != nil && signal.LastCommonConfirmedAt != nil && signal.LastCommonConfirmedAt.Before(*old.LastCommonConfirmedAt) {
		return
	}
	version := BroadcastRecoveryVersion
	b.Recovery = BroadcastRecovery{Version: &version, State: signal.State, Deadline: signal.Deadline, LastCommonConfirmedAt: signal.LastCommonConfirmedAt, CommonConfirmedEndSequenceExclusive: signal.CommonConfirmedEndSequenceExclusive}
}

// D2 reads one persisted binding snapshot; a control poll never upgrades a capture.
func (s *Service) GetBroadcastControlD2(ctx context.Context, id, actor, capture string, epoch int64) (BroadcastControlD2, error) {
	var out BroadcastControlD2
	repo, e := s.broadcastRepository()
	if e != nil {
		return out, e
	}
	b, e := repo.GetBroadcast(ctx, id)
	if e != nil {
		return out, e
	}
	if b.View.Binding == nil || b.View.Binding.ActorID != actor {
		return out, ErrNotFound
	}
	if b.View.Binding.CaptureID != capture || b.Epoch != epoch || b.View.Binding.Epoch != epoch {
		return out, ErrBroadcastEpoch
	}
	if s.packages != nil && b.awaitsDirectSignal() && b.View.Health.State == "interrupted" && b.View.Health.Reason != nil && *b.View.Health.Reason == "signal_grace" {
		refresh, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		captures, err := s.captureRepository()
		var observed Capture
		if err == nil {
			observed, err = captures.GetCapture(refresh, b.CaptureIntentID, id)
		}
		var signal assetclient.BroadcastSignal
		if err == nil && observed.IntentID == b.CaptureIntentID && observed.Status.CaptureID == capture && observed.ActorID == actor && observed.BroadcastEpoch == epoch && !observed.Terminal() && observed.Status.StopAcceptedAt == nil && s.now().Before(observed.Status.ExpiresAt) && observed.RecoveryVersion == BroadcastRecoveryVersion {
			owner := auditclient.WithPrincipalProvenance(refresh, observed.ActorType, observed.ActorID, observed.RequestID)
			stageCtx, finish := diagnostics.Start(owner, diagnostics.SignalReconcile)
			signal, err = s.packages.ReconcileRecordingBroadcastSignal(stageCtx, capture, false)
			finish(err)
		}
		var updated Broadcast
		if err == nil {
			updated, err = repo.UpdateBroadcast(refresh, id, "", s.now().UTC(), func(commitCtx context.Context, current *Broadcast, _ *Recording, c *Capture) error {
				binding := current.View.Binding
				if binding == nil || binding.ActorID != actor || binding.CaptureID != capture || binding.Epoch != epoch || current.Epoch != epoch {
					return ErrBroadcastEpoch
				}
				if !current.awaitsDirectSignal() || c == nil || c.Terminal() || c.Status.StopAcceptedAt != nil || !s.now().Before(c.Status.ExpiresAt) || c.IntentID != current.CaptureIntentID || c.Status.CaptureID != capture || c.ActorID != actor || c.BroadcastEpoch != epoch {
					return nil
				}
				if c.RecoveryVersion != BroadcastRecoveryVersion {
					current.Recovery = BroadcastRecovery{State: "unsupported"}
					return nil
				}
				// RecordingVersion is a read projection, not capture authority.
				snapshot := *c
				snapshot.Status.RecordingVersion = observed.Status.RecordingVersion
				if captureDigest(current) != captureDigest(b) || captureDigest(snapshot) != captureDigest(observed) {
					diagnostics.Discard(commitCtx, diagnostics.SignalReconcile)
					return nil
				}
				current.applyRecoverySignal(signal, c.RecoveryVersion)
				if current.Recovery.State == "healthy" && signal.EndSequenceExclusive == nil {
					current.View.Health.State = "healthy"
					current.View.Health.Reason = nil
					current.View.Health.LastConfirmedAt = signal.LastMediaConfirmedAt
					current.View.Revision++
				}
				return nil
			})
		}
		if err != nil {
			// Dependency refresh failure must not take producer control down.
			// Re-read authority so a concurrent rebind/revoke is not hidden.
			b, e = repo.GetBroadcast(ctx, id)
			if e != nil {
				return out, e
			}
			if b.View.Binding == nil || b.View.Binding.ActorID != actor {
				return out, ErrNotFound
			}
			if b.View.Binding.CaptureID != capture || b.View.Binding.Epoch != epoch || b.Epoch != epoch {
				return out, ErrBroadcastEpoch
			}
		} else {
			b = updated
		}
	}
	v := b.View
	recovery := b.Recovery
	if recovery.Version == nil {
		recovery = BroadcastRecovery{State: "unsupported"}
	}
	end := b.PublicEnd
	if end == nil && (b.Revoked || v.Boundaries.EndSequenceExclusive != nil) {
		end = &BroadcastPublicEnd{Reason: "unknown", EndSequenceExclusive: v.Boundaries.EndSequenceExclusive}
	}
	out = BroadcastControlD2{BroadcastControlD1: BroadcastControlD1{BroadcastControl: BroadcastControl{Revision: v.Revision, Phase: v.Phase, PendingCommand: v.PendingCommand}, ControlVersion: BroadcastControlD2Version, RecordingID: v.RecordingID, CaptureID: capture, Epoch: epoch, Boundaries: v.Boundaries}, Recovery: recovery, PublicEnd: end}
	return out, nil
}
