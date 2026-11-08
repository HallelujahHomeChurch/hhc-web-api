package recordings

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

type LiveCoverSelection struct {
	Mode     string `json:"mode"`
	UploadID string `json:"uploadId,omitempty"`
}

func (v LiveCoverSelection) Valid() bool {
	return v.Mode == "auto" && v.UploadID == "" || v.Mode == "custom" && assetclient.RecordingCoverID.MatchString(v.UploadID)
}

type LiveCoverSettings struct {
	Revision          int64              `json:"revision"`
	Selection         LiveCoverSelection `json:"selection"`
	CoverState        string             `json:"coverState"`
	CoverID           string             `json:"coverId,omitempty"`
	ContentPath       string             `json:"contentPath,omitempty"`
	InheritedRevision int64              `json:"inheritedRevision,omitempty"`
}

// This derivative aggregate is private CMS state, outside the OBS wire contract.
type CaptureLiveCover struct {
	Scope                                                 string
	Settings                                              LiveCoverSettings
	AutoUploadID, AutoState, ReferenceID, AutoReferenceID string
	PromotionID                                           string
	PromotionRevision                                     int64
	PromotionReady                                        bool
	AutoRefreshAt                                         time.Time
	AutoFrozen                                            bool
}

// End freezes new generations, not completion of the first already-created image.
func (c CaptureLiveCover) PendingFirstAutoCover() bool {
	return c.AutoFrozen && !c.PromotionReady && c.Settings.Selection.Mode == "auto" && c.Settings.CoverID == "" && c.AutoUploadID != "" && (c.AutoState == "pending" || c.AutoState == "processing" || c.AutoState == "ready")
}

type LiveCoverOperation struct {
	ID, Target, Actor, Key string
	Expected               int64
	Selection              LiveCoverSelection
	Receipt                *LiveCoverSettings
}
type LiveCoverReference struct {
	ID, Target, CoverID, Scope, RecordingID string
	Wanted, Applied                         bool
}
type LiveCoverRepository interface {
	GetLiveCoverSettings(context.Context, string) (CaptureLiveCover, error)
	PrepareLiveCoverOperation(context.Context, LiveCoverOperation, time.Time) (LiveCoverOperation, error)
	CommitLiveCoverOperation(context.Context, LiveCoverOperation, time.Time, func(context.Context) error) (LiveCoverSettings, error)
	EnsureLiveCoverReference(context.Context, LiveCoverReference) error
	ApplyLiveCoverReference(context.Context, LiveCoverReference, func(context.Context) error) error
	ReconcileLiveCoverReferences(context.Context, time.Time, func(context.Context, LiveCoverReference) error) error
	ListLiveCoverCaptures(context.Context, time.Time, int) ([]Capture, error)
	InheritLiveCover(context.Context, Capture, string, time.Time, func(context.Context) error) error
	ApplyLiveAutoCover(context.Context, Capture, assetclient.RecordingCoverUpload, func(context.Context) error) error
}

