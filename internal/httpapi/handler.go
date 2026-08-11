package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
)

const maxBulletinPDFSize = 20 << 20

type assetUploads interface {
	CreateBulletinUpload(context.Context, string, string, string, string, int64, string) (assetclient.CreatedUpload, error)
	CreateNewsCoverUpload(context.Context, string, string, string, string, int64, string) (assetclient.CreatedUpload, error)
	CompleteUpload(context.Context, string, assetclient.CompleteUploadInput) (assetclient.Asset, error)
	Get(context.Context, string) (assetclient.Asset, error)
	RequeueScan(context.Context, string) error
}

type engagementProxy interface {
	Forward(context.Context, string, string, io.Reader, string) (*http.Response, error)
}

type Handler struct {
	service        *bulletins.Service
	content        *content.Service
	db             *sql.DB
	uploads        assetUploads
	engagement     engagementProxy
	translation    TranslationPreviewer
	translationNow func() time.Time
	translationTTL time.Duration
	trustedCaller  string
	daprAPIToken   string
	allowDevCaller bool
}

func New(service *bulletins.Service, db *sql.DB, uploads assetUploads) *Handler {
	return NewWithContent(service, nil, db, uploads, "api-gateway", "", false)
}
func NewWithContent(service *bulletins.Service, contentService *content.Service, db *sql.DB, uploads assetUploads, trustedCaller, daprAPIToken string, allowDevCaller bool, engagement ...engagementProxy) *Handler {
	handler := &Handler{service: service, content: contentService, db: db, uploads: uploads, trustedCaller: trustedCaller, daprAPIToken: daprAPIToken, allowDevCaller: allowDevCaller, translationNow: time.Now, translationTTL: 50 * time.Second}
	if len(engagement) > 0 {
		handler.engagement = engagement[0]
	}
	return handler
}
func NewWithTranslation(service *bulletins.Service, contentService *content.Service, db *sql.DB, uploads assetUploads, trustedCaller, daprAPIToken string, allowDevCaller bool, previewer TranslationPreviewer, writeDeadline time.Duration, now func() time.Time, engagement ...engagementProxy) *Handler {
	handler := NewWithContent(service, contentService, db, uploads, trustedCaller, daprAPIToken, allowDevCaller, engagement...)
	handler.translation = previewer
	handler.translationTTL = writeDeadline
	handler.translationNow = now
	return handler
}
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	live := func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, map[string]string{"status": "healthy"}, nil)
	}
	mux.HandleFunc("GET /health", live)
	mux.HandleFunc("GET /health/live", live)
	mux.HandleFunc("GET /ready", h.ready)
	mux.HandleFunc("GET /health/ready", h.ready)
	mux.HandleFunc("GET /api/bulletins/latest", h.publicLatest)
	mux.HandleFunc("GET /api/bulletins/by-number/{issueNumber}", h.publicByNumber)
	mux.HandleFunc("GET /api/bulletins/{issueDate}", h.publicByDate)
	mux.HandleFunc("GET /api/bulletins", h.publicList)
	admin := http.NewServeMux()
	admin.HandleFunc("GET /api/admin/bulletins", requireScope("cms:read", h.adminList))
	admin.HandleFunc("POST /api/admin/bulletins", requireScope("cms:write", h.adminCreate))
	admin.HandleFunc("GET /api/admin/bulletins/{issueID}", requireScope("cms:read", h.adminGet))
	admin.HandleFunc("PUT /api/admin/bulletins/{issueID}", requireScope("cms:write", h.adminUpdateIssue))
	admin.HandleFunc("PUT /api/admin/bulletins/{issueID}/versions/{locale}", requireScope("cms:write", h.adminUpdateVersion))
	admin.HandleFunc("DELETE /api/admin/bulletins/{issueID}/versions/{locale}", requireScope("cms:write", h.adminDeleteVersion))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/upload-sessions", requireScopes([]string{"cms:write", "assets:write"}, h.adminCreateUpload))
	admin.HandleFunc("GET /api/admin/bulletins/{issueID}/assets/{assetID}", requireScope("cms:read", h.adminAssetStatus))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/assets/{assetID}/scan/retry", requireScopes([]string{"cms:write", "assets:write"}, h.adminRetryAssetScan))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/assets/{assetID}/complete", requireScopes([]string{"cms:write", "assets:write"}, h.adminCompleteUpload))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/publish", requireScope("cms:publish", h.adminPublish))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/unpublish", requireScope("cms:publish", h.adminUnpublish))
	admin.HandleFunc("DELETE /api/admin/bulletins/{issueID}", requireScope("cms:write", h.adminDelete))
	admin.HandleFunc("GET /api/admin/bulletins/{issueID}/revisions", requireScope("cms:read", h.adminIssueRevisions))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/revisions/{revision}/restore", requireScope("cms:write", h.adminRestoreIssueRevision))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/translation-previews/{targetLocale}", requireScope("cms:write", h.adminBulletinTranslationPreview))
	admin.HandleFunc("POST /api/admin/content/{module}/{contentID}/translation-previews/{targetLocale}", requireScope("cms:write", h.adminContentTranslationPreview))
	if h.content != nil {
		h.contentRoutes(mux, admin)
	}
	if h.engagement != nil {
		admin.HandleFunc("GET /api/admin/campaigns", requireScope("cms:read", h.forwardEngagement))
		admin.HandleFunc("POST /api/admin/campaigns", requireScope("cms:write", h.forwardEngagement))
		admin.HandleFunc("GET /api/admin/campaigns/{campaignID}", requireScope("cms:read", h.forwardEngagement))
		admin.HandleFunc("PUT /api/admin/campaigns/{campaignID}", requireScope("cms:write", h.forwardEngagement))
		admin.HandleFunc("DELETE /api/admin/campaigns/{campaignID}", requireScope("cms:write", h.forwardEngagement))
		admin.HandleFunc("POST /api/admin/campaigns/{campaignID}/send", requireScope("cms:write", h.forwardEngagement))
		admin.HandleFunc("GET /api/admin/campaigns/{campaignID}/deliveries", requireScope("cms:read", h.forwardEngagement))
		admin.HandleFunc("POST /api/admin/campaigns/{campaignID}/retry-failed", requireScope("cms:write", h.forwardEngagement))
		admin.HandleFunc("GET /api/admin/campaign-schedules", requireScope("cms:read", h.forwardEngagement))
		admin.HandleFunc("POST /api/admin/campaign-schedules", requireScope("cms:write", h.forwardEngagement))
		admin.HandleFunc("GET /api/admin/campaign-schedules/{scheduleID}", requireScope("cms:read", h.forwardEngagement))
		admin.HandleFunc("PUT /api/admin/campaign-schedules/{scheduleID}", requireScope("cms:write", h.forwardEngagement))
		admin.HandleFunc("DELETE /api/admin/campaign-schedules/{scheduleID}", requireScope("cms:write", h.forwardEngagement))
	}
	mux.Handle("/api/admin/", requireTrusted(h.trustedCaller, h.daprAPIToken, h.allowDevCaller, admin))
	return requestID(accessLog(mux))
}

