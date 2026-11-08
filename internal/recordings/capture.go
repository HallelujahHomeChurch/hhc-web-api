package recordings

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

var captureHashPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

var capturePathPattern = regexp.MustCompile(`^(master\.m3u8|(480p|720p|1080p)/(index\.m3u8|init\.mp4|seg-[0-9]{6}\.m4s))$`)

type CaptureCreateInput struct {
	OperationKey    string `json:"operationKey"`
	ExpectedVersion int64  `json:"expectedVersion"`
	AutoPublish     bool   `json:"autoPublish"`
	LiveEnabled     bool   `json:"liveEnabled"`
}
type CaptureStatus struct {
	assetclient.RecordingCaptureStatus
	RecordingVersion int64      `json:"recordingVersion"`
	LiveState        string     `json:"liveState"`
	LiveEnabled      bool       `json:"liveEnabled"`
	AutoPublish      string     `json:"autoPublish"`
	ServerNow        time.Time  `json:"serverNow"`
	StopAcceptedAt   *time.Time `json:"stopAcceptedAt"`
	ReplayUntil      *time.Time `json:"replayUntil"`
}
type CaptureResult struct {
	Capture CaptureStatus                       `json:"capture"`
	Receipt assetclient.RecordingCaptureReceipt `json:"receipt"`
}
type CaptureOperation struct {
	ActorID   string
	Operation string
	Digest    string
	Input     json.RawMessage
	Receipt   *assetclient.RecordingCaptureReceipt
	Pending   bool
}

// ponytail: one locked JSON aggregate, bounded upload/control receipts and 9 MiB pending;
// normalize receipts if measured transaction payload or lock cost warrants it.
type Capture struct {
	RequestedRecoveryVersion string
	RecoveryVersion          string
	SegmentFrames            int
	BroadcastEpoch           int64
	SealedEndSequence        *int
	LiveCover                *CaptureLiveCover
	MediaExpiresAt           *time.Time
	VerifiedReadyAt          *time.Time
	PermissionCheckedAt      *time.Time
	AutoNextAttemptAt        *time.Time
	AutoAttempts             int
	IntentID                 string
	ActorID                  string
	ActorType                string
	RequestID                string
	RecordingID              string
	Create                   CaptureCreateInput
	Generation               int64
	Status                   CaptureStatus
	NormalEnd                bool
	Operations               map[string]CaptureOperation
}

