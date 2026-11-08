package httpapi

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
)

func (h *Handler) adminBulletinLearningRules(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query) > 1 || (len(query) == 1 && (len(query["cursor"]) != 1 || query.Get("cursor") == "")) {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	value, err := h.service.OnlineBulletinLearningRules(r.Context(), query.Get("cursor"))
	if err != nil {
		handleError(w, err)
		return
	}
	if h.enqueueAdminAudit(w, r, "cms.site_settings.read", "singleton", auditclient.Metadata{}) {
		writeData(w, http.StatusOK, value, nil)
	}
}

func (h *Handler) adminBulletinLearningRule(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.URL.RawQuery != "" {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	value, err := h.service.OnlineBulletinLearningRule(r.Context(), r.PathValue("signature"))
	if err != nil {
		handleError(w, err)
		return
	}
	if h.enqueueAdminAudit(w, r, "cms.site_settings.revisions.read", "singleton", auditclient.Metadata{}) {
		w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Rule.Version))
		writeData(w, http.StatusOK, value, nil)
	}
}

func (h *Handler) adminSetBulletinLearningRule(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if r.URL.RawQuery != "" {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	if !decodeLimit(w, r, &input, 1024) {
		return
	}
	if input.Enabled == nil {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	value, err := h.service.SetOnlineBulletinLearningRuleEnabled(r.Context(), r.PathValue("signature"), *input.Enabled, expected, actor(r))
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}
