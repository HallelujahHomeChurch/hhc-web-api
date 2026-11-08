package recordings

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

type CoverSelection struct {
	Mode        string `json:"mode"`
	CandidateID string `json:"candidateId,omitempty"`
	UploadID    string `json:"uploadId,omitempty"`
}

func (v CoverSelection) Valid() bool {
	return v.Mode == "auto" && assetclient.RecordingCoverID.MatchString(v.CandidateID) && v.UploadID == "" || v.Mode == "custom" && assetclient.RecordingCoverID.MatchString(v.UploadID) && v.CandidateID == ""
}

type CoverReceipt struct {
	CoverID          string `json:"coverId"`
	RecordingVersion int64  `json:"recordingVersion"`
}
type CoverResult struct {
	Recording Recording    `json:"recording"`
	CoverID   string       `json:"coverId"`
	Outcome   string       `json:"outcome"`
	Receipt   CoverReceipt `json:"receipt"`
}

func NewCoverResult(receipt CoverReceipt, current Recording, now time.Time) CoverResult {
	outcome := "state_changed"
	if current.SelectedCoverID == receipt.CoverID && current.Version == receipt.RecordingVersion && !current.Expired(now) {
		outcome = "selected"
	}
	return CoverResult{Recording: current, CoverID: current.SelectedCoverID, Outcome: outcome, Receipt: receipt}
}

type CoverList struct {
	Items            []assetclient.RecordingCover `json:"items"`
	SelectedCoverID  string                       `json:"selectedCoverId,omitempty"`
	RecordingVersion int64                        `json:"recordingVersion"`
}
type CoverOperation struct {
	ID, RecordingID, PackageID, CoverID string
	Expected                            int64
	Selection                           CoverSelection
	Actor, Key                          string
}
type CoverRepository interface {
	GetCoverOperation(context.Context, string, int64, CoverSelection, string, string, time.Time) (CoverResult, error)
	PrepareCoverOperation(context.Context, CoverOperation, time.Time) (CoverOperation, error)
	CommitCoverOperation(context.Context, CoverOperation, bool, time.Time, func(context.Context) error) (CoverResult, error)
	FailCoverOperation(context.Context, string) error
	ReconcileCoverReferences(context.Context, time.Time, func(context.Context, CoverOperation) error) error
}

