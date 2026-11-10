package recordings

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/diagnostics"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"io"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrBroadcastProtocol    = errors.New("unsupported broadcast protocol")
	ErrBroadcastActiveLimit = errors.New("broadcast active capture limit")
	ErrBroadcastEpoch       = fmt.Errorf("%w: broadcast epoch mismatch", ErrConflict)
	ErrBroadcastOperation   = fmt.Errorf("%w: broadcast operation conflict", ErrConflict)
	ErrBroadcastBoundary    = fmt.Errorf("%w: broadcast boundary conflict", ErrConflict)
	ErrBroadcastBound       = fmt.Errorf("%w: broadcast already bound", ErrConflict)
)

const BroadcastProtocolVersion = "b1-2026-10-10.rc1"
const BroadcastControlVersion = "d1-2026-10-17.rc2"
const maxBroadcastInteger int64 = 9007199254740991

type BroadcastControl struct {
	Revision       int64             `json:"revision"`
	Phase          string            `json:"phase"`
	PendingCommand *BroadcastCommand `json:"pendingCommand"`
}

type BroadcastControlD1 struct {
	BroadcastControl
	ControlVersion string              `json:"controlVersion"`
	RecordingID    string              `json:"recordingId"`
	CaptureID      string              `json:"captureId"`
	Epoch          int64               `json:"epoch"`
	Boundaries     BroadcastBoundaries `json:"boundaries"`
}

type BroadcastPolicy struct {
	AutoStart   bool `json:"autoStart"`
	AutoEnd     bool `json:"autoEnd"`
	AutoPublish bool `json:"autoPublish"`
}
type BroadcastBinding struct {
	CaptureID string `json:"captureId"`
	Epoch     int64  `json:"epoch"`
	ActorID   string `json:"actorId"`
}
type BroadcastBoundaries struct {
	StartSequence        *int  `json:"startSequence"`
	EndSequenceExclusive *int  `json:"endSequenceExclusive"`
	Revision             int64 `json:"revision"`
}
type BroadcastCommand struct {
	CommandID        string    `json:"commandId"`
	Kind             string    `json:"kind"`
	CaptureID        string    `json:"captureId"`
	Epoch            int64     `json:"epoch"`
	RequestedAt      time.Time `json:"requestedAt"`
	Status           string    `json:"status"`
	BoundarySequence *int      `json:"boundarySequence"`
}
type BroadcastHealth struct {
	State              string     `json:"state"`
	LastConfirmedAt    *time.Time `json:"lastConfirmedAt"`
	LastPublishedAt    *time.Time `json:"lastPublishedAt"`
	VerifiedEndSeconds *float64   `json:"verifiedEndSeconds"`
	QueuedObjects      *int       `json:"queuedObjects"`
	Reason             *string    `json:"reason"`
}
type BroadcastArchive struct {
	State  string  `json:"state"`
	Reason *string `json:"reason"`
}
type BroadcastView struct {
	RecordingID    string              `json:"recordingId"`
	Revision       int64               `json:"revision"`
	Phase          string              `json:"phase"`
	Announced      bool                `json:"announced"`
	ScheduledAt    *time.Time          `json:"scheduledAt"`
	Title          string              `json:"title"`
	Description    string              `json:"description"`
	Policy         BroadcastPolicy     `json:"policy"`
	Cover          LiveCoverSettings   `json:"cover"`
	Binding        *BroadcastBinding   `json:"binding"`
	Boundaries     BroadcastBoundaries `json:"boundaries"`
	Health         BroadcastHealth     `json:"health"`
	PendingCommand *BroadcastCommand   `json:"pendingCommand"`
	Archive        BroadcastArchive    `json:"archive"`
}
type BroadcastMutationInput struct {
	OperationKey     string `json:"operationKey"`
	ExpectedRevision int64  `json:"expectedRevision"`
}
type BroadcastCreateInput struct {
	OperationKey string              `json:"operationKey"`
	Title        string              `json:"title"`
	Description  string              `json:"description"`
	ScheduledAt  *time.Time          `json:"scheduledAt"`
	Policy       BroadcastPolicy     `json:"policy"`
	Cover        *LiveCoverSelection `json:"cover,omitempty"`
}
type BroadcastUpdateInput struct {
	BroadcastMutationInput
	Title       *string             `json:"title,omitempty"`
	Description *string             `json:"description,omitempty"`
	ScheduledAt *time.Time          `json:"scheduledAt,omitempty"`
	ScheduleSet bool                `json:"-"`
	Policy      *BroadcastPolicy    `json:"policy,omitempty"`
	Cover       *LiveCoverSelection `json:"cover,omitempty"`
}
type BroadcastBindInput struct {
	BroadcastMutationInput
	ProtocolVersion string `json:"protocolVersion"`
}
type BroadcastMarkerAckInput struct {
	OperationKey     string `json:"operationKey"`
	CaptureID        string `json:"captureId"`
	Epoch            int64  `json:"epoch"`
	BoundarySequence int    `json:"boundarySequence"`
}
type BroadcastEmergencyCloseInput struct {
	BroadcastMutationInput
	Reason string `json:"reason"`
}
type BroadcastReceipt struct {
	OperationKey string `json:"operationKey"`
	Operation    string `json:"operation"`
	State        string `json:"state"`
	CommandID    string `json:"commandId,omitempty"`
}
type BroadcastMutationResult struct {
	Broadcast BroadcastView    `json:"broadcast"`
	Receipt   BroadcastReceipt `json:"receipt"`
}
type BroadcastOperation struct {
	ActorID, RequestID, Digest string
	Receipt                    BroadcastReceipt
	Intent                     json.RawMessage
}
type Broadcast struct {
	Recovery                 BroadcastRecovery
	PublicEnd                *BroadcastPublicEnd
	LiveNotificationPending  bool
	StartedAt                *time.Time
	EndedAt                  *time.Time
	PublicEndPending         bool
	SourceManaged            bool `json:"-"`
	StreamKeyHash            string
	StreamKeySuffix          string
	AutoPublicationActivated bool
	PublicationWithdrawn     bool
	View                     BroadcastView
	Epoch                    int64
	CaptureIntentID          string
	CreatedAt                time.Time
	CancelledAt              *time.Time
	Revoked                  bool
	RangePending             bool
	RangeApplied             int64
	MemberState              string
	CoverSnapshot            CaptureLiveCover
	Commands                 map[string]BroadcastCommand
	Operations               map[string]BroadcastOperation
	Previews                 map[string]BroadcastPreviewAudit
	Projection               *assetclient.BroadcastProjection
}
type BroadcastPreviewAudit struct {
	ActorID   string
	RequestID string
	CaptureID string
	Epoch     int64
	Scope     string
	Purpose   string
	IssuedAt  time.Time
	ExpiresAt time.Time
}
type BroadcastRepository interface {
	CreateBroadcast(context.Context, BroadcastCreateInput, string, time.Time) (Broadcast, error)
	GetBroadcast(context.Context, string) (Broadcast, error)
	ListBroadcasts(context.Context, string, int) ([]Broadcast, error)
	ListBroadcastReconciliation(context.Context, time.Time, int) ([]Broadcast, error)
	UpdateBroadcast(context.Context, string, string, time.Time, func(context.Context, *Broadcast, *Recording, *Capture) error) (Broadcast, error)
	PrepareBroadcastBinding(context.Context, string, string, BroadcastBindInput, time.Time) (Broadcast, Capture, error)
	ReconcileBroadcastCleanup(context.Context, time.Time) error
}

// Keep the first public transition independent of the bounded signal history.
// Preserve live termination intent before changing phase; archived withdrawal is not a live end.
func (b *Broadcast) RevokePublic() {
	if b.StartedAt != nil && b.EndedAt == nil && (b.View.Phase == "live" || b.View.Phase == "end_pending") {
		b.PublicEndPending = true
	}
	b.recordPublicEnd("revoked", b.View.Boundaries.EndSequenceExclusive)
	b.Revoked = true
}

// Record the first confirmed end observation, never a requested boundary or a historical guess.
func (b *Broadcast) observePublicEnd(now time.Time) {
	if b.StartedAt == nil || b.EndedAt != nil || b.Projection == nil {
		return
	}
	active := b.View.Phase == "live" || b.View.Phase == "end_pending"
	confirmed := b.Projection.State == "failed" || b.Projection.State == "ready" && b.View.Boundaries.EndSequenceExclusive != nil
	if confirmed && (active || b.PublicEndPending) {
		b.EndedAt = &now
		b.PublicEndPending = false
	}
}