func (s *Service) liveCoverRepository() (LiveCoverRepository, error) {
	r, ok := s.repo.(LiveCoverRepository)
	if !ok {
		return nil, assetclient.ErrUnavailable
	}
	return r, nil
}
func (s *Service) liveCoverTarget(ctx context.Context, id, capture string) (string, string, error) {
	if id == "" && capture == "" {
		return "defaults", "", nil
	}
	if !validLiveUUID(id) || !packageIDPattern.MatchString(capture) {
		return "", "", ErrNotFound
	}
	repo, err := s.captureRepository()
	if err != nil {
		return "", "", err
	}
	c, err := repo.GetCapture(ctx, capture, id)
	if err != nil {
		return "", "", err
	}
	c, err = s.expireCapture(ctx, c)
	if err != nil {
		return "", "", err
	}
	v, err := s.repo.Get(ctx, id)
	if err != nil {
		return "", "", err
	}
	if c.Terminal() || v.Expired(s.now()) || c.LiveCover == nil || !c.Create.LiveEnabled {
		return "", "", ErrNotFound
	}
	return c.IntentID, c.Status.CaptureID, nil
}
func liveCoverPath(id, capture string) string {
	if id == "" {
		return "/api/admin/recordings/live-cover-settings"
	}
	return "/api/admin/recordings/" + id + "/captures/" + capture + "/cover-settings"
}
func (s *Service) GetLiveCoverSettings(ctx context.Context, id, capture string) (LiveCoverSettings, error) {
	target, _, err := s.liveCoverTarget(ctx, id, capture)
	if err != nil {
		return LiveCoverSettings{}, err
	}
	r, err := s.liveCoverRepository()
	if err != nil {
		return LiveCoverSettings{}, err
	}
	c, err := r.GetLiveCoverSettings(ctx, target)
	v := c.Settings
	if v.CoverState == "ready" {
		if id == "" {
			v.ContentPath = liveCoverPath(id, capture) + "/uploads/" + v.CoverID + "/content"
		} else {
			v.ContentPath = liveCoverPath(id, capture) + "/content"
		}
	}
	return v, err
}
func (s *Service) SetLiveCoverSettings(ctx context.Context, id, capture string, expected int64, input LiveCoverSelection, actor, key string) (LiveCoverSettings, error) {
	if expected < 1 || !input.Valid() || actor == "" || !ValidOperationKey(key) {
		return LiveCoverSettings{}, ErrInvalid
	}
	target, scope, err := s.liveCoverTarget(ctx, id, capture)
	if err != nil {
		return LiveCoverSettings{}, err
	}
	if target == "defaults" {
		scope = "defaults"
	}
	repo, err := s.liveCoverRepository()
	if err != nil {
		return LiveCoverSettings{}, err
	}
	if s.packages == nil {
		return LiveCoverSettings{}, assetclient.ErrUnavailable
	}
	op, err := repo.PrepareLiveCoverOperation(ctx, LiveCoverOperation{ID: platform.NewID(), Target: target, Actor: actor, Key: key, Expected: expected, Selection: input}, s.now())
	if err != nil {
		return LiveCoverSettings{}, err
	}
	if op.Receipt != nil {
		return *op.Receipt, nil
	}
	retain := func(context.Context) error { return nil }
	if input.Mode == "custom" {
		upload, err := s.packages.GetLiveCoverUpload(ctx, scope, id, input.UploadID)
		if err != nil {
			return LiveCoverSettings{}, err
		}
		if upload.State != "ready" {
			return LiveCoverSettings{}, ErrConflict
		}
		retain = func(ctx context.Context) error {
			return s.packages.RetainLiveCover(ctx, scope, id, input.UploadID, op.ID)
		}
	}
	return repo.CommitLiveCoverOperation(ctx, op, s.now(), retain)
}
func (s *Service) UploadLiveCover(ctx context.Context, id, capture, mime, key string, body io.Reader) (assetclient.RecordingCoverUpload, error) {
	if !ValidOperationKey(key) || mime != "image/jpeg" && mime != "image/png" {
		return assetclient.RecordingCoverUpload{}, ErrInvalid
	}
	_, scope, err := s.liveCoverTarget(ctx, id, capture)
	if err != nil {
		return assetclient.RecordingCoverUpload{}, err
	}
	if id == "" {
		scope = "defaults"
	}
	if s.packages == nil {
		return assetclient.RecordingCoverUpload{}, assetclient.ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(body, 5<<20+1))
	if err != nil || len(data) > 5<<20 {
		return assetclient.RecordingCoverUpload{}, assetclient.ErrCoverTooLarge
	}
	return s.packages.UploadLiveCover(ctx, scope, id, mime, key, data)
}
func (s *Service) GetLiveCoverUpload(ctx context.Context, id, capture, upload string) (assetclient.RecordingCoverUpload, error) {
	_, scope, err := s.liveCoverTarget(ctx, id, capture)
	if err != nil {
		return assetclient.RecordingCoverUpload{}, err
	}
	if id == "" {
		scope = "defaults"
	}
	if s.packages == nil {
		return assetclient.RecordingCoverUpload{}, assetclient.ErrUnavailable
	}
	return s.packages.GetLiveCoverUpload(ctx, scope, id, upload)
}