func (s *Service) coverRecording(ctx context.Context, id string) (Recording, error) {
	if err := s.syncLifecycle(ctx); err != nil {
		return Recording{}, err
	}
	v, err := s.repo.Get(ctx, id)
	if err != nil {
		return v, err
	}
	if v.Expired(s.now()) {
		return v, ErrNotFound
	}
	if v.PackageID == "" || v.ReadyAt == nil || s.packages == nil {
		return v, ErrConflict
	}
	return v, nil
}
func (s *Service) ListCovers(ctx context.Context, id string) (CoverList, error) {
	v, err := s.coverRecording(ctx, id)
	if err != nil {
		return CoverList{}, err
	}
	items, err := s.packages.ListRecordingCovers(ctx, v.PackageID, v.ID)
	if err != nil {
		return CoverList{}, err
	}
	selected := v.SelectedCoverID
	for i := range items {
		items[i].ContentPath = "/api/admin/recordings/" + id + "/covers/" + items[i].ID + "/content"
		if selected == "" && items[i].Kind == "auto" && items[i].State == "ready" {
			selected = items[i].ID
		}
	}
	if items == nil {
		items = []assetclient.RecordingCover{}
	}
	return CoverList{Items: items, SelectedCoverID: selected, RecordingVersion: v.Version}, nil
}
func (s *Service) UploadCover(ctx context.Context, id, mimeType, key string, body io.Reader, canPublish bool) (assetclient.RecordingCoverUpload, error) {
	if !ValidOperationKey(key) || (mimeType != "image/jpeg" && mimeType != "image/png") {
		return assetclient.RecordingCoverUpload{}, ErrInvalid
	}
	v, err := s.coverRecording(ctx, id)
	if err != nil {
		return assetclient.RecordingCoverUpload{}, err
	}
	if v.Status == "published" && !canPublish {
		return assetclient.RecordingCoverUpload{}, ErrForbidden
	}
	data, err := io.ReadAll(io.LimitReader(body, 5<<20+1))
	if err != nil || len(data) > 5<<20 {
		return assetclient.RecordingCoverUpload{}, assetclient.ErrCoverTooLarge
	}
	return s.packages.UploadRecordingCover(ctx, v.PackageID, v.ID, mimeType, key, data)
}
func (s *Service) SelectCover(ctx context.Context, id string, expected int64, input CoverSelection, actor, key string, canPublish bool) (CoverResult, error) {
	if expected < 1 || !input.Valid() || actor == "" || !ValidOperationKey(key) {
		return CoverResult{}, ErrInvalid
	}
	repo, ok := s.repo.(CoverRepository)
	if !ok {
		return CoverResult{}, ErrConflict
	}
	replay, err := repo.GetCoverOperation(ctx, id, expected, input, actor, key, s.now())
	if !errors.Is(err, ErrNotFound) {
		return replay, err
	}
	v, err := s.coverRecording(ctx, id)
	if err != nil {
		return CoverResult{}, err
	}
	if v.Status == "published" && !canPublish {
		return CoverResult{}, ErrForbidden
	}
	if v.Version != expected {
		return CoverResult{}, ErrPrecondition
	}
	items, err := s.packages.ListRecordingCovers(ctx, v.PackageID, id)
	if err != nil {
		return CoverResult{}, err
	}
	cover := ""
	for _, item := range items {
		if item.State == "ready" && (input.Mode == "auto" && item.Kind == "auto" && item.ID == input.CandidateID || input.Mode == "custom" && item.Kind == "custom" && item.UploadID == input.UploadID) {
			cover = item.ID
			break
		}
	}
	if cover == "" {
		return CoverResult{}, ErrConflict
	}
	op, err := repo.PrepareCoverOperation(ctx, CoverOperation{ID: platform.NewID(), RecordingID: id, PackageID: v.PackageID, CoverID: cover, Expected: expected, Selection: input, Actor: actor, Key: key}, s.now())
	if err != nil {
		return CoverResult{}, err
	}
	result, err := repo.CommitCoverOperation(ctx, op, canPublish, s.now(), func(ctx context.Context) error {
		return s.packages.RetainRecordingCover(ctx, op.PackageID, id, op.CoverID, op.ID)
	})
	if errors.Is(err, ErrPrecondition) || errors.Is(err, ErrConflict) || errors.Is(err, ErrForbidden) || errors.Is(err, ErrNotFound) {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = repo.FailCoverOperation(cleanup, op.ID)
	}
	return result, err
}
func (s *Service) CoverContent(ctx context.Context, id, cover string, member bool) ([]byte, error) {
	v, err := s.coverRecording(ctx, id)
	if err != nil {
		return nil, err
	}
	if member && !v.Visible(s.now()) {
		return nil, ErrNotFound
	}
	if member {
		return s.selectedCoverContent(ctx, v)
	}
	return s.packages.ReadRecordingCover(ctx, v.PackageID, id, cover)
}

func (s *Service) selectedCoverContent(ctx context.Context, v Recording) ([]byte, error) {
	cover := v.SelectedCoverID
	if cover == "" {
		items, err := s.packages.ListRecordingCovers(ctx, v.PackageID, v.ID)
		if err != nil {
			return nil, err
		}
		for _, item := range items {
			if item.Kind == "auto" && item.State == "ready" {
				cover = item.ID
				break
			}
		}
	}
	if cover == "" {
		return nil, ErrNotFound
	}
	return s.packages.ReadRecordingCover(ctx, v.PackageID, v.ID, cover)
}
func (s *Service) ReconcileCoverReferences(ctx context.Context) error {
	repo, ok := s.repo.(CoverRepository)
	if !ok || s.packages == nil {
		return nil
	}
	return repo.ReconcileCoverReferences(ctx, s.now(), func(ctx context.Context, op CoverOperation) error {
		return s.packages.ReleaseRecordingCover(ctx, op.PackageID, op.RecordingID, op.CoverID, op.ID)
	})
}
