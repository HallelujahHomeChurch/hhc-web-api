package httpapi

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/google/uuid"
)

func (h *Handler) adminOnlineConvert(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	expected, ok := onlineIfMatch(w, r)
	if !ok {
		return
	}
	var input onlinebulletins.QueueSimplifiedInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	input.TargetOnlineVersion = expected
	queued, err := h.service.QueueOnlineConversion(r.Context(), r.PathValue("issueID"), r.PathValue("series"), r.PathValue("contentLocale"), input, actor(r))
	if err != nil {
		h.onlineWriteError(w, r, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, queued.Version))
	status := http.StatusAccepted
	if queued.Job.Status == "failed" || queued.Job.Status == "review_ready" {
		status = http.StatusOK
	}
	writeData(w, status, queued, nil)
}

func (h *Handler) adminOnlineExtract(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	issueID, series, locale := r.PathValue("issueID"), r.PathValue("series"), r.PathValue("contentLocale")
	if series != "general" || locale != "zh-Hant" {
		handleError(w, bulletins.ErrNotFound)
		return
	}
	if uuid.Validate(issueID) != nil {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	expected, ok := onlineIfMatch(w, r)
	if !ok {
		return
	}
	var input struct {
		CanonicalVersion int64 `json:"canonicalVersion"`
		Retry            bool  `json:"retry"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.CanonicalVersion < 1 {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	issue, err := h.service.GetIssue(r.Context(), issueID)
	if err != nil {
		handleError(w, err)
		return
	}
	if issue.Version != input.CanonicalVersion {
		h.onlineWriteError(w, r, bulletins.ErrPrecondition)
		return
	}
	assetID := ""
	for _, edition := range issue.Versions {
		if edition.Series == series && edition.Locale == locale {
			assetID = edition.PDFAssetID
			break
		}
	}
	if assetID == "" {
		handleError(w, bulletins.ErrNotFound)
		return
	}
	if h.uploads == nil {
		handleError(w, assetclient.ErrUnavailable)
		return
	}
	asset, err := h.uploads.Get(r.Context(), assetID)
	if errors.Is(err, assetclient.ErrUnavailable) {
		handleError(w, err)
		return
	}
	if err != nil || !validOnlineSource(asset, issue.ID, assetID) {
		handleError(w, bulletins.ErrNotFound)
		return
	}
	queued, err := h.service.QueueOnlineExtraction(r.Context(), issueID, series, locale, onlinebulletins.QueueExtractionInput{CanonicalVersion: input.CanonicalVersion, OnlineVersion: expected, SourceAssetID: assetID, SourceChecksum: asset.ChecksumSHA256, Retry: input.Retry}, actor(r))
	if err != nil {
		h.onlineWriteError(w, r, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, queued.Version))
	status := http.StatusAccepted
	if queued.Job.Status == "failed" || queued.Job.Status == "review_ready" {
		status = http.StatusOK
	}
	writeData(w, status, queued, nil)
}

func (h *Handler) adminOnlineConfirm(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	expected, ok := onlineIfMatch(w, r)
	if !ok {
		return
	}
	var input onlinebulletins.ConfirmInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	input.OnlineVersion = expected
	confirmed, err := h.service.ConfirmOnline(r.Context(), r.PathValue("issueID"), r.PathValue("series"), r.PathValue("contentLocale"), input, actor(r))
	if err != nil {
		h.onlineWriteError(w, r, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, confirmed.Version))
	writeData(w, http.StatusOK, confirmed, nil)
}

func (h *Handler) adminOnlinePublish(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	expected, ok := onlineIfMatch(w, r)
	if !ok {
		return
	}
	var input onlinebulletins.PublishInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, onlinebulletins.MaxDocumentBytes))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	if input.CanonicalVersion < 1 || input.Revision < 1 || expected < 1 || len(input.Mappings) > 65536 {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	state, err := h.service.OnlineState(r.Context(), r.PathValue("issueID"), r.PathValue("series"), r.PathValue("contentLocale"))
	if err != nil {
		h.onlineWriteError(w, r, err)
		return
	}
	if state.Version != expected || state.Local == nil || state.Local.Revision != input.Revision || state.CanonicalVersion != input.CanonicalVersion {
		h.onlineWriteError(w, r, bulletins.ErrPrecondition)
		return
	}
	if err := h.hydrateBulletinIllustrations(r.Context(), state.IssueID, state.Local.Document.Components, nil); err != nil {
		handleError(w, err)
		return
	}
	input.OnlineVersion = expected
	result, err := h.service.PublishOnline(r.Context(), r.PathValue("issueID"), r.PathValue("series"), r.PathValue("contentLocale"), input, actor(r))
	if err != nil {
		h.onlineWriteError(w, r, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, result.Version))
	writeData(w, http.StatusOK, result, nil)
}

func (h *Handler) adminOnlineUnpublish(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	expected, ok := onlineIfMatch(w, r)
	if !ok {
		return
	}
	var input struct {
		CanonicalVersion int64 `json:"canonicalVersion"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	result, err := h.service.UnpublishOnline(r.Context(), r.PathValue("issueID"), r.PathValue("series"), r.PathValue("contentLocale"), input.CanonicalVersion, expected, actor(r))
	if err != nil {
		h.onlineWriteError(w, r, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, result.Version))
	writeData(w, http.StatusOK, result, nil)
}

func (h *Handler) adminOnlineRestore(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	expected, ok := onlineIfMatch(w, r)
	if !ok {
		return
	}
	revision, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
	if err != nil || revision < 1 || strconv.FormatInt(revision, 10) != r.PathValue("revision") {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	var input struct {
		CanonicalVersion int64 `json:"canonicalVersion"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	restored, err := h.service.RestoreOnline(r.Context(), r.PathValue("issueID"), r.PathValue("series"), r.PathValue("contentLocale"), onlinebulletins.RestoreInput{CanonicalVersion: input.CanonicalVersion, OnlineVersion: expected, Revision: revision}, actor(r))
	if err != nil {
		h.onlineWriteError(w, r, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, restored.Version))
	writeData(w, http.StatusOK, restored, nil)
}

func (h *Handler) adminOnlineHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	before, limit := int64(0), int64(20)
	for name, target := range map[string]*int64{"before": &before, "limit": &limit} {
		values, ok := r.URL.Query()[name]
		if !ok {
			continue
		}
		if len(values) != 1 {
			handleError(w, bulletins.ErrInvalid)
			return
		}
		value, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || value < 1 || (name == "limit" && value > 50) {
			handleError(w, bulletins.ErrInvalid)
			return
		}
		*target = value
	}
	page, err := h.service.OnlineHistory(r.Context(), r.PathValue("issueID"), r.PathValue("series"), r.PathValue("contentLocale"), before, int(limit))
	if err != nil {
		handleError(w, err)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.bulletin.online.read", page.DocumentID, auditclient.Metadata{}) {
		return
	}
	writeData(w, http.StatusOK, page, nil)
}

func (h *Handler) adminOnlineRevision(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	revision, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
	if err != nil || revision < 1 || strconv.FormatInt(revision, 10) != r.PathValue("revision") {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	detail, err := h.service.OnlineRevision(r.Context(), r.PathValue("issueID"), r.PathValue("series"), r.PathValue("contentLocale"), revision)
	if err != nil {
		handleError(w, err)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.bulletin.online.read", detail.DocumentID, auditclient.Metadata{}) {
		return
	}
	// Keep authoring/recovery available if an asset later becomes unavailable.
	_ = h.hydrateBulletinIllustrations(r.Context(), r.PathValue("issueID"), detail.Document.Components, nil)
	writeData(w, http.StatusOK, detail, nil)
}

func onlineIfMatch(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.TrimSpace(r.Header.Get("If-Match"))
	version, err := strconv.ParseInt(strings.Trim(raw, `"`), 10, 64)
	if len(r.Header.Values("If-Match")) != 1 || err != nil || version < 0 || raw != fmt.Sprintf(`"%d"`, version) {
		writeError(w, http.StatusPreconditionRequired, "precondition_required", "A quoted Online version is required.")
		return 0, false
	}
	return version, true
}

func (h *Handler) onlineWriteError(w http.ResponseWriter, r *http.Request, err error) {
	if !errors.Is(err, bulletins.ErrPrecondition) {
		handleError(w, err)
		return
	}
	state, err := h.service.OnlineState(r.Context(), r.PathValue("issueID"), r.PathValue("series"), r.PathValue("contentLocale"))
	if errors.Is(err, bulletins.ErrNotFound) {
		// First conversion can conflict before the target edition exists. Do not
		// disguise the original CAS failure as a missing document.
		writeError(w, http.StatusConflict, "version_conflict", "The source or canonical version changed. Reload before converting.")
		return
	}
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, state.Version))
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(envelope{Data: nil, Meta: map[string]any{"currentVersion": state.Version, "canonicalVersion": state.CanonicalVersion}, Error: map[string]string{"code": "version_conflict", "message": "The Online or canonical version changed. Reload before saving."}})
}

func validOnlineSource(asset assetclient.Asset, issueID, assetID string) bool {
	checksum, err := hex.DecodeString(asset.ChecksumSHA256)
	return err == nil && len(checksum) == 32 && strings.ToLower(asset.ChecksumSHA256) == asset.ChecksumSHA256 && ownedBulletinAsset(asset, issueID, assetID, "zh-Hant") && asset.Purpose == "weekly_bulletin" && asset.Visibility == "private" && asset.UploadStatus == "completed" && asset.ScanStatus == "clean" && (asset.ProcessingStatus == "ready" || asset.ProcessingStatus == "not_required") && asset.DetectedMIMEType == "application/pdf"
}

func (h *Handler) adminOnlineState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	state, err := h.service.OnlineState(r.Context(), r.PathValue("issueID"), r.PathValue("series"), r.PathValue("contentLocale"))
	if err != nil {
		handleError(w, err)
		return
	}
	action, resource := "cms.bulletin.online.read", state.DocumentID
	if resource == "" {
		action, resource = "cms.bulletin.read", state.IssueID
	}
	if !h.enqueueAdminAudit(w, r, action, resource, auditclient.Metadata{}) {
		return
	}
	for _, revision := range []*onlinebulletins.AdminRevision{state.Local} {
		if revision != nil {
			_ = h.hydrateBulletinIllustrations(r.Context(), state.IssueID, revision.Document.Components, nil)
		}
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, state.Version))
	writeData(w, http.StatusOK, state, nil)
}