func (b *Broadcast) markLive(now time.Time) {
	b.View.Phase = "live"
	if b.StartedAt == nil {
		b.StartedAt = &now
	}
}
func (b Broadcast) CanChangePolicy() bool {
	return b.Epoch == 0 && b.CaptureIntentID == "" && b.View.Binding == nil
}
func (b *Broadcast) RequestCommand(kind string, now time.Time) (BroadcastCommand, error) {
	if b.View.Binding == nil || b.Revoked {
		return BroadcastCommand{}, ErrConflict
	}
	if b.View.PendingCommand != nil && b.View.PendingCommand.Kind == kind && b.View.PendingCommand.Status == "pending" {
		return *b.View.PendingCommand, nil
	}
	if kind == "start" && (!b.View.Announced || b.View.Phase != "preview") || kind == "end" && b.View.Phase != "live" || kind != "start" && kind != "end" {
		return BroadcastCommand{}, ErrConflict
	}
	if len(b.Commands) >= 64 {
		return BroadcastCommand{}, ErrConflict
	}
	c := BroadcastCommand{CommandID: platform.NewID(), Kind: kind, CaptureID: b.View.Binding.CaptureID, Epoch: b.View.Binding.Epoch, RequestedAt: now, Status: "pending"}
	b.Commands[c.CommandID] = c
	b.View.PendingCommand = &c
	b.View.Phase = kind + "_pending"
	return c, nil
}
func (b *Broadcast) Acknowledge(command, actor string, input BroadcastMarkerAckInput, stopped bool, now time.Time) error {
	if b.View.Binding == nil || b.View.Binding.ActorID != actor {
		return ErrNotFound
	}
	if input.CaptureID != b.View.Binding.CaptureID || input.Epoch != b.View.Binding.Epoch {
		return ErrBroadcastEpoch
	}
	if input.BoundarySequence < 0 || input.BoundarySequence > 2147483647 {
		return ErrInvalid
	}
	c, ok := b.Commands[command]
	if !ok || c.CaptureID != input.CaptureID || c.Epoch != input.Epoch {
		return ErrBroadcastEpoch
	}
	if c.Status == "acknowledged" {
		if c.BoundarySequence != nil && *c.BoundarySequence == input.BoundarySequence {
			return nil
		}
		return ErrBroadcastBoundary
	}
	if c.Status != "pending" || b.Revoked || c.Kind == "start" && stopped {
		return ErrConflict
	}
	if c.Kind == "end" && (b.View.Boundaries.StartSequence == nil || input.BoundarySequence <= *b.View.Boundaries.StartSequence) {
		return ErrBroadcastBoundary
	}
	c.Status = "acknowledged"
	boundary := input.BoundarySequence
	c.BoundarySequence = &boundary
	b.Commands[command] = c
	b.View.PendingCommand = nil
	if c.Kind == "start" {
		b.View.Boundaries.StartSequence = &boundary
	} else {
		b.View.Boundaries.EndSequenceExclusive = &boundary
		b.recordPublicEnd("manual_end", &boundary)
	}
	b.View.Boundaries.Revision++
	b.RangePending = true
	return nil
}
func (s *Service) WithBroadcastWriters(enabled bool) *Service { s.broadcastWriters = enabled; return s }
func (s *Service) broadcastRepository() (BroadcastRepository, error) {
	r, ok := s.repo.(BroadcastRepository)
	if !ok {
		return nil, assetclient.ErrUnavailable
	}
	return r, nil
}
func broadcastActor(ctx context.Context, actor string) error {
	p, ok := auditclient.ProvenanceFromContext(ctx)
	if !ok || p.ActorType != "user" || p.ActorID != actor || !auditclient.ValidActorRequest(actor, p.RequestID) {
		return ErrForbidden
	}
	return nil
}
func (s *Service) CreateBroadcast(ctx context.Context, input BroadcastCreateInput, actor string, canPublish bool) (BroadcastMutationResult, error) {
	if !s.broadcastWriters {
		return BroadcastMutationResult{}, assetclient.ErrUnavailable
	}
	if err := broadcastActor(ctx, actor); err != nil {
		return BroadcastMutationResult{}, err
	}
	input.Title = strings.TrimSpace(input.Title)
	if !ValidOperationKey(input.OperationKey) || !ValidTitle(input.Title) || !ValidDescription(input.Description) || input.Cover != nil && !input.Cover.Valid() {
		return BroadcastMutationResult{}, ErrInvalid
	}
	if (input.Policy.AutoStart || input.Policy.AutoPublish || input.Cover != nil) && !canPublish {
		return BroadcastMutationResult{}, ErrForbidden
	}
	repo, err := s.broadcastRepository()
	if err != nil {
		return BroadcastMutationResult{}, err
	}
	b, err := repo.CreateBroadcast(ctx, input, actor, s.now().UTC())
	if err != nil {
		return BroadcastMutationResult{}, err
	}
	return broadcastResult(b, input.OperationKey), nil
}
func broadcastResult(b Broadcast, key string) BroadcastMutationResult {
	return BroadcastMutationResult{b.View, b.Operations[key].Receipt}
}
func (s *Service) GetBroadcast(ctx context.Context, id string) (BroadcastView, error) {
	repo, err := s.broadcastRepository()
	if err != nil {
		return BroadcastView{}, err
	}
	b, err := repo.GetBroadcast(ctx, id)
	if err == nil {
		b, err = s.recoverBroadcastSignal(ctx, repo, id, b)
	}
	return b.View, err
}
func (s *Service) ListBroadcasts(ctx context.Context, cursor string, limit int) ([]BroadcastView, *string, error) {
	if limit < 1 || limit > 100 || cursor != "" && !validLiveUUID(cursor) {
		return nil, nil, ErrInvalid
	}
	repo, err := s.broadcastRepository()
	if err != nil {
		return nil, nil, err
	}
	bs, err := repo.ListBroadcasts(ctx, cursor, limit+1)
	out := []BroadcastView{}
	var next *string
	for i, b := range bs {
		if i == limit {
			v := bs[i-1].View.RecordingID
			next = &v
			break
		}
		out = append(out, b.View)
	}
	return out, next, err
}
func (s *Service) mutateBroadcast(ctx context.Context, id, actor, key, kind string, expected int64, input any, apply func(context.Context, *Broadcast, *Recording, *Capture) error) (BroadcastMutationResult, error) {
	if err := broadcastActor(ctx, actor); err != nil {
		return BroadcastMutationResult{}, err
	}
	if !ValidOperationKey(key) || expected < 0 || expected > maxBroadcastInteger {
		return BroadcastMutationResult{}, ErrInvalid
	}
	repo, err := s.broadcastRepository()
	if err != nil {
		return BroadcastMutationResult{}, err
	}
	b, err := repo.UpdateBroadcast(ctx, id, actor, s.now().UTC(), func(ctx context.Context, b *Broadcast, r *Recording, c *Capture) error {
		digest := captureDigest(input)
		if old, ok := b.Operations[key]; ok {
			if old.ActorID != actor || old.Digest != digest || old.Receipt.Operation != kind {
				return ErrBroadcastOperation
			}
			return nil
		}
		if len(b.Operations) >= 1024 && kind != "emergency-close" {
			return ErrConflict
		}
		if expected != 0 && expected != b.View.Revision {
			return ErrPrecondition
		}
		if err := apply(ctx, b, r, c); err != nil {
			return err
		}
		p, _ := auditclient.ProvenanceFromContext(ctx)
		receipt := BroadcastReceipt{OperationKey: key, Operation: kind, State: "completed"}
		if b.RangePending || kind == "bind" {
			receipt.State = "accepted"
		}
		if b.View.PendingCommand != nil {
			receipt.CommandID = b.View.PendingCommand.CommandID
			receipt.State = "accepted"
		}
		intent, _ := json.Marshal(input)
		b.Operations[key] = BroadcastOperation{ActorID: actor, RequestID: p.RequestID, Digest: digest, Receipt: receipt, Intent: intent}
		b.View.Revision++
		return nil
	})
	if err != nil {
		return BroadcastMutationResult{}, err
	}
	return broadcastResult(b, key), nil
}
func (s *Service) UpdateBroadcast(ctx context.Context, id, actor string, in BroadcastUpdateInput, canPublish bool) (BroadcastMutationResult, error) {
	if in.ExpectedRevision < 1 || in.Title != nil && !ValidTitle(*in.Title) || in.Description != nil && !ValidDescription(*in.Description) || in.Cover != nil && !in.Cover.Valid() || in.Title == nil && in.Description == nil && !in.ScheduleSet && in.Policy == nil && in.Cover == nil {
		return BroadcastMutationResult{}, ErrInvalid
	}
	scope, _, studio, e := s.broadcastCoverScope(ctx, id)
	if e != nil {
		return BroadcastMutationResult{}, e
	}
	if studio && in.Policy != nil {
		return BroadcastMutationResult{}, ErrInvalid
	}
	if studio && in.Cover != nil && in.Cover.Mode == "custom" {
		if s.packages == nil {
			return BroadcastMutationResult{}, assetclient.ErrUnavailable
		}
		if _, e = s.packages.GetLiveCoverUpload(ctx, "defaults", "", in.Cover.UploadID); e != nil {
			return BroadcastMutationResult{}, e
		}
	}
	return s.mutateBroadcast(ctx, id, actor, in.OperationKey, "update", in.ExpectedRevision, struct {
		BroadcastUpdateInput
		SchedulePresent bool
	}{in, in.ScheduleSet}, func(ctx context.Context, b *Broadcast, r *Recording, c *Capture) error {
		if b.View.Announced && !canPublish || in.Cover != nil && !canPublish || (in.Policy != nil && (in.Policy.AutoStart || in.Policy.AutoPublish) && !canPublish) {
			return ErrForbidden
		}
		if b.View.Phase == "cancelled" || b.View.Phase == "failed" {
			return ErrConflict
		}
		if in.Policy != nil {
			if !b.CanChangePolicy() {
				return ErrConflict
			}
			b.View.Policy = *in.Policy
		}
		if in.ScheduleSet {
			b.View.ScheduledAt = in.ScheduledAt
		}
		if in.Title != nil {
			r.Title = strings.TrimSpace(*in.Title)
		}
		if in.Description != nil {
			r.Description = *in.Description
		}
		if in.Cover != nil {
			b.CoverSnapshot.Settings.Selection = *in.Cover
			b.CoverSnapshot.Settings.Revision++
			b.CoverSnapshot.Settings.CoverState = "pending"
			b.CoverSnapshot.Settings.CoverID = ""
			b.CoverSnapshot.ReferenceID = platform.NewID()
			b.CoverSnapshot.Scope = ""
			if in.Cover.Mode == "custom" {
				b.CoverSnapshot.Scope = scope
			}
			b.CoverSnapshot.PromotionReady = false
			if c != nil {
				c.LiveCover = &b.CoverSnapshot
			}
		}
		return nil
	})
}
func (s *Service) ControlBroadcast(ctx context.Context, id, actor, kind, command string, in BroadcastMutationInput, reason string) (BroadcastMutationResult, error) {
	if in.ExpectedRevision < 1 {
		return BroadcastMutationResult{}, ErrInvalid
	}
	return s.mutateBroadcast(ctx, id, actor, in.OperationKey, kind, in.ExpectedRevision, struct {
		BroadcastMutationInput
		Command, Reason string
	}{in, command, reason}, func(ctx context.Context, b *Broadcast, r *Recording, c *Capture) error {
		switch kind {
		case "announce":
			if b.View.Phase != "draft" && b.View.Phase != "preview" && b.View.Phase != "scheduled" {
				return ErrConflict
			}
			b.View.Announced = true
			if b.View.Phase == "draft" {
				b.View.Phase = "scheduled"
			}
		case "cancel":
			if c != nil && c.Active() || b.View.Boundaries.StartSequence != nil {
				return ErrConflict
			}
			now := s.now().UTC()
			b.CancelledAt = &now
			b.View.Phase = "cancelled"
			b.RevokePublic()
			b.MemberState = "blocked"
		case "start", "end":
			_, err := b.RequestCommand(kind, s.now().UTC())
			return err
		case "cancel-command":
			v, ok := b.Commands[command]
			if !ok || v.Kind != "start" || v.Status != "pending" || b.View.Boundaries.StartSequence != nil {
				return ErrConflict
			}
			v.Status = "cancelled"
			b.Commands[command] = v
			b.View.PendingCommand = nil
			b.View.Phase = "preview"
		case "emergency-close":
			if len(strings.TrimSpace(reason)) == 0 || utf8.RuneCountInString(reason) > 500 || c == nil || b.View.Boundaries.StartSequence == nil {
				return ErrConflict
			}
			// Revocation is durable before any retryable producer-policy I/O.
			b.RevokePublic()
			b.MemberState = "blocked"
			b.RangePending = true
			b.View.Boundaries.Revision++
			b.View.Phase = "processing"
			b.View.Archive = BroadcastArchive{State: "none", Reason: &reason}
			c.ClosePublication()
		default:
			return ErrInvalid
		}
		return nil
	})
}
func (s *Service) AcknowledgeBroadcast(ctx context.Context, id, actor, command string, in BroadcastMarkerAckInput) (BroadcastMutationResult, error) {
	result, err := s.mutateBroadcast(ctx, id, actor, in.OperationKey, "ack", 0, struct {
		Command string
		Input   BroadcastMarkerAckInput
	}{command, in}, func(ctx context.Context, b *Broadcast, r *Recording, c *Capture) error {
		if c == nil {
			return ErrConflict
		}
		return b.Acknowledge(command, actor, in, c.Status.StopAcceptedAt != nil, s.now().UTC())
	})
	if err != nil {
		return result, err
	}
	if err = s.ReconcileBroadcast(ctx, id); err != nil {
		return result, err
	}
	return result, nil
}
func (s *Service) BindBroadcast(ctx context.Context, id, actor string, in BroadcastBindInput, canPublish bool) (BroadcastMutationResult, error) {
	return s.bindBroadcast(ctx, id, actor, in, canPublish, "")
}
func (s *Service) bindBroadcast(ctx context.Context, id, actor string, in BroadcastBindInput, canPublish bool, keyHash string) (BroadcastMutationResult, error) {
	if !s.broadcastWriters {
		return BroadcastMutationResult{}, assetclient.ErrUnavailable
	}
	if err := broadcastActor(ctx, actor); err != nil {
		return BroadcastMutationResult{}, err
	}
	if in.ProtocolVersion != BroadcastProtocolVersion {
		return BroadcastMutationResult{}, ErrBroadcastProtocol
	}
	if !ValidOperationKey(in.OperationKey) || in.ExpectedRevision < 1 {
		return BroadcastMutationResult{}, ErrInvalid
	}
	repo, err := s.broadcastRepository()
	if err != nil {
		return BroadcastMutationResult{}, err
	}
	old, err := repo.GetBroadcast(ctx, id)
	if err != nil {
		return BroadcastMutationResult{}, err
	}
	if (old.View.Policy.AutoStart || old.View.Policy.AutoPublish) && !canPublish {
		return BroadcastMutationResult{}, ErrForbidden
	}
	var c Capture
	if keyHash == "" {
		_, c, err = repo.PrepareBroadcastBinding(ctx, id, actor, in, s.now().UTC())
	} else {
		keyed, ok := repo.(broadcastStreamKeyRepository)
		if !ok {
			return BroadcastMutationResult{}, assetclient.ErrUnavailable
		}
		_, c, err = keyed.PrepareBroadcastStreamKeyBinding(ctx, id, actor, in, keyHash, s.now().UTC())
	}
	if err != nil {
		return BroadcastMutationResult{}, err
	}
	return s.completeBroadcastBinding(ctx, id, actor, in, c)
}
func (s *Service) completeBroadcastBinding(ctx context.Context, id, actor string, in BroadcastBindInput, c Capture) (BroadcastMutationResult, error) {
	repo, err := s.broadcastRepository()
	if err != nil {
		return BroadcastMutationResult{}, err
	}
	var b Broadcast
	if c.Operations[c.Create.OperationKey].Pending {
		c, err = s.deliverCaptureOperation(ctx, c, c.Create.OperationKey)
		if err != nil {
			return BroadcastMutationResult{}, err
		}
	}
	b, err = repo.UpdateBroadcast(ctx, id, actor, s.now().UTC(), func(ctx context.Context, b *Broadcast, r *Recording, current *Capture) error {
		if b.CaptureIntentID != c.IntentID || current == nil || current.Status.CaptureID == "" {
			return ErrConflict
		}
		// Asset creation runs outside this authority transaction. A stop or
		// revocation accepted while it was in flight must not issue a binding.
		if b.Revoked || current.ActorID != actor || current.BroadcastEpoch != b.Epoch || current.Terminal() || current.Status.StopAcceptedAt != nil || !s.now().UTC().Before(current.Status.ExpiresAt) {
			return ErrBroadcastOutputTerminal
		}
		if b.View.Binding == nil {
			b.View.Binding = &BroadcastBinding{c.Status.CaptureID, b.Epoch, actor}
			b.requestAutomaticStart()
			b.View.Revision++
		}
		return nil
	})
	if err != nil {
		return BroadcastMutationResult{}, err
	}
	return broadcastResult(b, in.OperationKey), nil
}

