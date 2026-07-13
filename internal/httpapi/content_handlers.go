package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
)

func (h *Handler) contentRoutes(public, admin *http.ServeMux) {
	public.HandleFunc("GET /api/news", h.publicContent(content.ModuleNews, 20))
	public.HandleFunc("GET /api/history", h.publicContent(content.ModuleHistory, 100))
	public.HandleFunc("GET /api/videos", h.publicContent(content.ModuleVideos, 100))
	public.HandleFunc("GET /api/home", h.publicHome)
	admin.HandleFunc("GET /api/admin/content/{module}", requireScope("cms:read", h.adminContentList))
	admin.HandleFunc("POST /api/admin/content/{module}", requireScope("cms:write", h.adminContentCreate))
	admin.HandleFunc("GET /api/admin/content/{module}/{contentID}", requireScope("cms:read", h.adminContentGet))
	admin.HandleFunc("PUT /api/admin/content/{module}/{contentID}", requireScope("cms:write", h.adminContentUpdate))
	admin.HandleFunc("POST /api/admin/content/{module}/{contentID}/publish", requireScope("cms:publish", h.adminContentPublish))
	admin.HandleFunc("POST /api/admin/content/{module}/{contentID}/unpublish", requireScope("cms:publish", h.adminContentUnpublish))
	admin.HandleFunc("GET /api/admin/content/{module}/{contentID}/revisions", requireScope("cms:read", h.adminContentRevisions))
	admin.HandleFunc("POST /api/admin/content/{module}/{contentID}/revisions/{revision}/restore", requireScope("cms:write", h.adminContentRestore))
}

func (h *Handler) publicContent(module content.Module, limit int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		values, err := h.content.PublicContent(r.Context(), module, locale(r), limit)
		if err != nil {
			handleContentError(w, err)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=300")
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
	writeData(w, http.StatusOK, map[string]any{"news": featuredNews(news, 3), "videos": eligibleVideos(videos, 3)}, nil)
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
func eligibleVideos(values []content.PublicItem, limit int) []content.PublicItem {
	eligible := make([]content.PublicItem, 0, limit)
	for _, value := range values {
		if value.HomeEligible {
			eligible = append(eligible, value)
		}
		if len(eligible) == limit {
			break
		}
	}
	return eligible
}

func (h *Handler) adminContentList(w http.ResponseWriter, r *http.Request) {
	page, size := pagination(r)
	value, err := h.content.ListContent(r.Context(), content.Module(r.PathValue("module")), page, size, r.URL.Query().Get("status"))
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
	if publish {
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
