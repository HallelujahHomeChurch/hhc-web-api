package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/google/uuid"
)

func (h *Handler) contentRoutes(public, admin *http.ServeMux) {
	public.HandleFunc("GET /api/statements/active", h.publicActiveStatement)
	public.HandleFunc("GET /api/news", h.publicContent(content.ModuleNews))
	public.HandleFunc("GET /api/news/{slug}", h.publicNews)
	public.HandleFunc("GET /api/history", h.publicContent(content.ModuleHistory))
	public.HandleFunc("GET /api/videos", h.publicContent(content.ModuleVideos))
	public.HandleFunc("GET /api/locations", h.publicLocations)
	public.HandleFunc("GET /api/pages/{pageKey}", h.publicEditorialPage)
	public.HandleFunc("GET /api/home", h.publicHome)
	admin.HandleFunc("GET /api/admin/content/{module}", requireContentScope("read", h.adminAuditProvenance(h.adminContentList)))
	admin.HandleFunc("POST /api/admin/content/{module}", requireContentScope("write", h.adminAuditProvenance(h.adminContentCreate)))
	admin.HandleFunc("GET /api/admin/content/{module}/{contentID}", requireContentScope("read", h.adminAuditProvenance(h.adminContentGet)))
	admin.HandleFunc("PUT /api/admin/content/{module}/{contentID}", requireContentScope("write", h.adminAuditProvenance(h.adminContentUpdate)))
	admin.HandleFunc("POST /api/admin/content/news/{contentID}/statement-popup/end", requireScope("cms:news:publish", h.adminAuditProvenance(h.endStatementPopup)))
	admin.HandleFunc("POST /api/admin/content/{module}/{contentID}/publish", requireContentScope("publish", h.adminAuditProvenance(h.adminContentPublish)))
	admin.HandleFunc("POST /api/admin/content/{module}/{contentID}/unpublish", requireContentScope("publish", h.adminAuditProvenance(h.adminContentUnpublish)))
	admin.HandleFunc("DELETE /api/admin/content/{module}/{contentID}", requireContentScope("write", h.adminAuditProvenance(h.adminContentDelete)))
	admin.HandleFunc("GET /api/admin/content/{module}/{contentID}/revisions", requireContentScope("read", h.adminAuditProvenance(h.adminContentRevisions)))
	admin.HandleFunc("POST /api/admin/content/{module}/{contentID}/revisions/{revision}/restore", requireContentScope("write", h.adminAuditProvenance(h.adminContentRestore)))
	admin.HandleFunc("POST /api/admin/content/news/{contentID}/upload-sessions", requireScope("cms:news:write", h.adminNewsCoverUpload))
	admin.HandleFunc("POST /api/admin/content/news/{contentID}/assets/{assetID}/complete", requireScope("cms:news:write", h.adminAuditProvenance(h.adminNewsCoverComplete)))
	admin.HandleFunc("GET /api/admin/content/news/{contentID}/assets/{assetID}", requireScope("cms:news:read", h.adminAuditProvenance(h.adminNewsCoverStatus)))
	admin.HandleFunc("GET /api/admin/content/news/{contentID}/assets/{assetID}/preview", requireScope("cms:news:read", h.adminNewsImagePreview))
	admin.HandleFunc("POST /api/admin/content/news/{contentID}/assets/{assetID}/scan/retry", requireScope("cms:news:write", h.adminNewsCoverRetry))
	admin.HandleFunc("POST /api/admin/content/pages/{contentID}/upload-sessions", requireScope("cms:pages:write", h.adminHomeBannerUpload))
	admin.HandleFunc("POST /api/admin/content/pages/{contentID}/assets/{assetID}/complete", requireScope("cms:pages:write", h.adminAuditProvenance(h.adminHomeBannerComplete)))
	admin.HandleFunc("GET /api/admin/content/pages/{contentID}/assets/{assetID}", requireScope("cms:pages:read", h.adminAuditProvenance(h.adminHomeBannerStatus)))
	admin.HandleFunc("POST /api/admin/content/pages/{contentID}/assets/{assetID}/scan/retry", requireScope("cms:pages:write", h.adminHomeBannerRetry))
}

func (h *Handler) publicEditorialPage(w http.ResponseWriter, r *http.Request) {
	value, etag, err := h.content.PublicEditorialPage(r.Context(), r.PathValue("pageKey"), locale(r))
	if err != nil {
		if errors.Is(err, content.ErrNotFound) {
			w.Header().Set("Cache-Control", "public, max-age=30, must-revalidate")
		}
		handleContentError(w, err)
		return
	}
	etag = `"` + etag + `"`
	w.Header().Set("Cache-Control", "public, max-age=30, must-revalidate")
	w.Header().Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeData(w, http.StatusOK, value, nil)
}

