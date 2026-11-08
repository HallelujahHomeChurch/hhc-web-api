package httpapi

import (
	"net/http"
	"strings"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

func (h *Handler) recordingRetentionHuman(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := r.Context().Value(principalKey{}).(principal)
	if !ok || p.ActorType != "user" {
		writeError(w, 403, "forbidden", "Human administrator required.")
		return false
	}
	if h.recordings == nil {
		recordingUnavailable(w)
		return false
	}
	return true
}
func (h *Handler) adminRecordingRetention(w http.ResponseWriter, r *http.Request) {
	if !h.recordingRetentionHuman(w, r) {
		return
	}
	v, err := h.recordings.GetRetentionPolicy(r.Context())
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	writeData(w, http.StatusOK, v, nil)
}
func (h *Handler) adminPreviewRecordingRetention(w http.ResponseWriter, r *http.Request) {
	if !h.recordingRetentionHuman(w, r) {
		return
	}
	var in struct {
		RetentionDays int `json:"retentionDays"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, err := h.recordings.PreviewRetentionPolicy(r.Context(), in.RetentionDays)
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	writeData(w, http.StatusOK, v, nil)
}
func (h *Handler) adminUpdateRecordingRetention(w http.ResponseWriter, r *http.Request) {
	if !h.recordingRetentionHuman(w, r) {
		return
	}
	var in assetclient.UpdateRecordingRetentionInput
	if !decode(w, r, &in) {
		return
	}
	v, err := h.recordings.UpdateRetentionPolicy(r.Context(), in, strings.TrimSpace(r.Header.Get("Idempotency-Key")))
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	writeData(w, http.StatusOK, v, nil)
}