func (c Capture) Terminal() bool {
	return c.Status.State == "failed" || c.Status.State == "expired" || c.Status.State == "aborted"
}
func (c Capture) Active() bool { return !c.Terminal() && c.Status.State != "ready" }
func (c *Capture) Expire(now time.Time) {
	if !c.Terminal() && c.Status.State != "ready" && !(c.NormalEnd && c.Status.Progress.Ended && c.Status.Progress.EndedAt != nil) && !now.Before(c.Status.ExpiresAt) {
		c.Terminate("expired", "capture_expired", c.Status.ExpiresAt)
	}
}
func (c *Capture) Terminate(state, reason string, now time.Time) {
	if c.Terminal() {
		return
	}
	c.Status.State = state
	c.Status.LiveState = state
	c.Status.LiveEnabled = false
	if c.Status.AutoPublish == "pending" {
		c.Status.AutoPublish = "cancelled"
	}
	c.Status.TerminalAt = &now
	c.Status.TerminalReason = &reason
	for key, op := range c.Operations {
		if op.Operation != "create" && op.Operation != "abort" {
			op.Pending = false
			c.Operations[key] = op
		}
	}
	c.QueueAbort("terminal:"+c.IntentID, "capture_incomplete")
}
func (c *Capture) QueueAbort(key, reason string) {
	if _, ok := c.Operations[key]; ok {
		return
	}
	input := assetclient.RecordingCaptureAbortInput{OperationKey: key, ReasonCode: reason}
	raw, _ := json.Marshal(input)
	c.Operations[key] = CaptureOperation{Operation: "abort", Digest: captureDigest(input), Input: raw, Pending: true}
}
func (c *Capture) ClosePublication() {
	c.Status.LiveEnabled = false
	if c.Status.AutoPublish == "pending" {
		c.Status.AutoPublish = "cancelled"
	}
}
func captureDigest(input any) string {
	raw, _ := json.Marshal(input)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

type CaptureRepository interface {
	PrepareCapture(context.Context, Capture, time.Time) (Capture, error)
	GetCapture(context.Context, string, string) (Capture, error)
	UpdateCapture(context.Context, string, func(*Capture) error) (Capture, error)
	ListPendingCaptures(context.Context, time.Time, int) ([]Capture, error)
}

func (s *Service) captureRepository() (CaptureRepository, error) {
	repo, ok := s.repo.(CaptureRepository)
	if !ok {
		return nil, assetclient.ErrUnavailable
	}
	return repo, nil
}
func captureOwner(c Capture, recording, actor string) error {
	if c.RecordingID != recording || c.ActorID != actor {
		return ErrNotFound
	}
	return nil
}
func (s *Service) captureResult(c Capture, key string) (CaptureResult, error) {
	op, ok := c.Operations[key]
	if !ok || op.Receipt == nil {
		return CaptureResult{}, assetclient.ErrUnavailable
	}
	c.Status.ServerNow = s.now().UTC()
	return CaptureResult{c.Status, *op.Receipt}, nil
}
func (s *Service) CreateCapture(ctx context.Context, id, actor string, input CaptureCreateInput, canPublish bool) (CaptureResult, error) {
	if !ValidOperationKey(input.OperationKey) || input.ExpectedVersion < 1 || !auditclient.ValidActorRequest(actor, "capture") || !auditclient.ValidActorRequest(id, "capture") {
		return CaptureResult{}, ErrInvalid
	}
	if (input.AutoPublish || input.LiveEnabled) && !canPublish {
		return CaptureResult{}, ErrForbidden
	}
	repo, err := s.captureRepository()
	if err != nil {
		return CaptureResult{}, err
	}
	p, ok := auditclient.ProvenanceFromContext(ctx)
	if !ok || p.ActorID != actor || p.ActorType != "user" {
		return CaptureResult{}, ErrForbidden
	}
	now := s.now().UTC()
	auto := "cancelled"
	if input.AutoPublish {
		auto = "pending"
	}
	c := Capture{IntentID: platform.NewID(), ActorID: actor, ActorType: p.ActorType, RequestID: p.RequestID, RecordingID: id, Create: input, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{RecordingID: id, State: "creating", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), Objects: []assetclient.RecordingCaptureObject{}, Progress: assetclient.RecordingLiveProgress{LastSequence: -1}}, RecordingVersion: input.ExpectedVersion, LiveState: "starting", LiveEnabled: input.LiveEnabled, AutoPublish: auto}, Operations: map[string]CaptureOperation{}}
	raw, _ := json.Marshal(input)
	c.Operations[input.OperationKey] = CaptureOperation{Operation: "create", Digest: captureDigest(input), Input: raw, Pending: true}
	c, err = repo.PrepareCapture(ctx, c, now)
	if err != nil {
		return CaptureResult{}, err
	}
	if c.Operations[input.OperationKey].Pending {
		c, err = s.deliverCaptureOperation(ctx, c, input.OperationKey)
		if err != nil {
			return CaptureResult{}, err
		}
	}
	c, err = s.expireCapture(ctx, c)
	if err != nil {
		return CaptureResult{}, err
	}
	return s.captureResult(c, input.OperationKey)
}
func (s *Service) ownedCapture(ctx context.Context, id, capture, actor string) (Capture, error) {
	if !packageIDPattern.MatchString(capture) {
		return Capture{}, ErrInvalid
	}
	repo, err := s.captureRepository()
	if err != nil {
		return Capture{}, err
	}
	c, err := repo.GetCapture(ctx, capture, id)
	if err != nil {
		return c, err
	}
	if err = captureOwner(c, id, actor); err != nil {
		return c, err
	}
	return s.expireCapture(ctx, c)
}