func (h *Handler) publicNews(w http.ResponseWriter, r *http.Request) {
	value, etag, err := h.content.PublicNews(r.Context(), locale(r), r.PathValue("slug"))
	if err != nil {
		if errors.Is(err, content.ErrNotFound) {
			w.Header().Set("Cache-Control", "public, max-age=30")
		}
		handleContentError(w, err)
		return
	}
	etag = `"` + etag + `"`
	w.Header().Set("Cache-Control", "public, no-cache")
	w.Header().Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeData(w, http.StatusOK, value, nil)
}

func (h *Handler) publicContent(module content.Module) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page, size := pagination(r)
		values, err := h.content.PublicContent(r.Context(), module, locale(r), page, size)
		if err != nil {
			handleContentError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=30, must-revalidate")
		writeData(w, http.StatusOK, values.Items, map[string]any{"page": values.Page, "pageSize": values.PageSize, "total": values.Total})
	}
}

func (h *Handler) publicLocations(w http.ResponseWriter, r *http.Request) {
	values, err := h.content.PublicLocations(r.Context(), locale(r))
	if err != nil {
		handleContentError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=30, must-revalidate")
	writeData(w, http.StatusOK, values, nil)
}

func (h *Handler) publicHome(w http.ResponseWriter, r *http.Request) {
	requestedLocale := locale(r)
	news, err := h.content.PublicContent(r.Context(), content.ModuleNews, requestedLocale, 1, 20)
	if err != nil {
		handleContentError(w, err)
		return
	}
	videos, err := h.content.PublicContent(r.Context(), content.ModuleVideos, requestedLocale, 1, 100)
	if err != nil {
		handleContentError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=30, must-revalidate")
	writeData(w, http.StatusOK, map[string]any{"news": latestNews(news.Items, 4), "videos": eligibleVideos(videos.Items, 3, homeVideoSeed(time.Now()))}, nil)
}

var taipei = time.FixedZone("Asia/Taipei", 8*60*60)

func homeVideoSeed(now time.Time) string {
	return now.In(taipei).Format("2006-01-02")
}

func etagMatches(header, target string) bool {
	target = strings.TrimPrefix(target, "W/")
	for _, candidate := range strings.Split(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == target {
			return true
		}
	}
	return false
}

func latestNews(values []content.PublicItem, limit int) []content.PublicItem {
	if len(values) > limit {
		values = values[:limit]
	}
	return values
}
func eligibleVideos(values []content.PublicItem, limit int, seed string) []content.PublicItem {
	eligible := make([]content.PublicItem, 0, len(values))
	for _, value := range values {
		if value.HomeEligible {
			eligible = append(eligible, value)
		}
	}
	sort.Slice(eligible, func(i, j int) bool {
		left := sha256.Sum256([]byte(seed + ":" + eligible[i].ID))
		right := sha256.Sum256([]byte(seed + ":" + eligible[j].ID))
		if left == right {
			return eligible[i].ID < eligible[j].ID
		}
		return string(left[:]) < string(right[:])
	})
	if len(eligible) > limit {
		eligible = eligible[:limit]
	}
	return eligible
}

func (h *Handler) adminContentList(w http.ResponseWriter, r *http.Request) {
	page, size := pagination(r)
	value, err := h.content.ListContent(r.Context(), content.Module(r.PathValue("module")), content.ListOptions{Kind: r.URL.Query().Get("kind"),
		Query:     r.URL.Query().Get("q"),
		Status:    r.URL.Query().Get("status"),
		Sort:      r.URL.Query().Get("sort"),
		Direction: r.URL.Query().Get("direction"),
		Page:      page,
		PageSize:  size,
	})
	if err != nil {
		handleContentError(w, err)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.content.list", "", contentAuditMetadata(content.Module(r.PathValue("module")))) {
		return
	}
	writeData(w, http.StatusOK, value.Items, map[string]any{"page": value.Page, "pageSize": value.PageSize, "total": value.Total})
}
func (h *Handler) adminContentCreate(w http.ResponseWriter, r *http.Request) {
	var input content.WriteInput
	if !decode(w, r, &input) {
		return
	}
	if input.Kind == content.KindStatement {
		if references, err := content.StatementImageRefs(input.Translations); err != nil || len(references) != 0 {
			handleContentError(w, content.ErrInvalid)
			return
		}
	}
	value, err := h.content.CreateContent(r.Context(), content.Module(r.PathValue("module")), input, actor(r), r.Header.Get("Idempotency-Key"))
	if err != nil {
		handleContentError(w, err)
		return
	}
	w.Header().Set("ETag", `"1"`)
	writeData(w, http.StatusCreated, value, nil)
}
func (h *Handler) adminContentGet(w http.ResponseWriter, r *http.Request) {
	value, err := h.content.GetContent(r.Context(), content.Module(r.PathValue("module")), r.PathValue("contentID"))
	if err != nil {
		handleContentError(w, err)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.content.read", value.ID, contentAuditMetadata(value.Module)) {
		return
	}
	value.OperationProgress = h.contentOperationProgress(r.Context(), value, time.Now().UTC())
	writeContentItem(w, value)
}

func contentAuditMetadata(module content.Module) auditclient.Metadata {
	group := "pages"
	if module == content.ModuleNews {
		group = "news"
	}
	return auditclient.Metadata{"module": group}
}

func (h *Handler) adminContentUpdate(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var request contentWriteRequest
	if !decode(w, r, &request) {
		return
	}
	if content.Module(r.PathValue("module")) == content.ModulePages && !request.indexablePresent {
		writeError(w, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return
	}
	input := request.WriteInput
	if content.Module(r.PathValue("module")) == content.ModuleNews && input.Kind == content.KindStatement {
		if !h.validateStatementAssets(w, r, r.PathValue("contentID"), input.Translations) {
			return
		}
	}
	value, err := h.content.UpdateContent(r.Context(), content.Module(r.PathValue("module")), r.PathValue("contentID"), expected, input, actor(r))
	if err != nil {
		handleContentError(w, err)
		return
	}
	writeContentItem(w, value)
}

type contentWriteRequest struct {
	content.WriteInput
	indexablePresent bool
}

func (request *contentWriteRequest) UnmarshalJSON(raw []byte) error {
	type writeInput content.WriteInput
	var value writeInput
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return content.ErrInvalid
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	request.WriteInput = content.WriteInput(value)
	_, startPresent := fields["popupStartsAt"]
	_, endPresent := fields["popupEndsAt"]
	request.PopupWindowSet = startPresent || endPresent
	indexable, present := fields["indexable"]
	if present && !bytes.Equal(bytes.TrimSpace(indexable), []byte("true")) && !bytes.Equal(bytes.TrimSpace(indexable), []byte("false")) {
		return content.ErrInvalid
	}
	request.indexablePresent = present
	return nil
}
func (h *Handler) adminContentPublish(w http.ResponseWriter, r *http.Request) {
	h.changeContentPublication(w, r, true)
}
func (h *Handler) adminContentUnpublish(w http.ResponseWriter, r *http.Request) {
	h.changeContentPublication(w, r, false)
}
func (h *Handler) changeContentPublication(w http.ResponseWriter, r *http.Request, publish bool) {
	var options struct {
		StatementNotification *content.StatementNotificationRequest `json:"statementNotification,omitempty"`
	}
	if publish && r.Body != nil && r.Body != http.NoBody {
		// Proxies can forward an empty stream with unknown length instead of NoBody.
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
		decoder.DisallowUnknownFields()
		err := decoder.Decode(&options)
		if err != nil && err != io.EOF {
			writeError(w, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
			return
		}
		if err == nil && decoder.Decode(new(any)) != io.EOF {
			writeError(w, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
			return
		}
	}
	if options.StatementNotification != nil {
		allowed := false
		requireScope("campaigns:send", func(http.ResponseWriter, *http.Request) { allowed = true })(w, r)
		if !allowed {
			return
		}
		if r.PathValue("module") != "news" || !options.StatementNotification.Valid() {
			handleContentError(w, content.ErrInvalid)
			return
		}
	}

	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	module, id := content.Module(r.PathValue("module")), r.PathValue("contentID")
	var value content.Item
	var err error
	if module == content.ModuleNews {
		if publish {
			current, getErr := h.content.GetContent(r.Context(), module, id)
			if getErr != nil {
				handleContentError(w, getErr)
				return
			}
			if current.Kind != content.KindStatement || current.CoverAssetID != "" {
				if h.uploads == nil || current.CoverAssetID == "" {
					handleContentError(w, content.ErrNotPublishable)
					return
				}
				asset, assetErr := h.uploads.Get(r.Context(), current.CoverAssetID)
				if assetErr != nil || asset.Namespace != "cms.news.cover" || asset.OwnerService != "hhc-web-api" || asset.OwnerType != "news" || asset.OwnerID != id || !assetCanEnterPublication(asset) {
					handleContentError(w, content.ErrNotPublishable)
					return
				}
			}
			if options.StatementNotification != nil {
				value, err = h.content.PublishStatement(r.Context(), id, expected, actor(r), *options.StatementNotification)
			} else {
				value, err = h.content.PublishContent(r.Context(), module, id, expected, actor(r))
			}
		} else {
			value, err = h.content.UnpublishContent(r.Context(), module, id, expected, actor(r))
		}
	} else if publish {
		value, err = h.content.PublishContent(r.Context(), module, id, expected, actor(r))
	} else {
		value, err = h.content.UnpublishContent(r.Context(), module, id, expected, actor(r))
	}
	if err != nil {
		handleContentError(w, err)
		return
	}
	writeContentItem(w, value)
}

func assetCanEnterPublication(asset assetclient.Asset) bool {
	if asset.UploadStatus != "completed" || (asset.ScanStatus != "pending" && asset.ScanStatus != "clean") {
		return false
	}
	return asset.ProcessingStatus == "pending" || asset.ProcessingStatus == "ready" || asset.ProcessingStatus == "not_required"
}

type newsCoverUploadInput struct {
	Usage     string `json:"usage,omitempty"`
	FileName  string `json:"fileName"`
	MIMEType  string `json:"mimeType"`
	SizeBytes int64  `json:"sizeBytes"`
}
type newsCoverCompleteInput struct {
	FileName       string `json:"fileName"`
	MIMEType       string `json:"mimeType"`
	SizeBytes      int64  `json:"sizeBytes"`
	ChecksumSHA256 string `json:"checksumSha256"`
}

func (h *Handler) adminNewsCoverUpload(w http.ResponseWriter, r *http.Request) {
	if h.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset uploads are unavailable.")
		return
	}
	var input newsCoverUploadInput
	if !decode(w, r, &input) {
		return
	}
	purpose, ok := newsImagePurpose(input.Usage)
	if !ok {
		handleContentError(w, content.ErrInvalid)
		return
	}
	if !validImageMIME(input.MIMEType) || strings.TrimSpace(input.FileName) == "" || input.SizeBytes < 1 || input.SizeBytes > 10<<20 || strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		handleContentError(w, content.ErrInvalid)
		return
	}
	current, err := h.content.GetContent(r.Context(), content.ModuleNews, r.PathValue("contentID"))
	if err != nil {
		handleContentError(w, err)
		return
	}
	if purpose == "statement_inline" && current.Kind != content.KindStatement {
		handleContentError(w, content.ErrInvalid)
		return
	}
	created, err := h.uploads.CreateNewsCoverUpload(r.Context(), r.PathValue("contentID"), purpose, input.FileName, input.MIMEType, input.SizeBytes, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "The upload session could not be created.")
		return
	}
	logAssetEvent(r, "asset upload session created", "news", r.PathValue("contentID"), created.Asset.ID)
	writeData(w, http.StatusCreated, created, nil)
}
func (h *Handler) adminNewsCoverComplete(w http.ResponseWriter, r *http.Request) {
	if h.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset uploads are unavailable.")
		return
	}
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input newsCoverCompleteInput
	if !decode(w, r, &input) {
		return
	}
	if !validImageMIME(input.MIMEType) || input.SizeBytes < 1 || len(input.ChecksumSHA256) != 64 {
		handleContentError(w, content.ErrInvalid)
		return
	}
	assetID := r.PathValue("assetID")
	asset, err := h.uploads.Get(r.Context(), assetID)
	if errors.Is(err, assetclient.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset uploads are unavailable.")
		return
	}
	if err != nil || !ownedNewsAsset(asset, r.PathValue("contentID"), assetID) {
		writeError(w, http.StatusForbidden, "asset_owner_mismatch", "The uploaded asset does not belong to this news item.")
		return
	}
	asset, err = h.uploads.CompleteUpload(r.Context(), assetID, assetclient.CompleteUploadInput{SizeBytes: input.SizeBytes, ChecksumSHA256: input.ChecksumSHA256, MIMEType: input.MIMEType})
	if errors.Is(err, assetclient.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset uploads are unavailable.")
		return
	}
	if err != nil || !ownedNewsAsset(asset, r.PathValue("contentID"), r.PathValue("assetID")) {
		writeError(w, http.StatusForbidden, "asset_owner_mismatch", "The uploaded asset does not belong to this news item.")
		return
	}
	current, err := h.content.GetContent(r.Context(), content.ModuleNews, r.PathValue("contentID"))
	if err != nil {
		handleContentError(w, err)
		return
	}
	write := writeInput(current)
	switch asset.Purpose {
	case "", "news_detail_cover", "news_cover":
		write.CoverAssetID = asset.ID
	case "news_home_cover":
		write.HomeCoverAssetID = asset.ID
	case "statement_inline":
		if current.Kind != content.KindStatement || current.Version != expected {
			handleContentError(w, content.ErrPrecondition)
			return
		}
		if _, err = h.uploads.CreateServiceGrant(r.Context(), asset.ID, "statement-inline:"+asset.ID); err != nil {
			writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Image preview is unavailable.")
			return
		}
		logAssetEvent(r, "asset upload completed", "news", current.ID, asset.ID)
		writeContentItem(w, current)
		return
	default:
		writeError(w, http.StatusForbidden, "asset_purpose_mismatch", "The uploaded asset has an unsupported purpose.")
		return
	}
	updated, err := h.content.UpdateContent(r.Context(), content.ModuleNews, current.ID, expected, write, actor(r))
	if err != nil {
		handleContentError(w, err)
		return
	}
	logAssetEvent(r, "asset attached", "news", r.PathValue("contentID"), asset.ID)
	writeContentItem(w, updated)
}
func (h *Handler) adminNewsCoverStatus(w http.ResponseWriter, r *http.Request) {
	if h.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset status is unavailable.")
		return
	}
	asset, err := h.uploads.Get(r.Context(), r.PathValue("assetID"))
	if errors.Is(err, assetclient.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset status is unavailable.")
		return
	}
	if err != nil || !ownedNewsAsset(asset, r.PathValue("contentID"), r.PathValue("assetID")) {
		writeError(w, http.StatusNotFound, "not_found", "The cover image was not found.")
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.content.asset_status.read", asset.ID, contentAuditMetadata(content.ModuleNews)) {
		return
	}
	writeData(w, http.StatusOK, cmsAssetStatus(asset), nil)
}
func (h *Handler) adminNewsCoverRetry(w http.ResponseWriter, r *http.Request) {
	if h.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset status is unavailable.")
		return
	}
	asset, err := h.uploads.Get(r.Context(), r.PathValue("assetID"))
	if errors.Is(err, assetclient.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset status is unavailable.")
		return
	}
	if err != nil || !ownedNewsAsset(asset, r.PathValue("contentID"), r.PathValue("assetID")) {
		writeError(w, http.StatusNotFound, "not_found", "The cover image was not found.")
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
func ownedNewsAsset(asset assetclient.Asset, contentID, assetID string) bool {
	return asset.ID == assetID && asset.Namespace == "cms.news.cover" && asset.OwnerService == "hhc-web-api" && asset.OwnerType == "news" && asset.OwnerID == contentID && validNewsImagePurpose(asset.Purpose)
}

func (h *Handler) validateStatementAssets(w http.ResponseWriter, r *http.Request, contentID string, translations []content.Translation) bool {
	references, err := content.StatementImageRefs(translations)
	if err != nil {
		handleContentError(w, content.ErrInvalid)
		return false
	}
	if len(references) == 0 {
		return true
	}
	if h.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset validation is unavailable.")
		return false
	}
	seen := map[string]bool{}
	for _, reference := range references {
		if seen[reference.AssetID] {
			continue
		}
		seen[reference.AssetID] = true
		asset, err := h.uploads.Get(r.Context(), reference.AssetID)
		if errors.Is(err, assetclient.ErrUnavailable) {
			writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset validation is unavailable.")
			return false
		}
		if err != nil || !ownedNewsAsset(asset, contentID, reference.AssetID) || asset.Purpose != "statement_inline" {
			writeError(w, http.StatusForbidden, "asset_owner_mismatch", "An inline image does not belong to this Statement.")
			return false
		}
	}
	return true
}