// Keep private reconciliation failures actionable without logging identifiers or provenance.
func broadcastReconciliationError(stage string, b Broadcast, c *Capture, err error) error {
	if !errors.Is(err, assetclient.ErrConflict) {
		return err
	}
	return fmt.Errorf("broadcast reconcile stage=%s recording_ref=%s epoch=%d capture_epoch=%d range_revision=%d applied=%d pending=%t revoked=%t blocked=%t terminal=%t ready=%t live_enabled=%t normal_end=%t: %w", stage, captureDigest(b.View.RecordingID)[:16], b.Epoch, c.BroadcastEpoch, b.View.Boundaries.Revision, b.RangeApplied, b.RangePending, b.Revoked, b.MemberState == "blocked", c.Terminal(), c.Status.State == "ready", c.Status.LiveEnabled, c.NormalEnd, err)
}

func (s *Service) ReconcileBroadcasts(ctx context.Context, limit int) error {
	ctx, endTrace := diagnostics.Reconciliation(ctx)
	defer endTrace()
	repo, err := s.broadcastRepository()
	if err != nil {
		return err
	}
	bs, err := repo.ListBroadcastReconciliation(ctx, s.now().UTC(), limit)
	if err != nil {
		return err
	}
	var failures error
	for _, snapshot := range bs {
		if e := s.ReconcileBroadcast(ctx, snapshot.View.RecordingID); e != nil {
			failures = errors.Join(failures, e)
			continue
		}
		if snapshot.CoverSnapshot.Settings.Selection.Mode == "custom" && snapshot.CoverSnapshot.Settings.CoverState != "ready" && !snapshot.Revoked {
			current, e := repo.GetBroadcast(ctx, snapshot.View.RecordingID)
			if e == nil && !current.Revoked {
				e = s.reconcileBroadcastCover(ctx, current)
			}
			failures = errors.Join(failures, e)
		}
		current, e := repo.GetBroadcast(ctx, snapshot.View.RecordingID)
		if e != nil {
			failures = errors.Join(failures, e)
			continue
		}
		var polledCapture Capture
		var projection *assetclient.BroadcastProjection
		var projectionErr error
		var arrival *assetclient.BroadcastSignal
		var signalErr error
		pollProjection := !current.Revoked && !current.RangePending && current.View.Boundaries.StartSequence != nil && current.CaptureIntentID != ""
		if pollProjection || current.awaitsDirectSignal() {
			captures, e := s.captureRepository()
			if e == nil && pollProjection {
				polledCapture, e = captures.GetCapture(ctx, current.CaptureIntentID, current.View.RecordingID)
			}
			if e != nil {
				failures = errors.Join(failures, e)
				continue
			}
			if s.packages == nil {
				e = assetclient.ErrUnavailable
			}
			if e == nil {
				owner := auditclient.WithPrincipalProvenance(ctx, polledCapture.ActorType, polledCapture.ActorID, polledCapture.RequestID)
				stageCtx, finish := diagnostics.Start(owner, diagnostics.ProjectionPoll)
				p, readErr := s.packages.GetRecordingBroadcastProjection(stageCtx, polledCapture.Status.CaptureID)
				finish(readErr)
				e, projection = readErr, &p
			}
			if e != nil {
				projectionErr = e
			}
			if current.awaitsDirectSignal() && !polledCapture.Terminal() {
				if s.packages == nil {
					signalErr = assetclient.ErrUnavailable
				} else {
					owner := auditclient.WithPrincipalProvenance(ctx, polledCapture.ActorType, polledCapture.ActorID, polledCapture.RequestID)
					stageCtx, finish := diagnostics.Start(owner, diagnostics.SignalReconcile)
					signal, readErr := s.packages.ReconcileRecordingBroadcastSignal(stageCtx, polledCapture.Status.CaptureID, polledCapture.Status.StopAcceptedAt == nil)
					finish(readErr)
					signalErr, arrival = readErr, &signal
				}
			}
		}
		projectionConsumed := false
		_, err = repo.UpdateBroadcast(ctx, snapshot.View.RecordingID, "", s.now().UTC(), func(ctx context.Context, b *Broadcast, r *Recording, c *Capture) error {
			if c == nil {
				return nil
			}
			if arrival != nil || signalErr != nil {
				observed := *c
				observed.Status.RecordingVersion = polledCapture.Status.RecordingVersion
				// Asset serializes its monotonic idle cutoff; CMS applies it only to unchanged authority.
				if b.SourceManaged != current.SourceManaged || captureDigest(*b) != captureDigest(current) || captureDigest(observed) != captureDigest(polledCapture) {
					diagnostics.Discard(ctx, diagnostics.SignalReconcile)
					return nil
				}
				if signalErr != nil {
					return broadcastReconciliationError("signal_reconcile", *b, c, signalErr)
				}
			} else if b.awaitsDirectSignal() && !c.Terminal() {
				return nil
			}
			if c.LiveCover != nil && c.LiveCover.Settings.Revision >= b.CoverSnapshot.Settings.Revision {
				b.CoverSnapshot = *c.LiveCover
			}
			beforeView := captureDigest(b.View)
			changed := false
			if b.View.Binding == nil && c.Status.CaptureID != "" {
				b.View.Binding = &BroadcastBinding{c.Status.CaptureID, b.Epoch, c.ActorID}
				changed = true
			}
			if b.View.Binding == nil {
				return nil
			}
			if c.Status.StopAcceptedAt == nil && !c.Terminal() && b.requestAutomaticStart() {
				changed = true
			}
			if c.NormalEnd && c.Status.Progress.Ended && b.View.Boundaries.StartSequence == nil {
				b.View.Phase = "cancelled"
				now := s.now().UTC()
				b.CancelledAt = &now
				c.ClosePublication()
				changed = true
			}
			if arrival != nil {
				b.applyRecoverySignal(*arrival, c.RecoveryVersion)
				ended, e := b.applyDirectSignal(*arrival)
				if e != nil {
					return e
				}
				changed = changed || ended
			}
			if !b.Revoked && c.NormalEnd && c.Status.Progress.Ended && b.View.Policy.AutoEnd && b.View.Boundaries.StartSequence != nil && b.View.Boundaries.EndSequenceExclusive == nil {
				end, e := captureInventoryEnd(*c)
				if e != nil {
					return e
				}
				if end <= *b.View.Boundaries.StartSequence {
					return ErrConflict
				}
				b.View.Boundaries.EndSequenceExclusive = &end
				b.recordPublicEnd("capture_terminal", &end)
				b.View.Boundaries.Revision++
				b.RangePending = true
				changed = true
				if pc := b.View.PendingCommand; pc != nil && pc.Kind == "end" {
					pc.Status = "acknowledged"
					pc.BoundarySequence = &end
					b.Commands[pc.CommandID] = *pc
					b.View.PendingCommand = nil
				}
			}
			if b.RangePending {
				if changed && captureDigest(b.View) != beforeView {
					b.View.Revision++
				}
				return nil
			}
			if !b.Revoked && b.View.Boundaries.StartSequence != nil {
				// The read must not hold viewer fences; discard results whose authority changed in flight.
				stale := b.CaptureIntentID != current.CaptureIntentID || b.Epoch != current.Epoch ||
					b.View.Revision != current.View.Revision || b.Revoked != current.Revoked || b.RangePending != current.RangePending ||
					b.MemberState != current.MemberState || b.RangeApplied != current.RangeApplied || captureDigest(b.View.Binding) != captureDigest(current.View.Binding) ||
					b.View.Boundaries.Revision != current.View.Boundaries.Revision || !equalSequence(b.View.Boundaries.StartSequence, current.View.Boundaries.StartSequence) || !equalSequence(b.View.Boundaries.EndSequenceExclusive, current.View.Boundaries.EndSequenceExclusive) ||
					c.IntentID != polledCapture.IntentID || c.Status.CaptureID != polledCapture.Status.CaptureID || c.ActorID != polledCapture.ActorID || c.ActorType != polledCapture.ActorType || c.BroadcastEpoch != polledCapture.BroadcastEpoch ||
					c.Status.State != polledCapture.Status.State || c.Terminal() != polledCapture.Terminal() || c.NormalEnd != polledCapture.NormalEnd || captureDigest(c.Status.StopAcceptedAt) != captureDigest(polledCapture.Status.StopAcceptedAt)
				if !stale && projectionErr != nil {
					return broadcastReconciliationError("projection_poll", *b, c, projectionErr)
				}
				if !stale && projection != nil && b.Projection != nil && b.Projection.Epoch == projection.Epoch && b.Projection.RangeRevision == projection.RangeRevision && b.Projection.Revision > projection.Revision {
					stale = true
				}
				if !stale {
					projectionConsumed = true
				}
				if !stale && projection != nil {
					p := *projection
					if p.Epoch != b.Epoch || p.RangeRevision != b.View.Boundaries.Revision || !equalSequence(p.StartSequence, b.View.Boundaries.StartSequence) || !equalSequence(p.EndSequenceExclusive, b.View.Boundaries.EndSequenceExclusive) {
						return ErrConflict
					}
					b.Projection = &p
					b.observePublicEnd(s.now().UTC())
					if p.State == "failed" {
						b.View.Archive.State = "failed"
						b.View.Phase = "failed"
						changed = true
					} else if p.State == "ready" {
						if b.View.Boundaries.EndSequenceExclusive == nil {
							if b.View.Phase != "end_pending" && b.View.Phase != "live" {
								b.markLive(s.now().UTC())
								changed = true
							}
							c.Status.LiveEnabled = true
						} else {
							if c.NormalEnd && c.Status.State == "ready" {
								if !b.AutoPublicationActivated && !b.PublicationWithdrawn {
									cancelled := false
									for _, op := range c.Operations {
										if op.Operation == "cancel_auto_publish" {
											cancelled = true
										}
									}
									if b.View.Policy.AutoPublish && r.Status == "draft" && c.Status.AutoPublish == "cancelled" && !cancelled {
										c.Status.AutoPublish = "pending"
									}
									b.AutoPublicationActivated = true
								}
								b.updateArchivePublication(*r, *c)
								changed = true
							} else {
								b.View.Phase = "processing"
								b.View.Archive.State = "processing"
								changed = true
							}
						}
					}
				}
			}
			prepareBroadcastPolicy(b, r, s.now())
			c.Status.LiveEnabled = !b.Revoked && (b.MemberState == "live" || b.MemberState == "vod")
			b.View.Health = BroadcastHealth{State: "waiting"}
			if c.Status.Progress.LastSequence >= 0 {
				state := "healthy"
				if c.Status.StopAcceptedAt != nil || c.Status.Progress.LastAdvancedAt == nil || s.now().Sub(*c.Status.Progress.LastAdvancedAt) > 90*time.Second {
					state = "interrupted"
				}
				seconds := c.Status.Progress.MediaEndSeconds
				b.View.Health = BroadcastHealth{State: state, LastPublishedAt: c.Status.Progress.LastAdvancedAt, VerifiedEndSeconds: &seconds}
			}
			if arrival != nil {
				b.View.Health.LastConfirmedAt = arrival.LastMediaConfirmedAt
				if c.Status.StopAcceptedAt == nil && arrival.State != "ended" {
					b.View.Health.State = arrival.State
				}
				if c.Status.StopAcceptedAt == nil && arrival.State == "interrupted" {
					reason := "signal_grace"
					b.View.Health.Reason = &reason
				}
				// Health transitions must survive client revision fencing too.
				changed = changed || captureDigest(b.View) != beforeView
			}
			if changed && captureDigest(b.View) != beforeView {
				b.View.Revision++
			}
			return nil
		})
		if err == nil {
			if !projectionConsumed && (projection != nil || projectionErr != nil) {
				diagnostics.Discard(ctx, diagnostics.ProjectionPoll)
			}
			err = s.ReconcileBroadcast(ctx, snapshot.View.RecordingID)
		}
		failures = errors.Join(failures, err)
	}
	return errors.Join(failures, repo.ReconcileBroadcastCleanup(ctx, s.now().UTC()))
}
func equalSequence(a, b *int) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }
func captureInventoryEnd(c Capture) (int, error) {
	for _, op := range c.Operations {
		if op.Operation == "seal" && (op.Pending || len(bytes.TrimSpace(op.Input)) > 0 && !bytes.Equal(bytes.TrimSpace(op.Input), []byte("null"))) {
			return 0, ErrConflict
		}
	}
	if c.SealedEndSequence == nil {
		return 0, ErrConflict
	}
	return *c.SealedEndSequence, nil
}