// MemberLiveCoverContent keeps B1 reads on broadcast ownership without enabling
// the legacy capture-scoped administrator writes.
func (s *Service) MemberLiveCoverContent(ctx context.Context, id, capture, user string) ([]byte, error) {
	if !validLiveUUID(id) || !packageIDPattern.MatchString(capture) || !validLiveUUID(user) {
		return nil, ErrNotFound
	}
	captures, err := s.captureRepository()
	if err != nil {
		return nil, err
	}
	c, err := captures.GetCapture(ctx, capture, id)
	if err != nil {
		return nil, err
	}
	if c.BroadcastEpoch == 0 {
		return s.LiveCoverContent(ctx, id, capture, "", true)
	}
	broadcasts, err := s.broadcastRepository()
	if err != nil {
		return nil, err
	}
	current := func() (Broadcast, error) {
		b, err := broadcasts.GetBroadcast(ctx, id)
		if err != nil {
			return b, err
		}
		r, err := s.repo.Get(ctx, id)
		if err != nil {
			return b, err
		}
		c, err := captures.GetCapture(ctx, capture, id)
		if err != nil {
			return b, err
		}
		if !b.View.Announced || b.Revoked || r.Expired(s.now()) || b.View.Binding == nil || b.View.Binding.CaptureID != capture || b.Epoch != c.BroadcastEpoch || (b.View.Phase != "live" && b.View.Phase != "end_pending") || !c.LiveAvailable(s.now()) || c.Status.StopAcceptedAt != nil {
			return b, ErrNotFound
		}
		return b, nil
	}
	before, err := current()
	if err != nil {
		return nil, err
	}
	data, err := s.BroadcastCoverContent(ctx, id, "", user, true)
	if err != nil {
		return nil, err
	}
	after, err := current()
	if err != nil {
		return nil, err
	}
	// A closure or cover replacement during Asset I/O must discard fetched bytes.
	if before.Epoch != after.Epoch || before.CoverSnapshot.Scope != after.CoverSnapshot.Scope || before.CoverSnapshot.Settings != after.CoverSnapshot.Settings {
		return nil, ErrNotFound
	}
	return data, nil
}

func (s *Service) LiveCoverContent(ctx context.Context, id, capture, upload string, member bool) ([]byte, error) {
	target, scope, err := s.liveCoverTarget(ctx, id, capture)
	if err != nil {
		return nil, err
	}
	repo, err := s.liveCoverRepository()
	if err != nil {
		return nil, err
	}
	if s.packages == nil {
		return nil, assetclient.ErrUnavailable
	}
	if member {
		if id == "" {
			return nil, ErrNotFound
		}
		r, _ := s.captureRepository()
		c, err := r.GetCapture(ctx, capture, id)
		if err != nil {
			return nil, err
		}
		if !c.LiveAvailable(s.now()) || c.Status.StopAcceptedAt != nil {
			return nil, ErrNotFound
		}
	}
	if upload == "" {
		v, err := repo.GetLiveCoverSettings(ctx, target)
		if err != nil {
			return nil, err
		}
		if v.Settings.CoverState != "ready" || v.Settings.CoverID == "" {
			return nil, ErrNotFound
		}
		upload = v.Settings.CoverID
	}
	if target == "defaults" {
		scope = "defaults"
	}
	data, err := s.packages.ReadLiveCover(ctx, scope, id, upload)
	if err != nil || !member {
		return data, err
	}
	// Asset I/O can outlive a stop, closure or selection change.
	if _, _, err = s.liveCoverTarget(ctx, id, capture); err != nil {
		return nil, err
	}
	captureRepo, _ := s.captureRepository()
	current, err := captureRepo.GetCapture(ctx, capture, id)
	if err != nil {
		return nil, err
	}
	if !current.LiveAvailable(s.now()) || current.Status.StopAcceptedAt != nil || current.LiveCover.Settings.CoverState != "ready" || current.LiveCover.Settings.CoverID != upload {
		return nil, ErrNotFound
	}
	return data, nil
}