func (h *Handler) adminNewsImagePreview(w http.ResponseWriter, r *http.Request) {
	if h.uploads == nil || h.downloads == nil {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Image preview is unavailable.")
		return
	}
	asset, err := h.uploads.Get(r.Context(), r.PathValue("assetID"))
	if errors.Is(err, assetclient.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Image preview is unavailable.")
		return
	}
	if err != nil || !ownedNewsAsset(asset, r.PathValue("contentID"), r.PathValue("assetID")) || asset.Purpose != "statement_inline" {
		writeError(w, http.StatusNotFound, "not_found", "The image was not found.")
		return
	}
	if asset.UploadStatus != "completed" || asset.ScanStatus != "clean" || (asset.ProcessingStatus != "ready" && asset.ProcessingStatus != "not_required") {
		writeError(w, http.StatusConflict, "asset_not_ready", "The image is not ready to preview.")
		return
	}
	response, err := h.downloads.AuthorizedDownload(r.Context(), http.MethodGet, asset.ID, r.Header.Get("Range"), r.Header.Get("If-Range"))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Image preview is unavailable.")
		return
	}
	if response.StatusCode == http.StatusNotFound {
		response.Body.Close()
		if _, err = h.uploads.CreateServiceGrant(r.Context(), asset.ID, "statement-inline:"+asset.ID); err == nil {
			response, err = h.downloads.AuthorizedDownload(r.Context(), http.MethodGet, asset.ID, r.Header.Get("Range"), r.Header.Get("If-Range"))
		}
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Image preview is unavailable.")
			return
		}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent && response.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Image preview is unavailable.")
		return
	}
	for _, header := range []string{"Accept-Ranges", "Content-Length", "Content-Range", "Content-Type", "ETag", "Last-Modified"} {
		if value := response.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(response.Body, 10<<20))
}