type RecordingWatchCover struct {
	Revision    string `json:"revision"`
	ContentPath string `json:"contentPath"`
}
type RecordingWatchView struct {
	progressVersion   string
	CoverMode         string               `json:"coverMode,omitempty"`
	PublicID          string               `json:"publicId,omitempty"`
	RecordingID       string               `json:"recordingId"`
	Title             string               `json:"title"`
	Description       string               `json:"description"`
	View              string               `json:"view"`
	ScheduledAt       *time.Time           `json:"scheduledAt"`
	ServerNow         time.Time            `json:"serverNow"`
	Cover             *RecordingWatchCover `json:"cover"`
	StateRevision     int64                `json:"stateRevision"`
	Recording         *Recording           `json:"recording"`
	Live              *LiveRecording       `json:"live"`
	MediaStartSeconds *float64             `json:"mediaStartSeconds"`
	DurationSeconds   *float64             `json:"durationSeconds"`
}

// SubjectReader reads through the connection already held by the DSR fence.
// It is valid only during the callback; writes retain their explicit owners.
type SubjectReader interface {
	Get(context.Context, string) (Recording, error)
	GetBroadcast(context.Context, string) (Broadcast, error)
	LatestRecordingCapture(context.Context, string) (Capture, error)
}
type recordingSubjectRepository interface {
	WithRecordingSubject(context.Context, string, func(SubjectReader) error) error
}

