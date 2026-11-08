package httpapi

import (
	"fmt"
	"net/http"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
)

func (h *Handler) adminBulletinTemplateSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	value, err := h.service.BulletinTemplateSettings(r.Context())
	if err != nil {
		handleError(w, err)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.site_settings.read", "singleton", auditclient.Metadata{}) {
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}

func (h *Handler) adminSaveBulletinTemplateSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	writeError(w, http.StatusGone, "bulletin_template_settings_retired", "Cover values are managed by versioned conversion rules.")
}

func (h *Handler) adminBulletinTemplateSettingsHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	values, err := h.service.BulletinTemplateSettingsHistory(r.Context())
	if err != nil {
		handleError(w, err)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.site_settings.revisions.read", "singleton", auditclient.Metadata{}) {
		return
	}
	writeData(w, http.StatusOK, values, nil)
}