func newsImagePurpose(usage string) (string, bool) {
	switch strings.TrimSpace(usage) {
	case "", "detail":
		return "news_detail_cover", true
	case "home":
		return "news_home_cover", true
	case "inline":
		return "statement_inline", true
	default:
		return "", false
	}
}

func validNewsImagePurpose(purpose string) bool {
	return purpose == "" || purpose == "news_cover" || purpose == "news_detail_cover" || purpose == "news_home_cover" || purpose == "statement_inline"
}
func validImageMIME(value string) bool {
	return value == "image/jpeg" || value == "image/png" || value == "image/webp"
}
func writeInput(item content.Item) content.WriteInput {
	return content.WriteInput{AuthorName: item.AuthorName, Slug: item.Slug, DisplayDate: item.DisplayDate, EventDate: item.EventDate, YouTubeVideoID: item.YouTubeVideoID, CoverAssetID: item.CoverAssetID, HomeCoverAssetID: item.HomeCoverAssetID, DetailLayout: item.DetailLayout, Featured: item.Featured, HomeEligible: item.HomeEligible, LocationKey: item.LocationKey, MapHref: item.MapHref, SortOrder: item.SortOrder, PageKey: item.PageKey, PageTemplate: item.PageTemplate, RoutePath: item.RoutePath, Indexable: item.Indexable, BannerAssetID: item.BannerAssetID, Links: item.Links, Locations: item.Locations, Translations: item.Translations}
}