// At the deadline, a lost seal reply or stale progress must not turn an
// already normal-ended capture into failure while VOD validation continues.
func (s *Service) expireCapture(ctx context.Context, c Capture) (Capture, error) {
	if c.LiveAvailable(s.now()) {
		c.DeriveLiveState(s.now().UTC())
		return c, nil
	}
	repo, _ := s.captureRepository()
	if !c.Terminal() && !s.now().Before(c.Status.ExpiresAt) && c.Status.State != "ready" {
		for key, op := range c.Operations {
			if op.Operation == "seal" && op.Pending {
				updated, err := s.deliverCaptureOperation(ctx, c, key)
				if err != nil && !errors.Is(err, assetclient.ErrCaptureExpired) && !errors.Is(err, assetclient.ErrConflict) && !errors.Is(err, assetclient.ErrCaptureMissingObjects) {
					return c, err
				}
				if err == nil {
					c = updated
				}
			}
		}
		if c.NormalEnd && !c.Terminal() && !c.Status.Progress.Ended {
			if s.packages == nil {
				return c, assetclient.ErrUnavailable
			}
			ownerCtx := auditclient.WithPrincipalProvenance(ctx, c.ActorType, c.ActorID, c.RequestID)
			value, err := s.packages.GetRecordingCapture(ownerCtx, c.Status.CaptureID, "", 1000)
			if err != nil {
				return c, err
			}
			return repo.UpdateCapture(ctx, c.IntentID, func(current *Capture) error { return mergeCaptureStatus(current, value, s.now().UTC()) })
		}
	}
	return repo.UpdateCapture(ctx, c.IntentID, func(current *Capture) error {
		current.Expire(s.now().UTC())
		current.DeriveLiveState(s.now().UTC())
		return nil
	})
}
func (s *Service) CaptureStatus(ctx context.Context, id, capture, actor, cursor string, limit int) (CaptureStatus, error) {
	if len(cursor) > 128 || limit < 1 || limit > 1000 {
		return CaptureStatus{}, ErrInvalid
	}
	c, err := s.ownedCapture(ctx, id, capture, actor)
	if err != nil {
		return CaptureStatus{}, err
	}
	if !c.Terminal() {
		if s.packages == nil {
			return CaptureStatus{}, assetclient.ErrUnavailable
		}
		value, err := s.packages.GetRecordingCapture(ctx, capture, cursor, limit)
		if err != nil {
			return CaptureStatus{}, err
		}
		repo, _ := s.captureRepository()
		c, err = repo.UpdateCapture(ctx, c.IntentID, func(c *Capture) error { return mergeCaptureStatus(c, value, s.now().UTC()) })
		if err != nil {
			return CaptureStatus{}, err
		}
	}
	c.Status.ServerNow = s.now().UTC()
	return c.Status, nil
}
func mergeCaptureStatus(c *Capture, value assetclient.RecordingCaptureStatus, now time.Time) error {
	if !validLiveProgress(value.Progress, now) {
		return ErrConflict
	}
	if value.CaptureID != c.Status.CaptureID || value.RecordingID != c.RecordingID || (value.PackageID != nil && *value.PackageID != value.CaptureID) {
		return ErrConflict
	}
	if c.NormalEnd && value.Progress.Ended && value.Progress.EndedAt != nil && !value.Progress.EndedAt.After(c.Status.ExpiresAt) && value.Progress.Revision >= c.Status.Progress.Revision {
		c.Status.Progress = value.Progress
	}
	c.Expire(now)
	if c.Terminal() {
		return nil
	}
	if value.State == "failed" || value.State == "expired" || value.State == "aborted" {
		reason := "capture_incomplete"
		if value.TerminalReason != nil {
			reason = *value.TerminalReason
		}
		c.Terminate(value.State, reason, now)
		return nil
	}
	// A delayed reply cannot roll back a sealed or verified capture.
	rank := map[string]int{"creating": 0, "uploading": 1, "freezing": 2, "validating": 3, "ready": 4}
	if rank[value.State] < rank[c.Status.State] {
		return nil
	}
	if value.State == "ready" && !c.NormalEnd {
		return ErrConflict
	}
	if value.Progress.Revision < c.Status.Progress.Revision {
		return nil
	}
	value.CreatedAt = c.Status.CreatedAt
	value.ExpiresAt = c.Status.ExpiresAt
	c.Status.RecordingCaptureStatus = value
	if c.Status.StopAcceptedAt != nil && c.Status.LiveState != "aborted" {
		c.Status.LiveState = "ending"
	}
	if value.Progress.Ended && c.NormalEnd && c.Status.StopAcceptedAt != nil && value.Progress.EndedAt != nil && c.Status.LiveState != "aborted" {
		c.Status.LiveState = "ended"
		until := value.Progress.EndedAt.Add(time.Duration(value.Progress.MediaEndSeconds*float64(time.Second)) + 30*time.Minute)
		if until.After(c.Status.ExpiresAt) {
			until = c.Status.ExpiresAt
		}
		c.Status.ReplayUntil = &until
	}
	c.DeriveLiveState(now)
	return nil
}
func captureControlOperation(operation string) bool {
	return operation == "stop" || operation == "abort" || operation == "cancel_auto_publish" || operation == "close_live"
}
func (s *Service) mutateCapture(ctx context.Context, id, capture, actor, key, operation string, input any, canPublish ...bool) (CaptureResult, error) {
	if !ValidOperationKey(key) {
		return CaptureResult{}, ErrInvalid
	}
	if !packageIDPattern.MatchString(capture) {
		return CaptureResult{}, ErrInvalid
	}
	repo, err := s.captureRepository()
	if err != nil {
		return CaptureResult{}, err
	}
	c, err := repo.GetCapture(ctx, capture, id)
	if err != nil {
		return CaptureResult{}, err
	}
	controlStaff := (operation == "close_live" || operation == "cancel_auto_publish") && len(canPublish) == 1 && canPublish[0]
	p, hasProvenance := auditclient.ProvenanceFromContext(ctx)
	controlStaff = controlStaff && hasProvenance && p.ActorType == "user" && p.ActorID == actor
	if c.ActorID != actor && !controlStaff {
		return CaptureResult{}, ErrNotFound
	}
	c, err = s.expireCapture(ctx, c)
	if err != nil {
		return CaptureResult{}, err
	}
	raw, _ := json.Marshal(input)
	digest := captureDigest(input)
	c, err = repo.UpdateCapture(ctx, c.IntentID, func(c *Capture) error {
		if c.RecordingID != id || c.ActorID != actor && !controlStaff {
			return ErrNotFound
		}
		if old, ok := c.Operations[key]; ok {
			receiptActor := old.ActorID
			if receiptActor == "" {
				receiptActor = c.ActorID
			}
			if old.Digest != digest || old.Operation != operation || receiptActor != actor {
				return ErrConflict
			}
			return nil
		}
		local := captureControlOperation(operation)
		count, limit := 0, 2*13_000+2 // create, per-object declare/confirm, seal; matches Asset receipt capacity.
		if local {
			limit = 32
		}
		for _, old := range c.Operations {
			if local && old.Operation == operation || !local && !captureControlOperation(old.Operation) {
				count++
			}
		}
		if count >= limit {
			return ErrConflict
		}
		c.Expire(s.now().UTC())
		if c.Terminal() {
			return assetclient.ErrCaptureExpired
		}
		if operation == "seal" && c.Status.StopAcceptedAt == nil {
			return ErrConflict
		}
		if operation == "abort" && c.Status.State == "ready" {
			return ErrConflict
		}
		if !local && c.Status.State != "uploading" {
			return ErrConflict
		}
		now := s.now().UTC()
		op := CaptureOperation{ActorID: actor, Operation: operation, Digest: digest, Input: raw, Pending: !local || operation == "abort"}
		switch operation {
		case "stop":
			if c.Status.StopAcceptedAt == nil {
				c.Status.StopAcceptedAt = &now
			}
			c.Status.LiveState = "ending"
		case "cancel_auto_publish":
			if c.Status.AutoPublish != "published" {
				c.Status.AutoPublish = "cancelled"
			}
		case "close_live":
			c.Status.LiveEnabled = false
		case "abort":
			var value assetclient.RecordingCaptureAbortInput
			json.Unmarshal(raw, &value)
			c.Terminate("aborted", value.ReasonCode, now)
			delete(c.Operations, "terminal:"+c.IntentID)
		}
		if local {
			if operation != "abort" {
				op.Input = nil
			}
			op.Receipt = &assetclient.RecordingCaptureReceipt{OperationKey: key, Operation: operation, AcceptedAt: now, CaptureID: c.Status.CaptureID}
		}
		pendingBytes := len(op.Input)
		for _, old := range c.Operations {
			if old.Pending {
				pendingBytes += len(old.Input)
			}
		}
		if pendingBytes > 9<<20 {
			return ErrConflict
		}
		c.Operations[key] = op
		return nil
	})
	if err != nil {
		return CaptureResult{}, err
	}
	op := c.Operations[key]
	if op.Pending {
		updated, err := s.deliverCaptureOperation(ctx, c, key)
		if err != nil && op.Receipt == nil {
			return CaptureResult{}, err
		}
		if err == nil {
			c = updated
		}
	}
	result, err := s.captureResult(c, key)
	if c.ActorID != actor {
		result.Capture.Objects = []assetclient.RecordingCaptureObject{}
		result.Capture.NextCursor = ""
	}
	return result, err
}
func (s *Service) StopCapture(ctx context.Context, id, capture, actor, key string) (CaptureResult, error) {
	return s.mutateCapture(ctx, id, capture, actor, key, "stop", struct {
		OperationKey string `json:"operationKey"`
	}{key})
}
func (s *Service) CancelCaptureAutoPublish(ctx context.Context, id, capture, actor, key string, canPublish ...bool) (CaptureResult, error) {
	return s.mutateCapture(ctx, id, capture, actor, key, "cancel_auto_publish", struct {
		OperationKey string `json:"operationKey"`
	}{key}, canPublish...)
}
func (s *Service) CloseCaptureLive(ctx context.Context, id, capture, actor, key string, canPublish ...bool) (CaptureResult, error) {
	return s.mutateCapture(ctx, id, capture, actor, key, "close_live", struct {
		OperationKey string `json:"operationKey"`
	}{key}, canPublish...)
}
func (s *Service) DeclareCapture(ctx context.Context, id, capture, actor string, input assetclient.RecordingCaptureDeclareInput) (CaptureResult, error) {
	if len(input.Objects) < 1 || len(input.Objects) > 100 {
		return CaptureResult{}, ErrInvalid
	}
	input.Objects = slices.Clone(input.Objects)
	slices.SortFunc(input.Objects, func(a, b assetclient.RecordingPackageObject) int {
		if a.Path < b.Path {
			return -1
		}
		if a.Path > b.Path {
			return 1
		}
		return 0
	})
	for i, o := range input.Objects {
		if !capturePathPattern.MatchString(o.Path) || o.SizeBytes < 1 || o.SizeBytes > 128<<20 || !captureHashPattern.MatchString(o.SHA256) || (i > 0 && input.Objects[i-1].Path == o.Path) {
			return CaptureResult{}, ErrInvalid
		}
	}
	return s.mutateCapture(ctx, id, capture, actor, input.OperationKey, "declare", input)
}
func validCapturePaths(paths []string) bool {
	if len(paths) < 1 || len(paths) > 100 {
		return false
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if !capturePathPattern.MatchString(p) || seen[p] {
			return false
		}
		seen[p] = true
	}
	return true
}
func (s *Service) ConfirmCapture(ctx context.Context, id, capture, actor string, input assetclient.RecordingCaptureConfirmInput) (CaptureResult, error) {
	if !validCapturePaths(input.Paths) {
		return CaptureResult{}, ErrInvalid
	}
	input.Paths = slices.Clone(input.Paths)
	slices.Sort(input.Paths)
	return s.mutateCapture(ctx, id, capture, actor, input.OperationKey, "confirm", input)
}
func (s *Service) SealCapture(ctx context.Context, id, capture, actor string, input assetclient.RecordingCaptureSealInput) (CaptureResult, error) {
	if !input.NormalEnd || len(input.Inventory.Renditions) != 3 || input.Inventory.InventoryDigest == "" {
		return CaptureResult{}, ErrInvalid
	}
	return s.mutateCapture(ctx, id, capture, actor, input.OperationKey, "seal", input)
}
func (s *Service) AbortCapture(ctx context.Context, id, capture, actor string, input assetclient.RecordingCaptureAbortInput) (CaptureResult, error) {
	switch input.ReasonCode {
	case "user_abort", "disk_limit", "encoder_failure", "capture_incomplete":
	default:
		return CaptureResult{}, ErrInvalid
	}
	return s.mutateCapture(ctx, id, capture, actor, input.OperationKey, "abort", input)
}
func (s *Service) SignCapture(ctx context.Context, id, capture, actor string, paths []string) ([]assetclient.SignedRecordingObject, error) {
	if !validCapturePaths(paths) {
		return nil, ErrInvalid
	}
	c, err := s.ownedCapture(ctx, id, capture, actor)
	if err != nil {
		return nil, err
	}
	if c.Terminal() {
		return nil, assetclient.ErrCaptureExpired
	}
	if c.Status.State != "uploading" {
		return nil, ErrConflict
	}
	if s.packages == nil {
		return nil, assetclient.ErrUnavailable
	}
	signed, err := s.packages.SignRecordingCapture(ctx, capture, paths)
	if err != nil {
		return nil, err
	}
	current, err := s.ownedCapture(ctx, id, capture, actor)
	if err != nil {
		return nil, err
	}
	if current.Terminal() || current.Status.State != "uploading" {
		return nil, ErrConflict
	}
	return signed, nil
}
func (s *Service) deliverCaptureOperation(ctx context.Context, c Capture, key string) (Capture, error) {
	if s.packages == nil {
		return c, assetclient.ErrUnavailable
	}
	repo, _ := s.captureRepository()
	op := c.Operations[key]
	if !op.Pending {
		return c, nil
	}
	// Persisted identity only, never a saved access token.
	ctx = auditclient.WithPrincipalProvenance(ctx, c.ActorType, c.ActorID, c.RequestID)
	var result assetclient.RecordingCaptureResult
	var err error
	switch op.Operation {
	case "create":
		if c.BroadcastEpoch > 0 {
			result, err = s.packages.CreateRecordingBroadcastCapture(ctx, c.RecordingID, key, c.BroadcastEpoch, c.SegmentFrames, c.RecoveryVersion)
		} else {
			result, err = s.packages.CreateRecordingCapture(ctx, c.RecordingID, key)
		}
	case "declare":
		var input assetclient.RecordingCaptureDeclareInput
		json.Unmarshal(op.Input, &input)
		result, err = s.packages.DeclareRecordingCapture(ctx, c.Status.CaptureID, input)
	case "confirm":
		var input assetclient.RecordingCaptureConfirmInput
		json.Unmarshal(op.Input, &input)
		result, err = s.packages.ConfirmRecordingCapture(ctx, c.Status.CaptureID, input)
	case "seal":
		var input assetclient.RecordingCaptureSealInput
		json.Unmarshal(op.Input, &input)
		result, err = s.packages.SealRecordingCapture(ctx, c.Status.CaptureID, input)
	case "abort":
		if c.Status.CaptureID == "" {
			return c, assetclient.ErrUnavailable
		}
		var input assetclient.RecordingCaptureAbortInput
		json.Unmarshal(op.Input, &input)
		result, err = s.packages.AbortRecordingCapture(ctx, c.Status.CaptureID, input)
	default:
		return c, ErrInvalid
	}
	if op.Operation == "abort" && errors.Is(err, assetclient.ErrConflict) {
		terminal, lookupErr := s.packages.GetRecordingCapture(ctx, c.Status.CaptureID, "", 1)
		if lookupErr == nil && terminal.CaptureID == c.Status.CaptureID && terminal.RecordingID == c.RecordingID {
			if terminal.State == "ready" {
				if terminal.PackageID != nil && *terminal.PackageID != terminal.CaptureID {
					return c, ErrConflict
				}
				return repo.UpdateCapture(ctx, c.IntentID, func(current *Capture) error {
					saved := current.Operations[key]
					if saved.Digest != op.Digest {
						return ErrConflict
					}
					// Ready bytes win the producer race; accepted CMS abort still
					// closes exposure and publication, without an Asset abort receipt.
					var requested assetclient.RecordingCaptureAbortInput
					json.Unmarshal(op.Input, &requested)
					if current.Status.State == "aborted" && saved.Receipt != nil && current.Status.TerminalReason != nil && *current.Status.TerminalReason == requested.ReasonCode {
						terminal.CreatedAt = current.Status.CreatedAt
						terminal.ExpiresAt = current.Status.ExpiresAt
						current.Status.RecordingCaptureStatus = terminal
						current.Status.LiveState = "aborted"
						current.ClosePublication()
					}
					saved.Pending = false
					saved.Input = nil
					current.Operations[key] = saved
					return nil
				})
			}
			if terminal.State == "failed" || terminal.State == "expired" || terminal.State == "aborted" {
				err = assetclient.ErrCaptureExpired
			}
		}
	}
	if op.Operation == "abort" && errors.Is(err, assetclient.ErrCaptureExpired) {
		return repo.UpdateCapture(ctx, c.IntentID, func(current *Capture) error {
			saved := current.Operations[key]
			saved.Pending = false
			current.Operations[key] = saved
			return nil
		})
	}
	if err != nil {
		return c, err
	}
	if result.Capture.RecordingID != c.RecordingID || !packageIDPattern.MatchString(result.Capture.CaptureID) || result.Receipt.OperationKey != key || result.Receipt.Operation != op.Operation || result.Receipt.CaptureID != result.Capture.CaptureID {
		return c, ErrConflict
	}
	return repo.UpdateCapture(ctx, c.IntentID, func(current *Capture) error {
		saved := current.Operations[key]
		if saved.Digest != op.Digest {
			return ErrConflict
		}
		if op.Operation == "seal" && !current.Terminal() {
			current.NormalEnd = true
			if current.BroadcastEpoch > 0 {
				var sealed assetclient.RecordingCaptureSealInput
				if json.Unmarshal(op.Input, &sealed) != nil || len(sealed.Inventory.Renditions) != 3 {
					return ErrConflict
				}
				end := sealed.Inventory.Renditions[0].SegmentCount
				for _, rendition := range sealed.Inventory.Renditions {
					if rendition.SegmentCount != end || end < 1 {
						return ErrConflict
					}
				}
				current.SealedEndSequence = &end
			}
		}
		if current.NormalEnd && result.Capture.Progress.Ended && result.Capture.Progress.EndedAt != nil && !result.Capture.Progress.EndedAt.After(current.Status.ExpiresAt) {
			current.Status.Progress = result.Capture.Progress
		}
		current.Expire(s.now().UTC())
		if op.Operation == "create" {
			if current.Status.CaptureID != "" && current.Status.CaptureID != result.Capture.CaptureID {
				return ErrConflict
			}
			current.Status.CaptureID = result.Capture.CaptureID
			if result.Capture.ExpiresAt.Before(current.Status.ExpiresAt) {
				current.Status.ExpiresAt = result.Capture.ExpiresAt
			}
		}
		if !current.Terminal() {
			if op.Operation == "seal" {
				current.NormalEnd = true
			}
			if err := mergeCaptureStatus(current, result.Capture, s.now().UTC()); err != nil {
				return err
			}
		}
		saved.Pending = false
		saved.Input = nil
		if saved.Receipt == nil {
			receipt := result.Receipt
			saved.Receipt = &receipt
		}
		current.Operations[key] = saved
		return nil
	})
}
func (s *Service) ReconcileCaptures(ctx context.Context, limit int) error {
	repo, err := s.captureRepository()
	if err != nil {
		return err
	}
	values, err := repo.ListPendingCaptures(ctx, s.now().UTC(), limit)
	if err != nil {
		return err
	}
	var failures error
	for _, c := range values {
		c, err = s.expireCapture(ctx, c)
		if err != nil {
			failures = errors.Join(failures, err)
			continue
		}
		keys := make([]string, 0, len(c.Operations))
		for key, op := range c.Operations {
			if op.Pending {
				keys = append(keys, key)
			}
		}
		slices.Sort(keys)
		// The original create must be recovered before its compensating abort.
		if op := c.Operations[c.Create.OperationKey]; op.Pending {
			keys = slices.DeleteFunc(keys, func(k string) bool { return k == c.Create.OperationKey })
			keys = append([]string{c.Create.OperationKey}, keys...)
		}
		for _, key := range keys {
			c, err = s.deliverCaptureOperation(ctx, c, key)
			if err != nil {
				failures = errors.Join(failures, err)
			}
		}
		if !c.Terminal() && (c.Status.State != "ready" || c.Status.LiveEnabled) && c.Status.CaptureID != "" && s.packages != nil {
			ownerCtx := auditclient.WithPrincipalProvenance(ctx, c.ActorType, c.ActorID, c.RequestID)
			value, readErr := s.packages.GetRecordingCapture(ownerCtx, c.Status.CaptureID, "", 1000)
			if readErr == nil {
				_, readErr = repo.UpdateCapture(ctx, c.IntentID, func(current *Capture) error { return mergeCaptureStatus(current, value, s.now().UTC()) })
			}
			failures = errors.Join(failures, readErr)
		}
	}
	return failures
}