func (s *Service) ReconcileLiveCovers(ctx context.Context, limit int) error {
	repo, err := s.liveCoverRepository()
	if err != nil {
		return err
	}
	captures, err := repo.ListLiveCoverCaptures(ctx, s.now(), limit)
	if err != nil {
		return err
	}
	var failures error
	if studio, ok := s.repo.(interface {
		RetainStudioCovers(context.Context, time.Time, int, func(context.Context, LiveCoverReference) error) error
	}); ok && s.packages != nil {
		failures = studio.RetainStudioCovers(ctx, s.now(), limit, func(ctx context.Context, ref LiveCoverReference) error {
			return s.packages.RetainLiveCover(ctx, ref.Scope, ref.RecordingID, ref.CoverID, ref.ID)
		})
	}
	for _, c := range captures {
		if err := s.reconcileCaptureLiveCover(ctx, c); err != nil {
			failures = errors.Join(failures, err)
		}
	}
	err = repo.ReconcileLiveCoverReferences(ctx, s.now(), func(ctx context.Context, ref LiveCoverReference) error {
		return s.packages.ReleaseLiveCover(ctx, ref.Scope, ref.RecordingID, ref.CoverID, ref.ID)
	})
	return errors.Join(failures, err)
}
func (s *Service) reconcileCaptureLiveCover(ctx context.Context, c Capture) error {
	if c.LiveCover == nil || c.LiveCover.PromotionReady || c.Status.CaptureID == "" {
		return nil
	}
	if s.packages == nil {
		return assetclient.ErrUnavailable
	}
	repo, _ := s.liveCoverRepository()
	captureRepo, _ := s.captureRepository()
	c, err := s.expireCapture(ctx, c)
	if err != nil {
		return err
	}
	if c.Terminal() {
		return nil
	}
	owner := auditclient.WithPrincipalProvenance(ctx, c.ActorType, c.ActorID, c.RequestID)
	cover := *c.LiveCover
	scope := c.Status.CaptureID
	ownerID := c.RecordingID
	target := c.IntentID
	if c.BroadcastEpoch > 0 && cover.Settings.Selection.Mode == "custom" {
		scope = cover.Scope
		target = "broadcast:" + c.RecordingID
		if scope == "defaults" {
			ownerID = ""
		}
	}
	if cover.Settings.Selection.Mode == "custom" && cover.Settings.CoverState != "ready" {
		ref := LiveCoverReference{ID: cover.ReferenceID, Target: target, CoverID: cover.Settings.Selection.UploadID, Scope: scope, RecordingID: ownerID, Wanted: true}
		if err = repo.ApplyLiveCoverReference(owner, ref, func(ctx context.Context) error {
			return s.packages.RetainLiveCover(ctx, scope, ownerID, ref.CoverID, ref.ID)
		}); err != nil {
			return err
		}
		_, err = captureRepo.UpdateCapture(ctx, c.IntentID, func(current *Capture) error {
			if current.LiveCover != nil && current.LiveCover.ReferenceID == ref.ID && !current.Terminal() {
				current.LiveCover.Settings.CoverState = "ready"
				current.LiveCover.Settings.CoverID = ref.CoverID
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	// Already-ended captures can predate the refresh fence; preserve their selected image.
	if c.NormalEnd && c.Status.State == "ready" && cover.Settings.CoverState == "ready" {
		return s.promoteLiveCover(owner, c)
	}
	if cover.Settings.Selection.Mode == "auto" && (!cover.AutoFrozen || cover.PendingFirstAutoCover()) {
		if c.BroadcastEpoch > 0 && !cover.PendingFirstAutoCover() {
			br, e := s.broadcastRepository()
			if e != nil {
				return e
			}
			b, e := br.GetBroadcast(ctx, c.RecordingID)
			if e != nil {
				return e
			}
			if b.Revoked || b.RangePending || b.View.Boundaries.StartSequence == nil || b.View.Boundaries.EndSequenceExclusive != nil || b.View.Phase != "live" || b.Projection == nil || b.Projection.State != "ready" {
				return nil
			}
		}
		upload := assetclient.RecordingCoverUpload{UploadID: cover.AutoUploadID, State: cover.AutoState}
		refresh := !cover.AutoFrozen && upload.UploadID != "" && c.Status.StopAcceptedAt == nil && c.Status.State == "uploading" && (upload.State == "ready" || upload.State == "failed" || upload.State == "expired") && !s.now().Before(cover.AutoRefreshAt.Add(5*time.Minute))
		if upload.UploadID == "" || refresh {
			if refresh {
				upload, err = s.packages.RefreshLiveAutoCover(owner, scope, c.RecordingID)
			} else {
				upload, err = s.packages.CreateLiveAutoCover(owner, scope, c.RecordingID)
			}
			if err != nil {
				return err
			}
			c, err = captureRepo.UpdateCapture(ctx, c.IntentID, func(current *Capture) error {
				if current.LiveCover == nil || current.Terminal() || current.LiveCover.AutoFrozen || current.LiveCover.Settings.Selection.Mode != "auto" || current.LiveCover.Settings.Revision != cover.Settings.Revision || current.LiveCover.AutoUploadID != cover.AutoUploadID || refresh && current.Status.StopAcceptedAt != nil {
					return nil
				}
				if current.LiveCover.AutoUploadID != upload.UploadID && current.LiveCover.AutoUploadID != "" {
					current.LiveCover.AutoReferenceID = platform.NewID()
				}
				current.LiveCover.AutoUploadID = upload.UploadID
				current.LiveCover.AutoState = upload.State
				current.LiveCover.AutoRefreshAt = s.now()
				return nil
			})
			if err != nil {
				return err
			}
			if c.LiveCover == nil || c.LiveCover.AutoUploadID != upload.UploadID || c.LiveCover.AutoFrozen {
				return nil
			}
		} else {
			upload, err = s.packages.GetLiveCoverUpload(owner, scope, c.RecordingID, upload.UploadID)
			if cover.PendingFirstAutoCover() && errors.Is(err, assetclient.ErrNotFound) {
				// Asset no longer has this first image; retain the existing VOD fallback.
				upload = assetclient.RecordingCoverUpload{UploadID: cover.AutoUploadID, State: "failed"}
				err = nil
			}
			if err != nil {
				return err
			}
		}
		if err = repo.ApplyLiveAutoCover(owner, c, upload, func(ctx context.Context) error {
			return s.packages.RetainLiveCover(ctx, scope, c.RecordingID, upload.UploadID, c.LiveCover.AutoReferenceID)
		}); err != nil {
			return err
		}
	}
	c, err = captureRepo.GetCapture(ctx, c.IntentID, c.RecordingID)
	if err != nil {
		return err
	}
	if c.NormalEnd && c.Status.State == "ready" {
		return s.promoteLiveCover(owner, c)
	}
	return nil
}
func (s *Service) promoteLiveCover(ctx context.Context, c Capture) error {
	if c.LiveCover == nil || c.LiveCover.PromotionReady {
		return nil
	}
	value, err := s.repo.Get(ctx, c.RecordingID)
	if err != nil {
		return err
	}
	if value.SelectedCoverID != "" && value.SelectedCoverID != c.LiveCover.PromotionID {
		repo, _ := s.captureRepository()
		_, err = repo.UpdateCapture(ctx, c.IntentID, func(current *Capture) error {
			if current.LiveCover != nil {
				current.LiveCover.PromotionReady = true
			}
			return nil
		})
		return err
	}
	cover := c.LiveCover
	// A failed/never-produced automatic cover retains the existing three VOD samples.
	if cover.Settings.CoverState != "ready" || cover.Settings.CoverID == "" {
		if cover.Settings.Selection.Mode == "custom" || !cover.AutoFrozen && cover.AutoState != "failed" && cover.AutoState != "expired" {
			return ErrConflict
		}
		return nil
	}
	var promoted assetclient.LiveCoverPromotion
	if c.BroadcastEpoch > 0 && cover.Settings.Selection.Mode == "custom" {
		ownerID := c.RecordingID
		if cover.Scope == "defaults" {
			ownerID = ""
		}
		promoted, err = s.packages.PromoteBroadcastLiveCover(ctx, cover.Scope, ownerID, cover.Settings.CoverID, c.Status.CaptureID, c.RecordingID)
	} else {
		promoted, err = s.packages.PromoteLiveCover(ctx, c.Status.CaptureID, c.RecordingID, cover.Settings.CoverID)
	}
	if err != nil {
		return err
	}
	items, err := s.packages.ListRecordingCovers(ctx, c.Status.CaptureID, c.RecordingID)
	if err != nil {
		return err
	}
	for _, item := range items {
		if item.ID == promoted.CoverID && (item.State == "pending" || item.State == "processing") {
			return ErrCoverPending
		}
		if item.ID == promoted.CoverID && item.State == "ready" {
			repo, _ := s.liveCoverRepository()
			return repo.InheritLiveCover(ctx, c, item.ID, s.now(), func(ctx context.Context) error {
				return s.packages.RetainRecordingCover(ctx, c.Status.CaptureID, c.RecordingID, item.ID, cover.ReferenceID)
			})
		}
	}
	return ErrConflict
}