type homeBannerUploadInput struct {
	Usage     string `json:"usage"`
	FileName  string `json:"fileName"`
	MIMEType  string `json:"mimeType"`
	SizeBytes int64  `json:"sizeBytes"`
}

type homeBannerCompleteInput struct {
	MIMEType       string `json:"mimeType"`
	SizeBytes      int64  `json:"sizeBytes"`
	ChecksumSHA256 string `json:"checksumSha256"`
}

func (h *Handler) adminHomeBannerUpload(w http.ResponseWriter, r *http.Request) {
	if h.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset uploads are unavailable.")
		return
	}
	var input homeBannerUploadInput
	if !decode(w, r, &input) {
		return
	}
	if input.Usage != "home_banner" || input.MIMEType != "image/jpeg" || strings.TrimSpace(input.FileName) == "" || input.SizeBytes < 1 || input.SizeBytes > 10<<20 || strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		handleContentError(w, content.ErrInvalid)
		return
	}
	if _, err := h.fixedHomeV2(r, r.PathValue("contentID")); err != nil {
		handleContentError(w, err)
		return
	}
	created, err := h.uploads.CreateHomeBannerUpload(r.Context(), r.PathValue("contentID"), input.FileName, input.MIMEType, input.SizeBytes, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "The upload session could not be created.")
		return
	}
	logAssetEvent(r, "asset upload session created", "page", r.PathValue("contentID"), created.Asset.ID)
	writeData(w, http.StatusCreated, created, nil)
}

