package httpapi

import (
	"crypto/sha256"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
)

func (h *Handler) contentRoutes(public, admin *http.ServeMux) {
	public.HandleFunc("GET /api/news", h.publicContent(content.ModuleNews, 20))
	public.HandleFunc("GET /api/news/{slug}", h.publicNews)
	public.HandleFunc("GET /api/history", h.publicContent(content.ModuleHistory, 100))
	public.HandleFunc("GET /api/videos", h.publicContent(content.ModuleVideos, 100))
	public.HandleFunc("GET /api/home", h.publicHome)
	admin.HandleFunc("GET /api/admin/content/{module}", requireScope("cms:read", h.adminContentList))
	admin.HandleFunc("POST /api/admin/content/{module}", requireScope("cms:write", h.adminContentCreate))
	admin.HandleFunc("GET /api/admin/content/{module}/{contentID}", requireScope("cms:read", h.adminContentGet))
	admin.HandleFunc("PUT /api/admin/content/{module}/{contentID}", requireScope("cms:write", h.adminContentUpdate))
	admin.HandleFunc("POST /api/admin/content/{module}/{contentID}/publish", requireScope("cms:publish", h.adminContentPublish))
	admin.HandleFunc("POST /api/admin/content/{module}/{contentID}/unpublish", requireScope("cms:publish", h.adminContentUnpublish))
	admin.HandleFunc("POST /api/admin/content/{module}/{contentID}/archive", requireScope("cms:write", h.adminContentArchive))
	admin.HandleFunc("POST /api/admin/content/{module}/{contentID}/restore", requireScope("cms:write", h.adminContentRestoreArchived))
	admin.HandleFunc("GET /api/admin/content/{module}/{contentID}/revisions", requireScope("cms:read", h.adminContentRevisions))
	admin.HandleFunc("POST /api/admin/content/{module}/{contentID}/revisions/{revision}/restore", requireScope("cms:write", h.adminContentRestore))
	admin.HandleFunc("POST /api/admin/content/news/{contentID}/upload-sessions", requireScopes([]string{"cms:write", "assets:write"}, h.adminNewsCoverUpload))
	admin.HandleFunc("POST /api/admin/content/news/{contentID}/assets/{assetID}/complete", requireScopes([]string{"cms:write", "assets:write"}, h.adminNewsCoverComplete))
	admin.HandleFunc("GET /api/admin/content/news/{contentID}/assets/{assetID}", requireScope("cms:read", h.adminNewsCoverStatus))
	admin.HandleFunc("POST /api/admin/content/news/{contentID}/assets/{assetID}/scan/retry", requireScopes([]string{"cms:write", "assets:write"}, h.adminNewsCoverRetry))
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

func (h *Handler) publicContent(module content.Module, limit int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		values, err := h.content.PublicContent(r.Context(), module, locale(r), limit)
		if err != nil {
			handleContentError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=30, must-revalidate")
		writeData(w, http.StatusOK, values, nil)
	}
}

func (h *Handler) publicHome(w http.ResponseWriter, r *http.Request) {
	requestedLocale := locale(r)
	news, err := h.content.PublicContent(r.Context(), content.ModuleNews, requestedLocale, 20)
	if err != nil {
		handleContentError(w, err)
		return
	}
	videos, err := h.content.PublicContent(r.Context(), content.ModuleVideos, requestedLocale, 100)
	if err != nil {
		handleContentError(w, err)
		return
	}
	seed := time.Now().UTC().Format("2006-01-02") + ":" + requestedLocale
	w.Header().Set("Cache-Control", "public, max-age=30, must-revalidate")
	writeData(w, http.StatusOK, map[string]any{"news": featuredNews(news, 3), "videos": eligibleVideos(videos, 3, seed)}, nil)
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

func featuredNews(values []content.PublicItem, limit int) []content.PublicItem {
	featured := make([]content.PublicItem, 0, limit)
	for _, value := range values {
		if value.Featured {
			featured = append(featured, value)
		}
	}
	if len(featured) < limit {
		for _, value := range values {
			if !value.Featured {
				featured = append(featured, value)
			}
			if len(featured) == limit {
				break
			}
		}
	}
	if len(featured) > limit {
		featured = featured[:limit]
	}
	return featured
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
	value, err := h.content.ListContent(r.Context(), content.Module(r.PathValue("module")), content.ListOptions{
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
	writeData(w, http.StatusOK, value.Items, map[string]any{"page": value.Page, "pageSize": value.PageSize, "total": value.Total})
}
func (h *Handler) adminContentCreate(w http.ResponseWriter, r *http.Request) {
	var input content.WriteInput
	if !decode(w, r, &input) {
		return
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
	writeContentItem(w, value)
}
func (h *Handler) adminContentUpdate(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input content.WriteInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.content.UpdateContent(r.Context(), content.Module(r.PathValue("module")), r.PathValue("contentID"), expected, input, actor(r))
	if err != nil {
		handleContentError(w, err)
		return
	}
	writeContentItem(w, value)
}
func (h *Handler) adminContentPublish(w http.ResponseWriter, r *http.Request) {
	h.changeContentPublication(w, r, true)
}
func (h *Handler) adminContentUnpublish(w http.ResponseWriter, r *http.Request) {
	h.changeContentPublication(w, r, false)
}
func (h *Handler) changeContentPublication(w http.ResponseWriter, r *http.Request, publish bool) {
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
			if h.uploads == nil || current.CoverAssetID == "" {
				handleContentError(w, content.ErrNotPublishable)
				return
			}
			asset, assetErr := h.uploads.Get(r.Context(), current.CoverAssetID)
			if assetErr != nil || asset.Namespace != "cms.news.cover" || asset.OwnerService != "hhc-web-api" || asset.OwnerType != "news" || asset.OwnerID != id || !assetCanEnterPublication(asset) {
				handleContentError(w, content.ErrNotPublishable)
				return
			}
			value, err = h.content.PublishContent(r.Context(), module, id, expected, actor(r))
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
	if !validImageMIME(input.MIMEType) || strings.TrimSpace(input.FileName) == "" || input.SizeBytes < 1 || input.SizeBytes > 10<<20 || strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		handleContentError(w, content.ErrInvalid)
		return
	}
	if _, err := h.content.GetContent(r.Context(), content.ModuleNews, r.PathValue("contentID")); err != nil {
		handleContentError(w, err)
		return
	}
	created, err := h.uploads.CreateNewsCoverUpload(r.Context(), r.PathValue("contentID"), input.FileName, input.MIMEType, input.SizeBytes, r.Header.Get("Idempotency-Key"))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "asset_service_unavailable", "The upload session could not be created.")
		return
	}
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
	write.CoverAssetID = asset.ID
	updated, err := h.content.UpdateContent(r.Context(), content.ModuleNews, current.ID, expected, write, actor(r))
	if err != nil {
		handleContentError(w, err)
		return
	}
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
	return asset.ID == assetID && asset.Namespace == "cms.news.cover" && asset.OwnerService == "hhc-web-api" && asset.OwnerType == "news" && asset.OwnerID == contentID
}
func validImageMIME(value string) bool {
	return value == "image/jpeg" || value == "image/png" || value == "image/webp"
}
func writeInput(item content.Item) content.WriteInput {
	return content.WriteInput{Slug: item.Slug, DisplayDate: item.DisplayDate, SortOrder: item.SortOrder, YouTubeVideoID: item.YouTubeVideoID, CoverAssetID: item.CoverAssetID, Featured: item.Featured, HomeEligible: item.HomeEligible, Translations: item.Translations}
}
func (h *Handler) adminContentRevisions(w http.ResponseWriter, r *http.Request) {
	values, err := h.content.ContentRevisions(r.Context(), content.Module(r.PathValue("module")), r.PathValue("contentID"))
	if err != nil {
		handleContentError(w, err)
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
	value, err := h.content.RestoreContent(r.Context(), content.Module(r.PathValue("module")), r.PathValue("contentID"), revision, expected, actor(r))
	if err != nil {
		handleContentError(w, err)
		return
	}
	writeContentItem(w, value)
}
func (h *Handler) adminContentArchive(w http.ResponseWriter, r *http.Request) {
	h.changeContentArchive(w, r, true)
}
func (h *Handler) adminContentRestoreArchived(w http.ResponseWriter, r *http.Request) {
	h.changeContentArchive(w, r, false)
}
func (h *Handler) changeContentArchive(w http.ResponseWriter, r *http.Request, archive bool) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	module, id := content.Module(r.PathValue("module")), r.PathValue("contentID")
	var value content.Item
	var err error
	if archive {
		value, err = h.content.ArchiveContent(r.Context(), module, id, expected, actor(r))
	} else {
		value, err = h.content.RestoreArchivedContent(r.Context(), module, id, expected, actor(r))
	}
	if err != nil {
		handleContentError(w, err)
		return
	}
	writeContentItem(w, value)
}
func writeContentItem(w http.ResponseWriter, value content.Item) {
	w.Header().Set("ETag", `"`+strconv.FormatInt(value.Version, 10)+`"`)
	writeData(w, http.StatusOK, value, nil)
}
func handleContentError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, content.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_content", "The content is invalid.")
	case errors.Is(err, content.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "The content was not found.")
	case errors.Is(err, content.ErrConflict):
		writeError(w, http.StatusConflict, "content_conflict", "The content conflicts with an existing record.")
	case errors.Is(err, content.ErrPrecondition):
		writeError(w, http.StatusPreconditionFailed, "precondition_failed", "The content changed. Reload and try again.")
	case errors.Is(err, content.ErrNotPublishable):
		writeError(w, http.StatusUnprocessableEntity, "not_publishable", "The content is not ready to publish.")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "The request could not be completed.")
	}
}
