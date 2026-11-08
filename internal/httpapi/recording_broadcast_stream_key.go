package httpapi

import (
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http"
)

func (h *Handler) adminBroadcastStreamKey(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	v, err := h.recordings.GetBroadcastStreamKey(r.Context(), r.PathValue("id"))
	if err != nil {
		broadcastError(w, err)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.recording.read", r.PathValue("id"), auditclient.Metadata{}) {
		return
	}
	writeData(w, http.StatusOK, v, nil)
}
func (h *Handler) adminSetBroadcastStreamKey(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	var in recordings.BroadcastStreamKeyInput
	if !broadcastDecode(w, r, &in, 2<<10) {
		return
	}
	v, err := h.recordings.SetBroadcastStreamKey(r.Context(), r.PathValue("id"), actor(r), in)
	if err != nil {
		broadcastError(w, err)
		return
	}
	writeData(w, http.StatusOK, v, nil)
}
func (h *Handler) adminBindBroadcastStreamKey(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	var in recordings.BroadcastStreamKeyBindInput
	if !broadcastDecode(w, r, &in, 2<<10) {
		return
	}
	v, err := h.recordings.BindBroadcastWithStreamKey(r.Context(), r.PathValue("id"), actor(r), in, coverPublishScope(r))
	if err != nil {
		broadcastError(w, err)
		return
	}
	writeData(w, http.StatusOK, v, nil)
}