func (h *Handler) forwardEngagement(w http.ResponseWriter, r *http.Request) {
	path := "/priv" + strings.TrimPrefix(r.URL.Path, "/api/admin")
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	var body io.Reader
	if r.Body != nil && r.Body != http.NoBody {
		body = http.MaxBytesReader(w, r.Body, 1<<20)
	}
	response, err := h.engagement.Forward(r.Context(), r.Method, path, body, actor(r))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "engagement_service_unavailable", "Campaign service is unavailable.")
		return
	}
	defer response.Body.Close()
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(response.Body, 2<<20))
}

type createUploadInput struct {
	Locale    string `json:"locale"`
	FileName  string `json:"fileName"`
	MIMEType  string `json:"mimeType"`
	SizeBytes int64  `json:"sizeBytes"`
}
type completeUploadInput struct {
	Locale         string `json:"locale"`
	Title          string `json:"title"`
	Subtitle       string `json:"subtitle"`
	FileName       string `json:"fileName"`
	MIMEType       string `json:"mimeType"`
	SizeBytes      int64  `json:"sizeBytes"`
	ChecksumSHA256 string `json:"checksumSha256"`
}
type updateVersionInput struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
}

type assetStatusResponse struct {
	ID               string `json:"id"`
	UploadStatus     string `json:"uploadStatus"`
	ScanStatus       string `json:"scanStatus"`
	ProcessingStatus string `json:"processingStatus"`
	Retryable        bool   `json:"retryable"`
}

func cmsAssetStatus(asset assetclient.Asset) assetStatusResponse {
	return assetStatusResponse{
		ID: asset.ID, UploadStatus: asset.UploadStatus, ScanStatus: asset.ScanStatus,
		ProcessingStatus: asset.ProcessingStatus, Retryable: asset.ScanStatus == "failed",
	}
}