func (h *Handler) adminOnlineComparison(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	comparison, err := h.service.OnlineComparison(r.Context(), r.PathValue("issueID"), r.PathValue("series"), r.PathValue("contentLocale"))
	if err != nil {
		handleError(w, err)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.bulletin.online.read", comparison.DocumentID, auditclient.Metadata{}) {
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, comparison.Version))
	writeData(w, http.StatusOK, comparison, nil)
}

func (h *Handler) adminOnlineCompare(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	expected, ok := onlineIfMatch(w, r)
	if !ok {
		return
	}
	var input onlinebulletins.CompareInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, onlinebulletins.MaxDocumentBytes))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	input.OnlineVersion = expected
	saved, err := h.service.ApplyOnlineComparison(r.Context(), r.PathValue("issueID"), r.PathValue("series"), r.PathValue("contentLocale"), input, actor(r))
	if err != nil {
		h.onlineWriteError(w, r, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, saved.Version))
	writeData(w, http.StatusOK, saved, nil)
}

func (h *Handler) adminOnlineSave(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	expected, ok := onlineIfMatch(w, r)
	if !ok {
		return
	}
	var input onlinebulletins.SaveDraftInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, onlinebulletins.MaxDocumentBytes))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	input.OnlineVersion = expected
	saved, err := h.service.SaveOnlineDraft(r.Context(), r.PathValue("issueID"), r.PathValue("series"), r.PathValue("contentLocale"), input, actor(r))
	if err != nil {
		h.onlineWriteError(w, r, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, saved.Version))
	writeData(w, http.StatusOK, saved, nil)
}

