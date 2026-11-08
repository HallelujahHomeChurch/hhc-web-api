package httpapi

import (
	"net/http"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func (h *Handler) liveCoverRequest(w http.ResponseWriter, r *http.Request, write bool) bool {
	if r.URL.RawQuery != "" {
		writeError(w, 400, "invalid_request", "Query parameters are not supported.")
		return false
	}
	if write {
		p := r.Context().Value(principalKey{}).(principal)
		if p.ActorType == "service" || !coverPublishScope(r) {
			writeError(w, 403, "forbidden", "Live thumbnail changes require a human administrator with publication capability.")
			return false
		}
	}
	if h.recordings == nil {
		recordingUnavailable(w)
		return false
	}
	return true
}
func (h *Handler) auditLiveCoverRead(w http.ResponseWriter, r *http.Request) bool {
	action, id := "cms.recording.read", r.PathValue("id")
	if id == "" {
		action, id = "cms.site_settings.read", "singleton"
	}
	return h.enqueueAdminAudit(w, r, action, id, auditclient.Metadata{})
}
func (h *Handler) adminLiveCoverSettings(w http.ResponseWriter, r *http.Request) {
	if !h.liveCoverRequest(w, r, false) {
		return
	}
	v, err := h.recordings.GetLiveCoverSettings(r.Context(), r.PathValue("id"), r.PathValue("captureId"))
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	if !h.auditLiveCoverRead(w, r) {
		return
	}
	writeData(w, 200, v, nil)
}
func liveCoverKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get("Idempotency-Key")
	if len(r.Header.Values("Idempotency-Key")) != 1 || !recordings.ValidOperationKey(key) {
		writeError(w, 400, "invalid_request", "One bounded Idempotency-Key is required.")
		return "", false
	}
	return key, true
}
func (h *Handler) adminSetLiveCoverSettings(w http.ResponseWriter, r *http.Request) {
	if !h.liveCoverRequest(w, r, true) {
		return
	}
	if len(r.Header.Values("If-Match")) != 1 {
		writeError(w, 428, "precondition_required", "One settings revision is required.")
		return
	}
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	key, ok := liveCoverKey(w, r)
	if !ok {
		return
	}
	var input recordings.LiveCoverSelection
	if !decodeLimit(w, r, &input, 2<<10) {
		return
	}
	v, err := h.recordings.SetLiveCoverSettings(r.Context(), r.PathValue("id"), r.PathValue("captureId"), expected, input, actor(r), key)
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) adminUploadLiveCover(w http.ResponseWriter, r *http.Request) {
	if !h.liveCoverRequest(w, r, true) {
		return
	}
	key, ok := liveCoverKey(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
	v, err := h.recordings.UploadLiveCover(r.Context(), r.PathValue("id"), r.PathValue("captureId"), r.Header.Get("Content-Type"), key, r.Body)
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	writeData(w, 202, v, nil)
}
func (h *Handler) adminLiveCoverUpload(w http.ResponseWriter, r *http.Request) {
	if !h.liveCoverRequest(w, r, false) {
		return
	}
	v, err := h.recordings.GetLiveCoverUpload(r.Context(), r.PathValue("id"), r.PathValue("captureId"), r.PathValue("uploadId"))
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	if !h.auditLiveCoverRead(w, r) {
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) liveCoverContent(w http.ResponseWriter, r *http.Request, member bool) {
	if !h.liveCoverRequest(w, r, false) {
		return
	}
	var data []byte
	var err error
	if member {
		p := r.Context().Value(principalKey{}).(principal)
		data, err = h.recordings.MemberLiveCoverContent(r.Context(), r.PathValue("id"), r.PathValue("captureId"), p.UserID)
	} else {
		data, err = h.recordings.LiveCoverContent(r.Context(), r.PathValue("id"), r.PathValue("captureId"), r.PathValue("uploadId"), false)
	}
	if err != nil {
		if member && writeMemberCancellation(w, r, err) {
			return
		}
		handleRecordingError(w, err)
		return
	}
	if !member && !h.auditLiveCoverRead(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Type", "image/jpeg")
	w.Write(data)
}
func (h *Handler) adminLiveCoverContent(w http.ResponseWriter, r *http.Request) {
	h.liveCoverContent(w, r, false)
}
func (h *Handler) memberLiveCoverContent(w http.ResponseWriter, r *http.Request) {
	h.liveCoverContent(w, r, true)
}