func (s *Service) ResolveRecordingWatch(ctx context.Context, id, user string) (RecordingWatchView, error) {
	var out RecordingWatchView
	if !validLiveUUID(id) || !validLiveUUID(user) {
		return out, ErrInvalid
	}
	repo, ok := s.repo.(recordingSubjectRepository)
	if !ok {
		return out, assetclient.ErrUnavailable
	}
	var pendingPackage *Recording
	var pendingCapture *Capture
	var packageStatus *assetclient.RecordingPackageStatus
	var captureStatus *Capture
	final := false
	read := func(reader SubjectReader) error {
		r, e := reader.Get(ctx, id)
		if e != nil {
			return e
		}
		out = RecordingWatchView{PublicID: r.PublicID, RecordingID: id, Title: r.Title, Description: r.Description, View: "unavailable", ServerNow: s.now().UTC(), StateRevision: r.Version}
		if r.Broadcast {
			b, e := reader.GetBroadcast(ctx, id)
			if e != nil {
				return e
			}
			if !b.View.Announced {
				return ErrNotFound
			}
		}
		if r.Expired(s.now()) {
			out.View = "expired"
			return nil
		}
		if r.Broadcast {
			b, e := reader.GetBroadcast(ctx, id)
			if e != nil {
				return e
			}
			if !b.View.Announced {
				return ErrNotFound
			}
			out.CoverMode = liveCoverMode(&b.CoverSnapshot)
			out.ScheduledAt = b.View.ScheduledAt
			out.StateRevision = b.View.Revision
			if b.CoverSnapshot.Settings.CoverState == "ready" && b.CoverSnapshot.Settings.CoverID != "" {
				out.Cover = &RecordingWatchCover{Revision: fmt.Sprint(b.CoverSnapshot.Settings.Revision), ContentPath: "/api/member/recordings/" + id + "/broadcast-cover"}
			}
			if b.Revoked {
				if b.View.Phase == "cancelled" {
					out.View = "cancelled"
				}
				return nil
			}
			switch b.View.Phase {
			case "draft", "scheduled", "preview", "start_pending":
				out.View = "waiting"
			case "live", "end_pending":
				out.View = "live"
			case "processing", "archived":
				out.View = "processing"
			case "cancelled":
				out.View = "cancelled"
			}
			if b.Projection != nil && b.Projection.State == "ready" {
				out.MediaStartSeconds = b.Projection.MediaStartSeconds
				out.DurationSeconds = b.Projection.DurationSeconds
			}
			if r.Visible(s.now()) && b.MemberVODReady() {
				if !final {
					pendingPackage = &r
					return nil
				}
				if packageStatus == nil || packageStatus.PackageID != r.PackageID || packageStatus.MediaExpiresAt == nil || !s.now().Before(*packageStatus.MediaExpiresAt) {
					return ErrConflict
				}
				r.DurationSeconds = *b.Projection.DurationSeconds
				out.View = "vod"
				out.progressVersion = b.VODProgressVersion(r.PackageID)
				out.Recording = &r
			}
			if out.View == "live" && b.View.Binding != nil {
				c, e := reader.LatestRecordingCapture(ctx, id)
				if e != nil {
					return e
				}
				if !c.LiveAvailable(s.now()) {
					out.View = "unavailable"
					return nil
				}
				out.Live = &LiveRecording{PublicID: r.PublicID, ID: id, StartedAt: r.BroadcastStartedAt, CaptureID: b.View.Binding.CaptureID, Title: r.Title, Description: r.Description, LiveState: c.Status.LiveState, CreatedAt: c.Status.CreatedAt, StopAcceptedAt: c.Status.StopAcceptedAt, Progress: c.Status.Progress, CoverRevision: liveCoverRevision(&b.CoverSnapshot), CoverMode: liveCoverMode(&b.CoverSnapshot)}
			}
			return nil
		}
		if r.Visible(s.now()) {
			if !final {
				pendingPackage = &r
				return nil
			}
			if packageStatus == nil || packageStatus.PackageID != r.PackageID || packageStatus.MediaExpiresAt == nil || !s.now().Before(*packageStatus.MediaExpiresAt) {
				return ErrConflict
			}
			if len(packageStatus.Renditions) > 0 {
				r.DurationSeconds = packageStatus.Renditions[0].DurationSeconds
			}
			out.View = "vod"
			out.progressVersion = "vod:" + r.PackageID
			out.Recording = &r
			return nil
		}
		c, e := reader.LatestRecordingCapture(ctx, id)
		if errors.Is(e, ErrNotFound) {
			return ErrNotFound
		}
		if e != nil {
			return e
		}
		if !final {
			pendingCapture = &c
			return nil
		}
		if captureStatus == nil || captureStatus.IntentID != c.IntentID || captureStatus.Status.CaptureID != c.Status.CaptureID || captureStatus.ActorID != c.ActorID || captureStatus.ActorType != c.ActorType || captureStatus.BroadcastEpoch != c.BroadcastEpoch {
			return ErrConflict
		}
		// Merge only the observed producer status into current control/expiry authority.
		if e = mergeCaptureStatus(&c, captureStatus.Status.RecordingCaptureStatus, s.now()); e != nil {
			return e
		}
		if c.LiveAvailable(s.now()) {
			out.View = "live"
			out.Live = &LiveRecording{PublicID: r.PublicID, ID: id, StartedAt: r.BroadcastStartedAt, CaptureID: c.Status.CaptureID, Title: r.Title, Description: r.Description, LiveState: c.Status.LiveState, CreatedAt: c.Status.CreatedAt, StopAcceptedAt: c.Status.StopAcceptedAt, Progress: c.Status.Progress, CoverRevision: liveCoverRevision(c.LiveCover), CoverMode: liveCoverMode(c.LiveCover)}
		}
		return nil
	}
	err := repo.WithRecordingSubject(ctx, user, read)
	if err != nil || pendingPackage == nil && pendingCapture == nil {
		return out, err
	}
	// Dependency projection/recovery owns its own SQL scope, outside the subject fence.
	if pendingPackage != nil {
		status, e := s.readyPackage(ctx, *pendingPackage)
		if e != nil {
			return out, e
		}
		packageStatus = &status
	} else {
		status, e := s.refreshLiveCapture(ctx, *pendingCapture)
		if e != nil {
			return out, e
		}
		captureStatus = &status
	}
	final = true
	err = repo.WithRecordingSubject(ctx, user, read)
	return out, err
}

