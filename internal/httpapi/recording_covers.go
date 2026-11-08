package httpapi

import (
	"net/http"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func coverPublishScope(r *http.Request) bool {
	p, _ := r.Context().Value(principalKey{}).(principal)
	return p.Scopes["cms:recordings:publish"] || p.Scopes["*"]
}
func (h *Handler) adminListRecordingCovers(w http.ResponseWriter, r *http.Request) {
	if h.recordings == nil {
		recordingUnavailable(w)
		return
	}
	v, err := h.recordings.ListCovers(r.Context(), r.PathValue("id"))
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.recording.read", r.PathValue("id"), auditclient.Metadata{}) {
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) adminUploadRecordingCover(w http.ResponseWriter, r *http.Request) {
	if h.recordings == nil {
		recordingUnavailable(w)
		return
	}
	if len(r.Header.Values("Idempotency-Key")) != 1 || !recordings.ValidOperationKey(r.Header.Get("Idempotency-Key")) {
		writeError(w, 400, "invalid_request", "One bounded Idempotency-Key is required.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
	v, err := h.recordings.UploadCover(r.Context(), r.PathValue("id"), r.Header.Get("Content-Type"), r.Header.Get("Idempotency-Key"), r.Body, coverPublishScope(r))
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	writeData(w, 202, v, nil)
}
func (h *Handler) adminSetRecordingCover(w http.ResponseWriter, r *http.Request) {
	if h.recordings == nil {
		recordingUnavailable(w)
		return
	}
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input recordings.CoverSelection
	if !decode(w, r, &input) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	p, _ := r.Context().Value(principalKey{}).(principal)
	if len(r.Header.Values("Idempotency-Key")) > 1 || key != "" && !recordings.ValidOperationKey(key) || p.ActorType == "service" && key == "" {
		writeError(w, 400, "invalid_request", "A bounded Idempotency-Key is required for service selection.")
		return
	}
	if key == "" {
		key = platform.NewID()
	}
	v, err := h.recordings.SelectCover(r.Context(), r.PathValue("id"), expected, input, actor(r), key, coverPublishScope(r))
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) recordingCoverContent(w http.ResponseWriter, r *http.Request, member bool) {
	if h.recordings == nil {
		recordingUnavailable(w)
		return
	}
	data, err := h.recordings.CoverContent(r.Context(), r.PathValue("id"), r.PathValue("coverId"), member)
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	if !member && !h.enqueueAdminAudit(w, r, "cms.recording.read", r.PathValue("id"), auditclient.Metadata{}) {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "image/jpeg")
	w.Write(data)
}
func (h *Handler) adminRecordingCoverContent(w http.ResponseWriter, r *http.Request) {
	h.recordingCoverContent(w, r, false)
}
func (h *Handler) memberRecordingCoverContent(w http.ResponseWriter, r *http.Request) {
	h.recordingCoverContent(w, r, true)
}