func (h *Handler) adminHomeBannerComplete(w http.ResponseWriter, r *http.Request) {
	if h.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset uploads are unavailable.")
		return
	}
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input homeBannerCompleteInput
	if !decode(w, r, &input) {
		return
	}
	if input.MIMEType != "image/jpeg" || input.SizeBytes < 1 || len(input.ChecksumSHA256) != 64 {
		handleContentError(w, content.ErrInvalid)
		return
	}
	current, err := h.fixedHomeV2(r, r.PathValue("contentID"))
	if err != nil {
		handleContentError(w, err)
		return
	}
	assetID := r.PathValue("assetID")
	asset, err := h.uploads.Get(r.Context(), assetID)
	if errors.Is(err, assetclient.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset uploads are unavailable.")
		return
	}
	if err != nil || !ownedHomeBanner(asset, current.ID, assetID) || asset.ExpectedMIMEType != "image/jpeg" {
		writeError(w, http.StatusForbidden, "asset_owner_mismatch", "The uploaded asset does not belong to the Home page.")
		return
	}
	asset, err = h.uploads.CompleteUpload(r.Context(), assetID, assetclient.CompleteUploadInput{SizeBytes: input.SizeBytes, ChecksumSHA256: input.ChecksumSHA256, MIMEType: input.MIMEType})
	if errors.Is(err, assetclient.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset uploads are unavailable.")
		return
	}
	if err != nil || !ownedHomeBanner(asset, current.ID, assetID) || asset.ExpectedMIMEType != "image/jpeg" || asset.DetectedMIMEType != "image/jpeg" || asset.UploadStatus != "completed" || asset.ProcessingStatus != "not_required" {
		writeError(w, http.StatusForbidden, "asset_contract_mismatch", "The uploaded asset does not satisfy the Home Banner contract.")
		return
	}
	inputWrite := writeInput(current)
	inputWrite.BannerAssetID = asset.ID
	updated, err := h.content.UpdateContent(r.Context(), content.ModulePages, current.ID, expected, inputWrite, actor(r))
	if err != nil {
		handleContentError(w, err)
		return
	}
	logAssetEvent(r, "asset attached", "page", current.ID, asset.ID)
	writeContentItem(w, updated)
}