// Publication commits before Asset applies its new authority revision.
func (b Broadcast) MemberVODReady() bool {
	p := b.Projection
	return b.View.Announced && !b.Revoked && !b.RangePending && b.MemberState == "vod" &&
		b.Epoch > 0 && b.RangeApplied == b.View.Boundaries.Revision &&
		p != nil && p.State == "ready" && p.DurationSeconds != nil &&
		p.Epoch == b.Epoch && p.RangeRevision == b.RangeApplied &&
		b.View.Boundaries.StartSequence != nil && b.View.Boundaries.EndSequenceExclusive != nil &&
		equalSequence(p.StartSequence, b.View.Boundaries.StartSequence) && equalSequence(p.EndSequenceExclusive, b.View.Boundaries.EndSequenceExclusive)
}

type upcomingRepository interface {
	ListUpcomingBroadcastPage(context.Context, time.Time, *time.Time, string, int, string) ([]Broadcast, error)
}

func (s *Service) ListUpcomingBroadcasts(ctx context.Context, user, cursor string, limit int, search ...string) ([]RecordingWatchView, *string, error) {
	query := ""
	if len(search) > 0 {
		query = search[0]
	}
	query, e := NormalizeSearch(query)
	if e != nil {
		return nil, nil, e
	}
	if limit < 1 || limit > 100 || len(cursor) > 256 {
		return nil, nil, ErrInvalid
	}
	repo, ok := s.repo.(upcomingRepository)
	if !ok {
		return nil, nil, assetclient.ErrUnavailable
	}
	var after pageCursor
	var at *time.Time
	if cursor != "" {
		raw, e := base64.RawURLEncoding.DecodeString(cursor)
		if e != nil || json.Unmarshal(raw, &after) != nil || after.Version != 2 || after.UploadedAt.IsZero() || !validLiveUUID(after.ID) || after.Query != searchDigest(query) {
			return nil, nil, ErrInvalid
		}
		at = &after.UploadedAt
	}
	out := []RecordingWatchView{}
	for {
		values, e := repo.ListUpcomingBroadcastPage(ctx, s.now(), at, after.ID, limit+1, query)
		if e != nil {
			return nil, nil, e
		}
		for _, b := range values {
			watch, e := s.ResolveRecordingWatch(ctx, b.View.RecordingID, user)
			if e != nil && !errors.Is(e, ErrNotFound) {
				return nil, nil, e
			}
			if e == nil && watch.View == "waiting" {
				if len(out) == limit {
					raw, _ := json.Marshal(after)
					next := base64.RawURLEncoding.EncodeToString(raw)
					return out, &next, nil
				}
				out = append(out, watch)
			}
			after = pageCursor{UploadedAt: *b.View.ScheduledAt, ID: b.View.RecordingID, Version: 2, Query: searchDigest(query)}
			at = &after.UploadedAt
		}
		if len(values) < limit+1 {
			return out, nil, nil
		}
	}
}

type BroadcastPreviewAccess struct {
	RecordingID        string    `json:"recordingId"`
	CaptureID          string    `json:"captureId"`
	Epoch              int64     `json:"epoch"`
	Purpose            string    `json:"purpose"`
	PlaybackScopeID    string    `json:"playbackScopeId"`
	MediaURL           string    `json:"mediaUrl"`
	ExchangeCredential string    `json:"exchangeCredential"`
	IssuedAt           time.Time `json:"issuedAt"`
	ExpiresAt          time.Time `json:"expiresAt"`
}