func (h *Handler) adminCreateUpload(w http.ResponseWriter, r *http.Request) {
	if h.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset uploads are unavailable.")
		return
	}
	var input createUploadInput
	if !decode(w, r, &input) {
		return
	}
	if !validLocale(input.Locale) || strings.TrimSpace(input.FileName) == "" || input.MIMEType != "application/pdf" || input.SizeBytes <= 0 || input.SizeBytes > maxBulletinPDFSize || strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	_, err := h.service.GetIssue(r.Context(), r.PathValue("issueID"))
	if err != nil {
		handleError(w, err)
		return
	}
	created, err := h.uploads.CreateBulletinUpload(r.Context(), r.PathValue("issueID"), input.Locale, input.FileName, input.MIMEType, input.SizeBytes, r.Header.Get("Idempotency-Key"))
	if err != nil {
		handleError(w, err)
		return
	}
	logAssetEvent(r, "asset upload session created", "bulletin", r.PathValue("issueID"), created.Asset.ID)
	writeData(w, http.StatusCreated, created, nil)
}
func (h *Handler) adminCompleteUpload(w http.ResponseWriter, r *http.Request) {
	if h.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset uploads are unavailable.")
		return
	}
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input completeUploadInput
	if !decode(w, r, &input) {
		return
	}
	if !validLocale(input.Locale) || strings.TrimSpace(input.Title) == "" || input.MIMEType != "application/pdf" || input.SizeBytes <= 0 || len(input.ChecksumSHA256) != 64 {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	_, err := h.service.GetIssue(r.Context(), r.PathValue("issueID"))
	if err != nil {
		handleError(w, err)
		return
	}
	assetID := r.PathValue("assetID")
	asset, err := h.uploads.Get(r.Context(), assetID)
	if errors.Is(err, assetclient.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset uploads are unavailable.")
		return
	}
	if err != nil || !ownedBulletinAsset(asset, r.PathValue("issueID"), assetID, input.Locale) {
		writeError(w, http.StatusForbidden, "asset_owner_mismatch", "The uploaded asset does not belong to this bulletin.")
		return
	}
	asset, err = h.uploads.CompleteUpload(r.Context(), assetID, assetclient.CompleteUploadInput{SizeBytes: input.SizeBytes, ChecksumSHA256: input.ChecksumSHA256, MIMEType: input.MIMEType})
	if err != nil {
		handleError(w, err)
		return
	}
	if !ownedBulletinAsset(asset, r.PathValue("issueID"), assetID, input.Locale) {
		writeError(w, http.StatusForbidden, "asset_owner_mismatch", "The uploaded asset does not belong to this bulletin.")
		return
	}
	fileName := asset.OriginalFileName
	if fileName == "" {
		fileName = input.FileName
	}
	value, err := h.service.PutVersion(r.Context(), r.PathValue("issueID"), expected, bulletins.PutVersionInput{Locale: input.Locale, Title: input.Title, Subtitle: input.Subtitle, PDFAssetID: asset.ID, PDFFileName: fileName}, actor(r))
	if err != nil {
		handleError(w, err)
		return
	}
	logAssetEvent(r, "asset attached", "bulletin", r.PathValue("issueID"), asset.ID)
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}
func (h *Handler) adminAssetStatus(w http.ResponseWriter, r *http.Request) {
	if h.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset status is unavailable.")
		return
	}
	asset, err := h.uploads.Get(r.Context(), r.PathValue("assetID"))
	if errors.Is(err, assetclient.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset status is unavailable.")
		return
	}
	if err != nil || asset.ID != r.PathValue("assetID") || asset.Namespace != "cms.weekly.pdf" ||
		asset.OwnerService != "hhc-web-api" || asset.OwnerType != "bulletin_issue" || asset.OwnerID != r.PathValue("issueID") {
		writeError(w, http.StatusNotFound, "not_found", "The bulletin asset was not found.")
		return
	}
	writeData(w, http.StatusOK, cmsAssetStatus(asset), nil)
}
func (h *Handler) adminRetryAssetScan(w http.ResponseWriter, r *http.Request) {
	if h.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset status is unavailable.")
		return
	}
	asset, err := h.uploads.Get(r.Context(), r.PathValue("assetID"))
	if errors.Is(err, assetclient.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset status is unavailable.")
		return
	}
	if err != nil || asset.ID != r.PathValue("assetID") || asset.Namespace != "cms.weekly.pdf" ||
		asset.OwnerService != "hhc-web-api" || asset.OwnerType != "bulletin_issue" || asset.OwnerID != r.PathValue("issueID") {
		writeError(w, http.StatusNotFound, "not_found", "The bulletin asset was not found.")
		return
	}
	if asset.ScanStatus != "failed" {
		writeError(w, http.StatusConflict, "asset_not_retryable", "The asset scan cannot be retried.")
		return
	}
	if err := h.uploads.RequeueScan(r.Context(), asset.ID); err != nil {
		if errors.Is(err, assetclient.ErrUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset status is unavailable.")
		} else {
			writeError(w, http.StatusConflict, "asset_not_retryable", "The asset scan state changed.")
		}
		return
	}
	asset.ScanStatus = "pending"
	writeData(w, http.StatusOK, cmsAssetStatus(asset), nil)
}
func ownedBulletinAsset(asset assetclient.Asset, issueID, assetID, locale string) bool {
	return asset.ID == assetID && asset.Namespace == "cms.weekly.pdf" &&
		asset.OwnerService == "hhc-web-api" && asset.OwnerType == "bulletin_issue" &&
		asset.OwnerID == issueID && asset.Locale == locale
}
func logAssetEvent(r *http.Request, message, resourceType, resourceID, assetID string) {
	slog.Info(message,
		"request_id", r.Header.Get("X-HHC-Request-ID"),
		"resource_type", resourceType,
		"resource_id", resourceID,
		"asset_id", assetID,
	)
}
func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := h.db.PingContext(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "Database is unavailable.")
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "ready"}, nil)
}
func (h *Handler) publicLatest(w http.ResponseWriter, r *http.Request) {
	value, err := h.service.GetPublicLatest(r.Context(), locale(r))
	if err != nil {
		handleError(w, err)
		return
	}
	publicResponse(w, value)
}
func (h *Handler) publicByDate(w http.ResponseWriter, r *http.Request) {
	value, err := h.service.GetPublicByDate(r.Context(), r.PathValue("issueDate"), locale(r))
	if err != nil {
		handleError(w, err)
		return
	}
	publicResponse(w, value)
}
func (h *Handler) publicByNumber(w http.ResponseWriter, r *http.Request) {
	raw := r.PathValue("issueNumber")
	parsed, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || parsed < 1 || strconv.FormatInt(parsed, 10) != raw {
		w.Header().Set("Cache-Control", "private, no-store")
		handleError(w, bulletins.ErrInvalid)
		return
	}
	value, err := h.service.GetPublicByNumber(r.Context(), int(parsed), locale(r))
	if err != nil {
		w.Header().Set("Cache-Control", "private, no-store")
		handleError(w, err)
		return
	}
	publicResponse(w, value)
}
func (h *Handler) publicList(w http.ResponseWriter, r *http.Request) {
	page, size := pagination(r)
	value, err := h.service.ListPublic(r.Context(), page, size)
	if err != nil {
		handleError(w, err)
		return
	}
	writeData(w, http.StatusOK, value.Items, map[string]any{"page": value.Page, "pageSize": value.PageSize, "total": value.Total})
}
func (h *Handler) adminList(w http.ResponseWriter, r *http.Request) {
	page, size := pagination(r)
	value, err := h.service.ListIssues(r.Context(), page, size, r.URL.Query().Get("status"), r.URL.Query().Get("q"))
	if err != nil {
		handleError(w, err)
		return
	}
	writeData(w, http.StatusOK, value.Items, map[string]any{"page": value.Page, "pageSize": value.PageSize, "total": value.Total})
}
func (h *Handler) adminGet(w http.ResponseWriter, r *http.Request) {
	value, err := h.service.GetIssue(r.Context(), r.PathValue("issueID"))
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}
func (h *Handler) adminUpdateIssue(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input bulletins.UpdateIssueInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.UpdateIssue(r.Context(), r.PathValue("issueID"), expected, input, actor(r))
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}
func (h *Handler) adminCreate(w http.ResponseWriter, r *http.Request) {
	var input bulletins.CreateIssueInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.CreateIssue(r.Context(), input, actor(r), r.Header.Get("Idempotency-Key"))
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusCreated, value, nil)
}
func (h *Handler) adminPublish(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input bulletins.PublishInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.Publish(r.Context(), r.PathValue("issueID"), input.Locale, expected, input.NotifySubscribers, actor(r))
	if err != nil {
		handleError(w, err)
		return
	}
	writeData(w, http.StatusAccepted, value, nil)
}
func (h *Handler) adminUnpublish(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input bulletins.PublishInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.Unpublish(r.Context(), r.PathValue("issueID"), input.Locale, expected, actor(r))
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}
func (h *Handler) adminDelete(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	if err := h.service.DeleteIssue(r.Context(), r.PathValue("issueID"), expected, actor(r)); err != nil {
		handleError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (h *Handler) adminUpdateVersion(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input updateVersionInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.UpdateVersion(r.Context(), r.PathValue("issueID"), r.PathValue("locale"), expected, bulletins.UpdateVersionInput{Title: input.Title, Subtitle: input.Subtitle}, actor(r))
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}
func (h *Handler) adminDeleteVersion(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	value, err := h.service.DeleteVersion(r.Context(), r.PathValue("issueID"), r.PathValue("locale"), expected, actor(r))
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}
func (h *Handler) adminIssueRevisions(w http.ResponseWriter, r *http.Request) {
	values, err := h.service.IssueRevisions(r.Context(), r.PathValue("issueID"))
	if err != nil {
		handleError(w, err)
		return
	}
	writeData(w, http.StatusOK, values, nil)
}
func (h *Handler) adminRestoreIssueRevision(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	revision, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
	if err != nil || revision < 1 {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	values, err := h.service.IssueRevisions(r.Context(), r.PathValue("issueID"))
	if err != nil {
		handleError(w, err)
		return
	}
	var snapshot *bulletins.Issue
	for index := range values {
		if values[index].Version == revision {
			snapshot = &values[index].Snapshot
			break
		}
	}
	if snapshot == nil {
		handleError(w, bulletins.ErrNotFound)
		return
	}
	if h.uploads == nil {
		handleError(w, assetclient.ErrUnavailable)
		return
	}
	for _, version := range snapshot.Versions {
		asset, assetErr := h.uploads.Get(r.Context(), version.PDFAssetID)
		if assetErr != nil || !ownedBulletinAsset(asset, r.PathValue("issueID"), version.PDFAssetID, version.Locale) {
			if errors.Is(assetErr, assetclient.ErrUnavailable) {
				handleError(w, assetErr)
			} else {
				handleError(w, bulletins.ErrNotPublishable)
			}
			return
		}
	}
	value, err := h.service.RestoreIssueRevision(r.Context(), r.PathValue("issueID"), revision, expected, actor(r))
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}
func publicResponse(w http.ResponseWriter, value bulletins.PublicBulletin) {
	w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=300")
	w.Header().Set("ETag", fmt.Sprintf(`"bulletin-%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}
func locale(r *http.Request) string {
	value := r.URL.Query().Get("locale")
	if value == "" {
		value = "zh-Hant"
	}
	return value
}
func validLocale(value string) bool {
	switch value {
	case "zh-Hant", "zh-Hans", "en", "ja", "ko":
		return true
	default:
		return false
	}
}
func pagination(r *http.Request) (int, int) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	return page, size
}
func ifMatch(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		writeError(w, http.StatusPreconditionRequired, "precondition_required", "A valid If-Match version is required.")
		return 0, false
	}
	return value, true
}
func decode(w http.ResponseWriter, r *http.Request, destination any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return false
	}
	return true
}
func handleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, assetclient.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset uploads are unavailable.")
	case errors.Is(err, bulletins.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", "The request is invalid.")
	case errors.Is(err, bulletins.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "The bulletin was not found.")
	case errors.Is(err, bulletins.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "The request conflicts with an existing resource.")
	case errors.Is(err, bulletins.ErrPrecondition):
		writeError(w, http.StatusPreconditionFailed, "version_conflict", "The resource changed. Reload and try again.")
	case errors.Is(err, bulletins.ErrNotPublishable):
		writeError(w, http.StatusUnprocessableEntity, "not_publishable", "The bulletin is not ready for this operation.")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "The service could not complete the request.")
	}
}

type envelope struct {
	Data  any `json:"data"`
	Meta  any `json:"meta"`
	Error any `json:"error"`
}

func writeData(w http.ResponseWriter, status int, data, meta any) {
	if meta == nil {
		meta = map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{Data: data, Meta: meta, Error: nil})
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{Data: nil, Meta: map[string]any{}, Error: map[string]string{"code": code, "message": message}})
}
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-HHC-Request-ID"))
		if id == "" {
			id = strconv.FormatInt(time.Now().UnixNano(), 36)
		}
		w.Header().Set("X-HHC-Request-ID", id)
		next.ServeHTTP(w, r)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/health") || r.URL.Path == "/ready" {
			next.ServeHTTP(w, r)
			return
		}
		started := time.Now()
		response := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(response, r)
		if response.status == 0 {
			response.status = http.StatusOK
		}
		slog.Info("http request",
			"request_id", w.Header().Get("X-HHC-Request-ID"),
			"method", r.Method,
			"path", r.URL.Path,
			"status", response.status,
			"duration_ms", time.Since(started).Milliseconds(),
		)
	})
}