func (h *Handler) adminHomeBannerStatus(w http.ResponseWriter, r *http.Request) {
	h.homeBannerStatusOrRetry(w, r, false)
}

func (h *Handler) adminHomeBannerRetry(w http.ResponseWriter, r *http.Request) {
	h.homeBannerStatusOrRetry(w, r, true)
}

func (h *Handler) homeBannerStatusOrRetry(w http.ResponseWriter, r *http.Request, retry bool) {
	if h.uploads == nil {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset status is unavailable.")
		return
	}
	current, err := h.fixedHomeV2(r, r.PathValue("contentID"))
	if err != nil {
		handleContentError(w, err)
		return
	}
	asset, err := h.uploads.Get(r.Context(), r.PathValue("assetID"))
	if errors.Is(err, assetclient.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "Asset status is unavailable.")
		return
	}
	if err != nil || !ownedHomeBanner(asset, current.ID, r.PathValue("assetID")) {
		writeError(w, http.StatusNotFound, "not_found", "The Home Banner was not found.")
		return
	}
	if retry {
		if asset.ScanStatus != "failed" {
			writeError(w, http.StatusConflict, "asset_not_retryable", "The asset scan cannot be retried.")
			return
		}
		if err := h.uploads.RequeueScan(r.Context(), asset.ID); err != nil {
			writeError(w, http.StatusConflict, "asset_not_retryable", "The asset scan state changed.")
			return
		}
		asset.ScanStatus = "pending"
	} else if !h.enqueueAdminAudit(w, r, "cms.content.asset_status.read", asset.ID, contentAuditMetadata(content.ModulePages)) {
		return
	}
	writeData(w, http.StatusOK, cmsAssetStatus(asset), nil)
}

func (h *Handler) fixedHomeV2(r *http.Request, id string) (content.Item, error) {
	item, err := h.content.GetContent(r.Context(), content.ModulePages, id)
	if err != nil {
		return content.Item{}, err
	}
	if item.ID != id || item.Module != content.ModulePages || item.PageKey != "home" || item.PageTemplate != "home.v2" || item.RoutePath != "/" {
		return content.Item{}, content.ErrInvalid
	}
	return item, nil
}