func (s *Service) BroadcastPreview(ctx context.Context, id, actor, capture string, epoch int64, scope string) (BroadcastPreviewAccess, error) {
	var out BroadcastPreviewAccess
	if err := broadcastActor(ctx, actor); err != nil {
		return out, err
	}
	if s.mediaBaseURL == "" {
		return out, assetclient.ErrUnavailable
	}
	repo, e := s.broadcastRepository()
	if e != nil {
		return out, e
	}
	renew := scope != ""
	var until time.Time
	validate := func(ctx context.Context, b *Broadcast, r *Recording, c *Capture) error {
		if b.Revoked || b.CancelledAt != nil || b.View.Binding == nil || b.View.Binding.CaptureID != capture || b.View.Binding.Epoch != epoch || b.Epoch != epoch || c == nil || c.IntentID != b.CaptureIntentID || c.Status.CaptureID != capture || c.BroadcastEpoch != epoch || c.Status.StopAcceptedAt != nil || c.Terminal() || !c.Active() || !c.Status.ExpiresAt.After(s.now()) {
			return ErrNotFound
		}
		if s.packages == nil {
			return assetclient.ErrUnavailable
		}
		for scope, prior := range b.Previews {
			if !prior.ExpiresAt.After(s.now()) {
				delete(b.Previews, scope)
			}
		}
		if renew {
			prior, ok := b.Previews[scope]
			if !ok || prior.ActorID != actor || prior.CaptureID != capture || prior.Epoch != epoch || prior.Scope != scope || prior.Purpose != "staff-preview" || !prior.ExpiresAt.After(s.now()) {
				return ErrNotFound
			}
		} else if len(b.Previews) >= 100 {
			return ErrConflict
		}
		if b.Previews == nil {
			b.Previews = map[string]BroadcastPreviewAudit{}
		}
		if scope == "" {
			scope = platform.NewID()
		}
		until = s.now().UTC().Add(5 * time.Minute)
		if c.Status.ExpiresAt.Before(until) {
			until = c.Status.ExpiresAt
		}
		return nil
	}
	if _, e = repo.UpdateBroadcast(ctx, id, actor, s.now().UTC(), validate); e != nil {
		return out, e
	}
	grant, e := s.packages.IssueRecordingBroadcastPreview(ctx, capture, assetclient.BroadcastPreviewGrantInput{RecordingID: id, Epoch: epoch, UserID: actor, PlaybackScopeID: scope, ExpiresAt: until})
	if e != nil {
		return out, e
	}
	expected := "/videos/" + id + "/captures/" + capture + "/previews/" + scope + "/master.m3u8"
	if grant.RecordingID != id || grant.CaptureID != capture || grant.Epoch != epoch || grant.Purpose != "staff-preview" || grant.Scope != scope || grant.MediaPath != expected || grant.ExchangeCredential == "" || len(grant.ExchangeCredential) > 8192 || !grant.ExpiresAt.After(s.now()) || grant.ExpiresAt.After(until) || grant.IssuedAt.After(s.now().Add(5*time.Second)) || !grant.ExpiresAt.After(grant.IssuedAt) {
		return out, ErrConflict
	}
	_, e = repo.UpdateBroadcast(ctx, id, actor, s.now().UTC(), func(ctx context.Context, b *Broadcast, r *Recording, c *Capture) error {
		if err := validate(ctx, b, r, c); err != nil {
			return err
		}
		if grant.ExpiresAt.After(until) {
			return ErrNotFound
		}
		if prior, ok := b.Previews[scope]; ok && grant.ExpiresAt.Before(prior.ExpiresAt) {
			return ErrConflict
		}
		p, _ := auditclient.ProvenanceFromContext(ctx)
		b.Previews[scope] = BroadcastPreviewAudit{ActorID: actor, RequestID: p.RequestID, CaptureID: capture, Epoch: epoch, Scope: scope, Purpose: grant.Purpose, IssuedAt: grant.IssuedAt, ExpiresAt: grant.ExpiresAt}
		out = BroadcastPreviewAccess{id, capture, epoch, grant.Purpose, scope, s.mediaBaseURL + grant.MediaPath, grant.ExchangeCredential, grant.IssuedAt, grant.ExpiresAt}
		return nil
	})
	if e != nil {
		return BroadcastPreviewAccess{}, e
	}
	return out, e
}
func (s *Service) UploadBroadcastCover(ctx context.Context, id, mime, key string, body io.Reader) (assetclient.RecordingCoverUpload, error) {
	if !ValidOperationKey(key) || mime != "image/jpeg" && mime != "image/png" {
		return assetclient.RecordingCoverUpload{}, ErrInvalid
	}
	if _, e := s.GetBroadcast(ctx, id); e != nil {
		return assetclient.RecordingCoverUpload{}, e
	}
	if s.packages == nil {
		return assetclient.RecordingCoverUpload{}, assetclient.ErrUnavailable
	}
	data, e := io.ReadAll(io.LimitReader(body, 5<<20+1))
	if e != nil {
		return assetclient.RecordingCoverUpload{}, e
	}
	scope, owner, _, e := s.broadcastCoverScope(ctx, id)
	if e != nil {
		return assetclient.RecordingCoverUpload{}, e
	}
	return s.packages.UploadLiveCover(ctx, scope, owner, mime, key, data)
}
func (s *Service) GetBroadcastCoverUpload(ctx context.Context, id, upload string) (assetclient.RecordingCoverUpload, error) {
	if _, e := s.GetBroadcast(ctx, id); e != nil {
		return assetclient.RecordingCoverUpload{}, e
	}
	if s.packages == nil {
		return assetclient.RecordingCoverUpload{}, assetclient.ErrUnavailable
	}
	scope, owner, _, e := s.broadcastCoverScope(ctx, id)
	if e != nil {
		return assetclient.RecordingCoverUpload{}, e
	}
	return s.packages.GetLiveCoverUpload(ctx, scope, owner, upload)
}
func (s *Service) BroadcastCoverContent(ctx context.Context, id, upload, user string, member bool) ([]byte, error) {
	var out []byte
	var scope, owner, authority string
	read := func(reader SubjectReader) error {
		repo, e := s.broadcastRepository()
		if e != nil {
			return e
		}
		var b Broadcast
		if reader != nil {
			b, e = reader.GetBroadcast(ctx, id)
		} else {
			b, e = repo.GetBroadcast(ctx, id)
		}
		if e != nil {
			return e
		}
		if member {
			value, e := reader.Get(ctx, id)
			if e != nil {
				return e
			}
			if !b.View.Announced || b.Revoked || value.MediaTerminal || value.ExpiresAt != nil && !s.now().Before(*value.ExpiresAt) {
				return ErrNotFound
			}
			authority = captureDigest(struct {
				Broadcast Broadcast
				Recording Recording
			}{b, value})
		}
		scope, owner = "", ""
		if !member {
			scope, owner, _, e = s.broadcastCoverScope(ctx, id)
			if e != nil {
				return e
			}
		}
		if upload == "" || member {
			if b.CoverSnapshot.Settings.CoverState != "ready" || b.CoverSnapshot.Settings.CoverID == "" {
				return ErrNotFound
			}
			upload = b.CoverSnapshot.Settings.CoverID
			scope = b.CoverSnapshot.Scope
			if scope == "" && b.View.Binding != nil {
				scope = b.View.Binding.CaptureID
			}
			if scope == "defaults" {
				owner = ""
			} else {
				owner = id
			}
		}
		return nil
	}
	if s.packages == nil {
		return nil, assetclient.ErrUnavailable
	}
	if member {
		repo, ok := s.repo.(recordingSubjectRepository)
		if !ok {
			return nil, assetclient.ErrUnavailable
		}
		if e := repo.WithRecordingSubject(ctx, user, read); e != nil {
			return nil, e
		}
		snapshot := authority
		var e error
		out, e = s.packages.ReadLiveCover(ctx, scope, owner, upload)
		if e != nil {
			return nil, e
		}
		e = repo.WithRecordingSubject(ctx, user, func(reader SubjectReader) error {
			if err := read(reader); err != nil {
				return err
			}
			if authority != snapshot {
				return ErrNotFound
			}
			return nil
		})
		if e != nil {
			return nil, e
		}
		return out, nil
	}
	if e := read(nil); e != nil {
		return nil, e
	}
	return s.packages.ReadLiveCover(ctx, scope, owner, upload)
}

