package httpapi

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/google/uuid"
)

const bulletinRendererV2 = "trustmark-v2"

var errBulletinChanged = errors.New("bulletin changed")

type downloadJobRepository interface {
	CreateDownloadJob(context.Context, bulletinwatermark.DownloadJobRequest) (bulletinwatermark.DownloadJob, error)
	GetDownloadJob(context.Context, string, string, time.Time) (bulletinwatermark.DownloadJob, error)
}

func downloadJobError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, bulletinwatermark.ErrNotFound):
		writeError(w, http.StatusNotFound, "bulletin_download_job_not_found", "Download preparation was not found.")
	case errors.Is(err, bulletinwatermark.ErrDownloadConflict):
		writeError(w, http.StatusConflict, "bulletin_download_conflict", "The request conflicts with an existing preparation.")
	case errors.Is(err, bulletinwatermark.ErrRestricted):
		writeError(w, http.StatusForbidden, "bulletin_processing_restricted", "Document processing is restricted.")
	default:
		writeError(w, http.StatusServiceUnavailable, "bulletin_download_unavailable", "Document preparation is unavailable.")
	}
}

func (h *Handler) createBulletinDownloadJob(w http.ResponseWriter, r *http.Request) {
	repo, ok := h.watermarkRepo.(downloadJobRepository)
	if !ok || h.service == nil || h.uploads == nil || h.watermarkPrivacyVersion == "" {
		downloadJobError(w, bulletinwatermark.ErrUnavailable)
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(r.Header.Values("Idempotency-Key")) != 1 || uuid.Validate(key) != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", "A UUID Idempotency-Key is required.")
		return
	}
	var input struct {
		IssueID uuid.UUID `json:"issueId"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.IssueID == uuid.Nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", "A bulletin issue ID is required.")
		return
	}
	protected := protectedBulletinFrom(r)
	sourceBulletin, err := h.service.GetProtectedVersion(r.Context(), input.IssueID.String(), protected.series, protected.locale)
	if err != nil {
		handleProtectedBulletinError(w, err)
		return
	}
	asset, err := h.uploads.Get(r.Context(), sourceBulletin.ProtectedAssetID)
	if err != nil || !protectedAssetMatches(asset, sourceBulletin) || len(asset.ChecksumSHA256) != 64 {
		downloadJobError(w, bulletinwatermark.ErrUnavailable)
		return
	}
	p, _ := r.Context().Value(principalKey{}).(principal)
	fingerprint, err := bulletinwatermark.NewFingerprint()
	if err != nil {
		downloadJobError(w, err)
		return
	}
	code, err := bulletinwatermark.NewCode()
	if err != nil {
		downloadJobError(w, err)
		return
	}
	now := time.Now().UTC()
	source := bulletinwatermark.Source{IssueID: sourceBulletin.IssueID, AssetID: sourceBulletin.ProtectedAssetID, Series: sourceBulletin.Series, Locale: sourceBulletin.Locale, Revision: sourceBulletin.Version, Filename: sourceBulletin.DownloadName}
	job, err := repo.CreateDownloadJob(r.Context(), bulletinwatermark.DownloadJobRequest{ID: uuid.NewString(), IdempotencyKey: key, AccountID: p.UserID, IssueDate: sourceBulletin.IssueDate, RendererVersion: bulletinRendererV2, FingerprintPayload: fingerprint, TraceCode: code, Source: source, SourceSHA256: asset.ChecksumSHA256, Now: now})
	if err != nil {
		downloadJobError(w, err)
		return
	}
	w.Header().Set("Location", "/api/member/bulletin-download-jobs/"+job.ID)
	status := http.StatusOK
	if !job.Ready(now) {
		status = http.StatusAccepted
		w.Header().Set("Retry-After", "2")
	}
	writeData(w, status, job.View(), nil)
}

func (h *Handler) getBulletinDownloadJob(w http.ResponseWriter, r *http.Request) {
	job, ok := h.authorizedDownloadJob(w, r)
	if !ok {
		return
	}
	status := http.StatusOK
	if job.Status == "queued" || job.Status == "running" {
		status = http.StatusAccepted
		w.Header().Set("Retry-After", "2")
	}
	writeData(w, status, job.View(), nil)
}

func (h *Handler) downloadPreparedBulletin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Accept-Ranges", "none")
	job, ok := h.authorizedDownloadJob(w, r)
	if !ok {
		return
	}
	if !job.Ready(time.Now().UTC()) {
		if job.Status == "queued" || job.Status == "running" {
			w.Header().Set("Retry-After", "2")
		}
		writeError(w, http.StatusConflict, "bulletin_download_not_ready", "The document is not ready.")
		return
	}
	if h.uploads == nil || h.downloads == nil {
		downloadJobError(w, bulletinwatermark.ErrUnavailable)
		return
	}
	asset, err := h.uploads.Get(r.Context(), job.DerivedAssetID)
	if err != nil || !derivedBulletinAssetMatches(asset, job) {
		downloadJobError(w, bulletinwatermark.ErrUnavailable)
		return
	}
	response, err := h.downloads.AuthorizedDownload(r.Context(), http.MethodGet, job.DerivedAssetID, "", "")
	if err != nil || response.StatusCode != http.StatusOK {
		if response != nil {
			response.Body.Close()
		}
		downloadJobError(w, bulletinwatermark.ErrUnavailable)
		return
	}
	defer response.Body.Close()
	name := filepath.Base(job.Filename)
	if name == "." || name == "" {
		name = "bulletin.pdf"
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, response.Body)
}

func (h *Handler) authorizedDownloadJob(w http.ResponseWriter, r *http.Request) (bulletinwatermark.DownloadJob, bool) {
	p, ok := r.Context().Value(principalKey{}).(principal)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Trusted gateway identity is required.")
		return bulletinwatermark.DownloadJob{}, false
	}
	job, err := h.currentDownloadJob(r.Context(), p, protectedBulletinFrom(r), r.PathValue("id"))
	if errors.Is(err, errBulletinChanged) {
		writeError(w, http.StatusConflict, "bulletin_changed", "The document changed. Please retry.")
		return bulletinwatermark.DownloadJob{}, false
	}
	if err != nil {
		downloadJobError(w, err)
		return bulletinwatermark.DownloadJob{}, false
	}
	return job, true
}

func (h *Handler) currentDownloadJob(ctx context.Context, p principal, protected protectedBulletinRequest, id string) (bulletinwatermark.DownloadJob, error) {
	repo, ok := h.watermarkRepo.(downloadJobRepository)
	if !ok || h.service == nil || uuid.Validate(id) != nil || uuid.Validate(p.UserID) != nil {
		return bulletinwatermark.DownloadJob{}, bulletinwatermark.ErrNotFound
	}
	job, err := repo.GetDownloadJob(ctx, id, p.UserID, time.Now().UTC())
	if err != nil {
		return bulletinwatermark.DownloadJob{}, err
	}
	source, err := h.service.GetProtectedVersion(ctx, job.IssueID, protected.series, protected.locale)
	if err != nil || source.IssueID != job.IssueID || source.Series != protected.series || source.Series != job.Series || job.Series != protected.series || source.Locale != job.Locale || job.Locale != protected.locale || source.Version != job.Revision || source.ProtectedAssetID != job.SourceAssetID {
		return bulletinwatermark.DownloadJob{}, errBulletinChanged
	}
	return job, nil
}

func derivedBulletinAssetMatches(asset assetclient.Asset, job bulletinwatermark.DownloadJob) bool {
	return asset.ID == job.DerivedAssetID && asset.Namespace == "cms.weekly.pdf" && asset.OwnerService == "hhc-web-api" && asset.OwnerType == "bulletin_download_job" && asset.OwnerID == job.ID && asset.Purpose == "weekly_bulletin_member" && asset.Locale == job.Locale && asset.UploadStatus == "completed" && asset.ScanStatus == "clean" && (asset.ProcessingStatus == "ready" || asset.ProcessingStatus == "not_required") && asset.Visibility == "private"
}