func ownedHomeBanner(asset assetclient.Asset, contentID, assetID string) bool {
	return asset.ID == assetID && asset.Namespace == "cms.home.banner" && asset.OwnerService == "hhc-web-api" && asset.OwnerType == "page" && asset.OwnerID == contentID && asset.Purpose == "home_banner"
}
func (h *Handler) adminContentRevisions(w http.ResponseWriter, r *http.Request) {
	values, err := h.content.ContentRevisions(r.Context(), content.Module(r.PathValue("module")), r.PathValue("contentID"))
	if err != nil {
		handleContentError(w, err)
		return
	}
	resourceID := r.PathValue("contentID")
	if parsed, parseErr := uuid.Parse(resourceID); parseErr == nil {
		resourceID = parsed.String()
	}
	if !h.enqueueAdminAudit(w, r, "cms.content.revisions.read", resourceID, contentAuditMetadata(content.Module(r.PathValue("module")))) {
		return
	}
	writeData(w, http.StatusOK, values, nil)
}
func (h *Handler) adminContentRestore(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	revision, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
	if err != nil {
		handleContentError(w, content.ErrInvalid)
		return
	}
	module, id := content.Module(r.PathValue("module")), r.PathValue("contentID")
	if module == content.ModuleNews {
		stored, loadErr := h.content.ContentRevision(r.Context(), module, id, revision)
		if loadErr != nil {
			handleContentError(w, loadErr)
			return
		}
		if stored.Snapshot.Kind == content.KindStatement && !h.validateStatementAssets(w, r, id, stored.Snapshot.Translations) {
			return
		}
	}
	value, err := h.content.RestoreContent(r.Context(), module, id, revision, expected, actor(r))
	if err != nil {
		handleContentError(w, err)
		return
	}
	writeContentItem(w, value)
}
func (h *Handler) adminContentDelete(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	module, id := content.Module(r.PathValue("module")), r.PathValue("contentID")
	if err := h.content.DeleteContent(r.Context(), module, id, expected, actor(r)); err != nil {
		handleContentError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func writeContentItem(w http.ResponseWriter, value content.Item) {
	now := time.Now().UTC()
	value.ServerNow = &now
	w.Header().Set("ETag", `"`+strconv.FormatInt(value.Version, 10)+`"`)
	writeData(w, http.StatusOK, value, nil)
}
func handleContentError(w http.ResponseWriter, err error) {
	var windowConflict *content.StatementWindowConflict
	switch {
	case errors.As(err, &windowConflict):
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(envelope{Meta: map[string]any{}, Error: map[string]string{"code": "statement_window_conflict", "message": windowConflict.Error(), "contentId": windowConflict.ContentID}})
	case errors.Is(err, content.ErrInvalidStatementWindow):
		writeError(w, http.StatusBadRequest, "invalid_statement_window", err.Error())
	case errors.Is(err, content.ErrDraftChangesPresent):
		writeError(w, http.StatusConflict, "draft_changes_present", err.Error())
	case errors.Is(err, content.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_content", "The content is invalid.")
	case errors.Is(err, content.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "The content was not found.")
	case errors.Is(err, content.ErrConflict):
		writeError(w, http.StatusConflict, "content_conflict", "The content conflicts with an existing record.")
	case errors.Is(err, content.ErrLocaleSetMismatch):
		writeError(w, http.StatusConflict, "locale_set_mismatch", "Existing locales must be submitted or explicitly deleted.")
	case errors.Is(err, content.ErrPrecondition):
		writeError(w, http.StatusPreconditionFailed, "precondition_failed", "The content changed. Reload and try again.")
	case errors.Is(err, content.ErrNotPublishable):
		writeError(w, http.StatusUnprocessableEntity, "not_publishable", "The content is not ready to publish.")
	case errors.Is(err, content.ErrSummaryProcessing):
		writeError(w, http.StatusConflict, "summary_processing", "News summaries must be ready before publication.")
	case errors.Is(err, content.ErrMethodNotAllowed):
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "This operation is not allowed for fixed pages.")
	case errors.Is(err, content.ErrAuditUnavailable):
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "Audit recording is unavailable.")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "The request could not be completed.")
	}
}

func (h *Handler) publicActiveStatement(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	result, err := h.content.ActiveStatement(r.Context(), locale(r))
	if err != nil {
		handleContentError(w, err)
		return
	}
	writeData(w, http.StatusOK, result, nil)
}

func (h *Handler) endStatementPopup(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	item, err := h.content.EndStatementPopup(r.Context(), r.PathValue("contentID"), expected, actor(r))
	if err != nil {
		handleContentError(w, err)
		return
	}
	writeContentItem(w, item)
}