// Preview reuses the extractor's owner/scan/checksum/size checks and temporary
// grant lifecycle. No response bytes escape before validation and revocation.
func (h *Handler) adminOnlineSourcePDF(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	notFound := func() { writeError(w, http.StatusNotFound, "not_found", "The source PDF was not found.") }
	unavailable := func() {
		writeError(w, http.StatusServiceUnavailable, "source_unavailable", "The source PDF is unavailable.")
	}
	if r.PathValue("series") != "general" || r.PathValue("contentLocale") != "zh-Hant" {
		notFound()
		return
	}
	reader, ok := h.uploads.(interface {
		WithBulletinSource(context.Context, assetclient.Asset, string, int, func(string) error) error
	})
	if !ok || h.service == nil {
		unavailable()
		return
	}
	issue, err := h.service.GetIssue(r.Context(), r.PathValue("issueID"))
	if err != nil {
		handleError(w, err)
		return
	}
	assetID := ""
	for _, version := range issue.Versions {
		if version.Series == r.PathValue("series") && version.Locale == r.PathValue("contentLocale") {
			assetID = version.PDFAssetID
			break
		}
	}
	if assetID == "" {
		notFound()
		return
	}
	asset, err := h.uploads.Get(r.Context(), assetID)
	if errors.Is(err, assetclient.ErrUnavailable) {
		unavailable()
		return
	}
	if err != nil || !validOnlineSource(asset, issue.ID, assetID) {
		notFound()
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.bulletin.read", issue.ID, auditclient.Metadata{}) {
		return
	}
	var body bytes.Buffer
	err = reader.WithBulletinSource(r.Context(), asset, "preview-"+platform.NewID(), 1, func(name string) error {
		file, err := os.Open(name)
		if err != nil {
			return err
		}
		defer file.Close()
		n, err := io.Copy(&body, io.LimitReader(file, maxBulletinPDFSize+1))
		if n > maxBulletinPDFSize {
			return assetclient.ErrInvalid
		}
		return err
	})
	if err != nil {
		unavailable()
		return
	}
	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Disposition", `inline; filename="bulletin-source.pdf"`)
	w.Header().Set("Content-Length", strconv.Itoa(body.Len()))
	w.Header().Set("X-HHC-Source-SHA256", asset.ChecksumSHA256)
	w.Header().Set("X-HHC-Source-Version", strconv.FormatInt(issue.Version, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, &body)
}