// Prepare, apply and acknowledge are separate transactions. A successful producer
// write can never outrun the durable intent, even if the read or CMS commit fails.
func (s *Service) ReconcileBroadcast(ctx context.Context, id string) error {
	repo, e := s.broadcastRepository()
	if e != nil {
		return e
	}
	captures, e := s.captureRepository()
	if e != nil {
		return e
	}
	for attempt := 0; attempt < 3; attempt++ {
		snapshot, e := repo.UpdateBroadcast(ctx, id, "", s.now().UTC(), func(ctx context.Context, b *Broadcast, r *Recording, c *Capture) error {
			// Terminal authority must commit before Asset can reject a stale public range.
			if c != nil && c.Status.CaptureID != "" && c.Terminal() && (!b.Revoked || b.MemberState != "blocked" || b.View.Phase != "failed" && b.View.Phase != "cancelled") {
				b.RevokePublic()
				if b.View.Phase != "cancelled" {
					b.View.Phase = "failed"
					b.View.Archive = BroadcastArchive{State: "failed", Reason: c.Status.TerminalReason}
				}
				b.MemberState = "blocked"
				b.RangePending = true
				b.View.Boundaries.Revision++
				b.View.Revision++
			}
			if c != nil && !b.RangePending {
				prepareBroadcastPolicy(b, r, s.now())
			}
			return nil
		})
		if e != nil {
			return e
		}
		if !snapshot.RangePending {
			return nil
		}
		if s.packages == nil {
			return assetclient.ErrUnavailable
		}
		c, e := captures.GetCapture(ctx, snapshot.CaptureIntentID, id)
		if e != nil {
			return e
		}
		owner := auditclient.WithPrincipalProvenance(ctx, c.ActorType, c.ActorID, c.RequestID)
		intent := assetclient.BroadcastRangeInput{RecordingID: id, Epoch: snapshot.Epoch, RangeRevision: snapshot.View.Boundaries.Revision, StartSequence: snapshot.View.Boundaries.StartSequence, EndSequenceExclusive: snapshot.View.Boundaries.EndSequenceExclusive, Revoked: snapshot.Revoked, MemberState: snapshot.MemberState}
		if _, e = s.packages.SetRecordingBroadcastRange(owner, c.Status.CaptureID, intent); e != nil {
			// Ready, inactive captures are no longer selected by periodic capture refresh.
			if errors.Is(e, assetclient.ErrConflict) && !snapshot.Revoked && !c.Terminal() {
				status, refreshErr := s.CaptureStatus(owner, id, c.Status.CaptureID, c.ActorID, "", 1)
				if refreshErr != nil {
					return broadcastReconciliationError("capture_refresh", snapshot, &c, refreshErr)
				}
				c.Status = status
				if c.Terminal() {
					continue
				}
			}
			return broadcastReconciliationError("range_apply", snapshot, &c, e)
		}
		projection, e := s.packages.GetRecordingBroadcastProjection(owner, c.Status.CaptureID)
		if e != nil {
			return broadcastReconciliationError("range_projection", snapshot, &c, e)
		}
		if projection.Epoch != intent.Epoch || projection.RangeRevision != intent.RangeRevision || intent.Revoked && projection.State != "failed" || !equalSequence(projection.StartSequence, intent.StartSequence) || !equalSequence(projection.EndSequenceExclusive, intent.EndSequenceExclusive) {
			return ErrConflict
		}
		_, e = repo.UpdateBroadcast(ctx, id, "", s.now().UTC(), func(ctx context.Context, b *Broadcast, r *Recording, c *Capture) error {
			if b.Epoch != intent.Epoch || b.View.Boundaries.Revision != intent.RangeRevision || b.MemberState != intent.MemberState || b.Revoked != intent.Revoked || !equalSequence(b.View.Boundaries.StartSequence, intent.StartSequence) || !equalSequence(b.View.Boundaries.EndSequenceExclusive, intent.EndSequenceExclusive) {
				return nil
			}
			b.RangeApplied = intent.RangeRevision
			b.Projection = &projection
			b.observePublicEnd(s.now().UTC())
			b.RangePending = false
			if !b.Revoked && projection.State == "ready" {
				if b.View.Boundaries.EndSequenceExclusive != nil && (b.View.Phase == "live" || b.View.Phase == "end_pending") {
					b.View.Phase = "processing"
					b.View.Archive.State = "processing"
					b.View.Revision++
					if c != nil {
						c.Status.LiveEnabled = true
					}
				} else if b.View.Boundaries.EndSequenceExclusive == nil && b.View.Phase == "start_pending" {
					b.markLive(s.now().UTC())
					b.View.Revision++
					if c != nil {
						c.Status.LiveEnabled = true
					}
				}
			}
			prepareBroadcastPolicy(b, r, s.now())
			return nil
		})
		if e != nil {
			return e
		}
	}
	return assetclient.ErrUnavailable
}
func (s *Service) reconcileBroadcastCover(ctx context.Context, b Broadcast) error {
	repo, e := s.liveCoverRepository()
	if e != nil {
		return e
	}
	if s.packages == nil {
		return assetclient.ErrUnavailable
	}
	owner := b.View.RecordingID
	if b.CoverSnapshot.Scope == "defaults" {
		owner = ""
	}
	ref := LiveCoverReference{ID: b.CoverSnapshot.ReferenceID, Target: "broadcast:" + b.View.RecordingID, CoverID: b.CoverSnapshot.Settings.Selection.UploadID, Scope: b.CoverSnapshot.Scope, RecordingID: owner, Wanted: true}
	return repo.ApplyLiveCoverReference(ctx, ref, func(ctx context.Context) error {
		upload, e := s.packages.GetLiveCoverUpload(ctx, ref.Scope, ref.RecordingID, ref.CoverID)
		if e != nil {
			return e
		}
		if upload.State != "ready" {
			return ErrConflict
		}
		return s.packages.RetainLiveCover(ctx, ref.Scope, ref.RecordingID, ref.CoverID, ref.ID)
	})
}

// Member access is independent of raw capture readiness. Every change advances
// the Worker authority revision and independently gates member access.
func broadcastMemberState(b Broadcast, r Recording, now time.Time) string {
	if b.Revoked || b.PublicationWithdrawn || r.Expired(now) || b.View.Boundaries.StartSequence == nil {
		return "blocked"
	}
	if r.Visible(now) && b.View.Boundaries.EndSequenceExclusive != nil {
		return "vod"
	}
	ready := b.Projection != nil && b.Projection.State == "ready" && equalSequence(b.Projection.StartSequence, b.View.Boundaries.StartSequence) && equalSequence(b.Projection.EndSequenceExclusive, b.View.Boundaries.EndSequenceExclusive)
	if (b.View.Phase == "live" || b.View.Phase == "end_pending") && (b.View.Boundaries.EndSequenceExclusive == nil || !ready) {
		return "live"
	}
	// Keep admitted live scopes usable while the archive is being prepared.
	// The scope registry rejects new viewers; replay/retention deadlines still apply.
	if b.View.Phase == "processing" && b.View.Boundaries.EndSequenceExclusive != nil && ready {
		return "live"
	}
	return "blocked"
}
func prepareBroadcastPolicy(b *Broadcast, r *Recording, now time.Time) {
	if b.RangePending {
		return
	}
	if desired := broadcastMemberState(*b, *r, now); b.MemberState != desired {
		b.MemberState = desired
		b.View.Boundaries.Revision++
		b.RangePending = true
	}
}

// Display publication intent only after media and the public range are ready.
func (b *Broadcast) updateArchivePublication(r Recording, c Capture) {
	b.View.Archive = BroadcastArchive{State: "ready"}
	b.View.Phase = "processing"
	if b.PublicationWithdrawn {
		return
	}
	if c.Status.AutoPublish == "published" || r.Status == "published" {
		b.View.Archive.State = "published"
		b.View.Phase = "archived"
		return
	}
	reason := ""
	switch {
	case !b.View.Policy.AutoPublish:
		reason = "auto_publish_disabled"
	case c.Status.AutoPublish == "cancelled":
		reason = "auto_publish_cancelled"
	case c.Status.AutoPublish == "blocked":
		reason = "auto_publish_blocked"
	case c.Status.AutoPublish == "pending" && c.AutoNextAttemptAt != nil:
		reason = "auto_publish_retrying"
	}
	if reason != "" {
		b.View.Archive.Reason = &reason
	}
}

// Persist the automatic intent before ingest can race with Stop. Projection
// readiness still gates public access and the actual StartedAt timestamp.
func (b *Broadcast) requestAutomaticStart() bool {
	if b.Revoked || b.View.Binding == nil || !b.View.Policy.AutoStart || !b.View.Announced || b.View.Boundaries.StartSequence != nil || b.View.Phase != "preview" {
		return false
	}
	zero := 0
	b.View.Boundaries.StartSequence = &zero
	b.View.Boundaries.Revision++
	b.View.Phase = "start_pending"
	b.RangePending = true
	return true
}

// Admission needs three verified public segments and the applied member authority.
func (b Broadcast) LiveNotificationReady(c Capture, now time.Time) bool {
	p := b.Projection
	start := b.View.Boundaries.StartSequence
	return b.View.Announced && b.View.Phase == "live" && !b.Revoked && !b.RangePending && b.MemberState == "live" &&
		b.Epoch > 0 && start != nil && b.View.Boundaries.EndSequenceExclusive == nil && b.RangeApplied == b.View.Boundaries.Revision &&
		p != nil && p.State == "ready" && p.Epoch == b.Epoch && p.RangeRevision == b.RangeApplied && equalSequence(p.StartSequence, start) && p.EndSequenceExclusive == nil &&
		c.LiveAvailable(now) && c.Status.StopAcceptedAt == nil && c.Status.Progress.LastSequence-*start >= 2
}
