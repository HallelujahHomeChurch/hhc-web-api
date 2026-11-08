package recordings

import (
	"context"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/google/uuid"
	"time"
)

const BroadcastOutputProtocolVersion = "k4-2026-10-15.rc1"

// Output identity is persisted by the sender before its first request. A retry
// must never resolve a different recording, even after the source is retargeted.
type BroadcastOutputOpenInput struct {
	ProtocolVersion string `json:"protocolVersion"`
	OutputSessionID string `json:"outputSessionId"`
	StreamKey       string `json:"streamKey"`
	MediaProfile    string `json:"mediaProfile,omitempty"`
	RecoveryVersion string `json:"recoveryVersion,omitempty"`
}

const BroadcastRecoveryVersion = "common-watermark-v1"

type BroadcastOutputOptions struct {
	MediaProfile    string
	RecoveryVersion string
}

func (in BroadcastOutputOpenInput) Validate() error {
	if in.RecoveryVersion != "" && in.RecoveryVersion != BroadcastRecoveryVersion {
		return ErrInvalid
	}
	if in.MediaProfile != "" && in.MediaProfile != "obs-hls-10s-v1" {
		return ErrBroadcastProtocol
	}
	if in.ProtocolVersion != BroadcastOutputProtocolVersion {
		return ErrBroadcastProtocol
	}
	id, err := uuid.Parse(in.OutputSessionID)
	if err != nil || id == uuid.Nil || id.String() != in.OutputSessionID {
		return ErrInvalid
	}
	if _, _, err := HashBroadcastSourceKey(in.StreamKey); err != nil {
		return ErrBroadcastStreamKey
	}
	return nil
}

// Binding completion is server-owned; clients do not race resolve and bind.
// Session remains an opaque K3 event-scoped credential, stored only in the OS vault.
type BroadcastOutputOpened struct {
	ProtocolVersion string `json:"protocolVersion"`
	OutputSessionID string `json:"outputSessionId"`
	SourceID        string `json:"sourceId"`
	MediaProfile    string `json:"mediaProfile,omitempty"`
	BroadcastIngestBinding
}

// Persisted by the repository before any request to Asset is made.
type BroadcastOutputReservation struct {
	SourceID  string
	Broadcast Broadcast
	Capture   Capture
}

var ErrBroadcastOutputCapacity = errors.New("broadcast output capacity exhausted")

var ErrBroadcastOutputTerminal = errors.New("broadcast output terminal")

type BroadcastOutputRepository interface {
	BroadcastOutputOwner(context.Context, string, string) (string, error)
	PrepareBroadcastOutput(context.Context, string, string, string, bool, time.Time, ...BroadcastOutputOptions) (BroadcastOutputReservation, error)
	EnsureDirectBroadcast(context.Context, string, string, BroadcastMutationInput, time.Time) (Broadcast, error)
}

func (s *Service) OpenBroadcastOutput(ctx context.Context, requestID string, in BroadcastOutputOpenInput) (BroadcastOutputOpened, error) {
	var out BroadcastOutputOpened
	if err := in.Validate(); err != nil {
		return out, err
	}
	r, ok := s.repo.(BroadcastOutputRepository)
	if !ok {
		return out, assetclient.ErrUnavailable
	}
	hash, _, _ := HashBroadcastSourceKey(in.StreamKey)
	owner, err := r.BroadcastOutputOwner(ctx, hash, in.OutputSessionID)
	if err != nil {
		return out, err
	}
	if !auditclient.ValidActorRequest(owner, requestID) {
		return out, ErrForbidden
	}
	if err = s.ingestAuthorizer(ctx, owner); err != nil {
		return out, err
	}
	if err = s.retainAuthenticatedStreamKey(ctx, hash, in.StreamKey); err != nil {
		return out, err
	}
	ctx = auditclient.WithProvenance(ctx, owner, requestID)
	reserved, err := r.PrepareBroadcastOutput(ctx, hash, in.OutputSessionID, owner, s.studioWriters && s.broadcastWriters, s.now().UTC(), BroadcastOutputOptions{MediaProfile: in.MediaProfile, RecoveryVersion: in.RecoveryVersion})
	if err != nil {
		return out, err
	}
	bind := BroadcastBindInput{ProtocolVersion: BroadcastProtocolVersion, BroadcastMutationInput: BroadcastMutationInput{OperationKey: in.OutputSessionID + ".bind"}}
	out.BroadcastMutationResult, err = s.completeBroadcastBinding(ctx, reserved.Broadcast.View.RecordingID, owner, bind, reserved.Capture)
	if err != nil {
		return out, err
	}
	if out.Broadcast.Binding == nil {
		return out, assetclient.ErrUnavailable
	}
	ir, err := s.ingestRepository()
	if err != nil {
		return out, err
	}
	current, secret, err := ir.BroadcastIngestCredentials(ctx, out.Broadcast.RecordingID)
	if err != nil {
		return out, err
	}
	if current != hash {
		return out, ErrBroadcastStreamKey
	}
	cr, err := s.captureRepository()
	if err != nil {
		return out, err
	}
	capture, err := cr.GetCapture(ctx, out.Broadcast.Binding.CaptureID, out.Broadcast.RecordingID)
	if err != nil {
		return out, err
	}
	out.Session, err = s.issueIngestSession(ingestSigningKey(current, secret), ingestClaims{RecordingID: out.Broadcast.RecordingID, CaptureID: out.Broadcast.Binding.CaptureID, ActorID: owner, Epoch: out.Broadcast.Binding.Epoch}, capture)
	out.Receipt.State = "completed"
	out.ProtocolVersion = BroadcastOutputProtocolVersion
	out.OutputSessionID = in.OutputSessionID
	out.SourceID = reserved.SourceID
	out.MediaProfile = in.MediaProfile
	return out, err
}
func (s *Service) WithStudioWriters(enabled bool) *Service { s.studioWriters = enabled; return s }
func (s *Service) EnsureDirectBroadcast(ctx context.Context, source, actor string, in BroadcastMutationInput) (BroadcastView, error) {
	if !s.studioWriters || !s.broadcastWriters {
		return BroadcastView{}, assetclient.ErrUnavailable
	}
	if !validLiveUUID(source) || !ValidOperationKey(in.OperationKey) || in.ExpectedRevision < 1 {
		return BroadcastView{}, ErrInvalid
	}
	if err := broadcastActor(ctx, actor); err != nil {
		return BroadcastView{}, err
	}
	if err := s.ingestAuthorizer(ctx, actor); err != nil {
		return BroadcastView{}, err
	}
	r, ok := s.repo.(BroadcastOutputRepository)
	if !ok {
		return BroadcastView{}, assetclient.ErrUnavailable
	}
	b, err := r.EnsureDirectBroadcast(ctx, source, actor, in, s.now().UTC())
	return b.View, err
}
