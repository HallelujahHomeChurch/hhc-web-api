package httpapi

import (
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http"
)

func (h *Handler) adminBroadcastSource(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	v, e := h.recordings.GetBroadcastSource(r.Context())
	if e != nil {
		broadcastError(w, e)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.site_settings.read", "singleton", auditclient.Metadata{}) {
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) adminSetBroadcastSourceKey(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	var in recordings.BroadcastSourceStreamKeyInput
	if !broadcastDecode(w, r, &in, 4<<10) {
		return
	}
	v, e := h.recordings.SetBroadcastSourceStreamKey(r.Context(), actor(r), in)
	if e != nil {
		broadcastError(w, e)
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) adminSetBroadcastSourceTarget(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	var in recordings.BroadcastSourceTargetInput
	if !broadcastDecode(w, r, &in, 4<<10) {
		return
	}
	v, e := h.recordings.SetBroadcastSourceTarget(r.Context(), actor(r), in)
	if e != nil {
		broadcastError(w, e)
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) adminResolveBroadcastSource(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	var in recordings.BroadcastSourceResolveInput
	if !broadcastDecode(w, r, &in, 4<<10) {
		return
	}
	v, e := h.recordings.ResolveBroadcastSource(r.Context(), in)
	if e != nil {
		broadcastError(w, e)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.recording.read", v.RecordingID, auditclient.Metadata{}) {
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) adminBindBroadcastSource(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	var in recordings.BroadcastSourceBindInput
	if !broadcastDecode(w, r, &in, 4<<10) {
		return
	}
	v, e := h.recordings.BindBroadcastSource(r.Context(), actor(r), in, coverPublishScope(r))
	if e != nil {
		broadcastError(w, e)
		return
	}
	writeData(w, 200, v, nil)
}
