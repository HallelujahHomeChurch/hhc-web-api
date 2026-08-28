package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/sitesettings"
)

func (h *Handler) siteSettingsRoutes(public, admin *http.ServeMux) {
	public.HandleFunc("GET /api/site-layout", h.publicSiteLayout)
	admin.HandleFunc("GET /api/admin/site-settings", requireScope("cms:read", h.adminSiteSettingsGet))
	admin.HandleFunc("PUT /api/admin/site-settings", requireScope("cms:write", h.adminSiteSettingsSave))
	admin.HandleFunc("POST /api/admin/site-settings/publish", requireScope("cms:publish", h.adminSiteSettingsPublish))
	admin.HandleFunc("POST /api/admin/site-settings/unpublish", requireScope("cms:publish", h.adminSiteSettingsUnpublish))
	admin.HandleFunc("GET /api/admin/site-settings/revisions", requireScope("cms:read", h.adminSiteSettingsRevisions))
	admin.HandleFunc("POST /api/admin/site-settings/revisions/{revision}/restore", requireScope("cms:write", h.adminSiteSettingsRestore))
}

func privateNoStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) publicSiteLayout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=30, must-revalidate")
	value, err := h.siteSettings.Public(r.Context(), locale(r))
	if err != nil {
		handleSiteSettingsError(w, err)
		return
	}
	etag := fmt.Sprintf(`"site-layout-%d"`, value.Version)
	w.Header().Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeData(w, http.StatusOK, value, nil)
}

func (h *Handler) adminSiteSettingsGet(w http.ResponseWriter, r *http.Request) {
	value, err := h.siteSettings.Get(r.Context())
	if err != nil {
		handleSiteSettingsError(w, err)
		return
	}
	writeSiteSettings(w, value)
}

func (h *Handler) adminSiteSettingsSave(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input sitesettings.WriteInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.siteSettings.Save(r.Context(), input, expected, actor(r))
	if err != nil {
		handleSiteSettingsError(w, err)
		return
	}
	writeSiteSettings(w, value)
}

func (h *Handler) adminSiteSettingsPublish(w http.ResponseWriter, r *http.Request) {
	h.siteSettingsPublication(w, r, true)
}

func (h *Handler) adminSiteSettingsUnpublish(w http.ResponseWriter, r *http.Request) {
	h.siteSettingsPublication(w, r, false)
}

func (h *Handler) siteSettingsPublication(w http.ResponseWriter, r *http.Request, publish bool) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var value sitesettings.Settings
	var err error
	if publish {
		value, err = h.siteSettings.Publish(r.Context(), expected, actor(r))
	} else {
		value, err = h.siteSettings.Unpublish(r.Context(), expected, actor(r))
	}
	if err != nil {
		handleSiteSettingsError(w, err)
		return
	}
	writeSiteSettings(w, value)
}

func (h *Handler) adminSiteSettingsRevisions(w http.ResponseWriter, r *http.Request) {
	values, err := h.siteSettings.Revisions(r.Context())
	if err != nil {
		handleSiteSettingsError(w, err)
		return
	}
	writeData(w, http.StatusOK, values, nil)
}

func (h *Handler) adminSiteSettingsRestore(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	revision, err := strconv.ParseInt(r.PathValue("revision"), 10, 64)
	if err != nil || revision < 1 {
		handleSiteSettingsError(w, sitesettings.ErrInvalid)
		return
	}
	value, err := h.siteSettings.Restore(r.Context(), revision, expected, actor(r))
	if err != nil {
		handleSiteSettingsError(w, err)
		return
	}
	writeSiteSettings(w, value)
}

func writeSiteSettings(w http.ResponseWriter, value sitesettings.Settings) {
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}

func handleSiteSettingsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sitesettings.ErrInvalid):
		writeError(w, http.StatusUnprocessableEntity, "invalid_request", "The site settings are invalid.")
	case errors.Is(err, sitesettings.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "The site settings were not found.")
	case errors.Is(err, sitesettings.ErrPrecondition):
		writeError(w, http.StatusConflict, "version_conflict", "The site settings changed. Reload and try again.")
	case errors.Is(err, sitesettings.ErrNotPublishable):
		writeError(w, http.StatusUnprocessableEntity, "not_publishable", "The site settings are not ready for this operation.")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "The site settings request could not be completed.")
	}
}
