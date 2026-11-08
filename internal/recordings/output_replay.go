package recordings

import (
	"context"
	"crypto/subtle"
	"errors"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
)

const BroadcastOutputReplayProtocolVersion = "k5-2026-10-20.rc1"

var ErrBroadcastOutputReplayUnknown = errors.New("original broadcast output cannot be proven")

type BroadcastOutputReplayInput struct {
	ProtocolVersion string            `json:"protocolVersion"`
	OutputSessionID string            `json:"outputSessionId"`
	SourceID        string            `json:"sourceId"`
	RecordingID     string            `json:"recordingId"`
	CaptureID       string            `json:"captureId"`
	Epoch           int64             `json:"epoch"`
	MediaProfile    *string           `json:"mediaProfile,omitempty"`
	Receipt         *BroadcastReceipt `json:"receipt"`
	StreamKey       string            `json:"streamKey"`
}

func (in BroadcastOutputReplayInput) Validate() error {
	if in.ProtocolVersion != BroadcastOutputReplayProtocolVersion || in.MediaProfile != nil && *in.MediaProfile != "obs-hls-10s-v1" {
		return ErrBroadcastProtocol
	}
	if !validLiveUUID(in.OutputSessionID) || !validLiveUUID(in.SourceID) || !validLiveUUID(in.RecordingID) || !packageIDPattern.MatchString(in.CaptureID) || in.Epoch < 1 || in.Epoch > maxBroadcastInteger || in.Receipt == nil || !ValidOperationKey(in.Receipt.OperationKey) || in.Receipt.Operation != "bind" || in.Receipt.State != "completed" || in.Receipt.CommandID != "" && !validLiveUUID(in.Receipt.CommandID) {
		return ErrInvalid
	}
	_, _, err := HashBroadcastSourceKey(in.StreamKey)
	return err
}

type BroadcastOutputReplayResult struct {
	ProtocolVersion string                  `json:"protocolVersion"`
	OutputSessionID string                  `json:"outputSessionId"`
	SourceID        string                  `json:"sourceId"`
	RecordingID     string                  `json:"recordingId"`
	CaptureID       string                  `json:"captureId"`
	Epoch           int64                   `json:"epoch"`
	MediaProfile    *string                 `json:"mediaProfile,omitempty"`
	Receipt         BroadcastReceipt        `json:"receipt"`
	State           string                  `json:"state"`
	Session         *BroadcastIngestSession `json:"session,omitempty"`
}

type BroadcastOutputReplaySnapshot struct {
	Owner   string
	Capture Capture
	Receipt BroadcastReceipt
	State   string
}

type BroadcastOutputReplayRepository interface {
	ReadBroadcastOutputReplay(context.Context, string, BroadcastOutputReplayInput) (BroadcastOutputReplaySnapshot, error)
}

// Replay never reserves or completes an intent. Unknown identity stays unknown;
// only the original durable input-release proof can retire the original output.
func (s *Service) ReplayBroadcastOutput(ctx context.Context, requestID string, in BroadcastOutputReplayInput) (BroadcastOutputReplayResult, error) {
	var out BroadcastOutputReplayResult
	if err := in.Validate(); err != nil {
		return out, err
	}
	repo, ok := s.repo.(BroadcastOutputReplayRepository)
	if !ok {
		return out, assetclient.ErrUnavailable
	}
	hash, _, _ := HashBroadcastSourceKey(in.StreamKey)
	first, err := repo.ReadBroadcastOutputReplay(ctx, hash, in)
	if err != nil {
		return out, err
	}
	if !auditclient.ValidActorRequest(first.Owner, requestID) {
		return out, ErrForbidden
	}
	if err = s.ingestAuthorizer(ctx, first.Owner); err != nil {
		return out, err
	}
	// Authorization is a remote read, outside the bounded database transaction.
	// Recheck original authority afterwards instead of holding locks across it.
	current, err := repo.ReadBroadcastOutputReplay(ctx, hash, in)
	if err != nil {
		return out, err
	}
	if current.Owner != first.Owner || current.State != "active" && current.State != "terminal" {
		return out, ErrBroadcastOutputReplayUnknown
	}
	out = BroadcastOutputReplayResult{ProtocolVersion: BroadcastOutputReplayProtocolVersion, OutputSessionID: in.OutputSessionID, SourceID: in.SourceID, RecordingID: in.RecordingID, CaptureID: in.CaptureID, Epoch: in.Epoch, MediaProfile: in.MediaProfile, Receipt: current.Receipt, State: current.State}
	if current.State == "terminal" {
		return out, nil
	}
	credentials, err := s.ingestRepository()
	if err != nil {
		return BroadcastOutputReplayResult{}, err
	}
	latest, secret, err := credentials.BroadcastIngestCredentials(ctx, in.RecordingID)
	if err != nil {
		return BroadcastOutputReplayResult{}, err
	}
	if subtle.ConstantTimeCompare([]byte(latest), []byte(hash)) != 1 {
		return BroadcastOutputReplayResult{}, ErrBroadcastStreamKey
	}
	if secret == "" {
		return BroadcastOutputReplayResult{}, assetclient.ErrUnavailable
	}
	session, err := s.issueIngestSession(ingestSigningKey(latest, secret), ingestClaims{RecordingID: in.RecordingID, CaptureID: in.CaptureID, ActorID: current.Owner, Epoch: in.Epoch}, current.Capture)
	if err != nil {
		return BroadcastOutputReplayResult{}, err
	}
	out.Session = &session
	return out, nil
}
