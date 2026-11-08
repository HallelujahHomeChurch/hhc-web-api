package httpapi

import (
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http"
)

func (h *Handler) adminStudioSources(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	v, e := h.recordings.ListStudioSources(r.Context())
	if e != nil {
		broadcastError(w, e)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.site_settings.read", "singleton", auditclient.Metadata{}) {
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) adminStudioSourceMutation(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.broadcastRequest(w, r, false) {
			return
		}
		var in recordings.StudioSourceInput
		if !broadcastDecode(w, r, &in, 4<<10) {
			return
		}
		v, e := h.recordings.MutateStudioSource(r.Context(), r.PathValue("sourceId"), actor(r), kind, in)
		if e != nil {
			broadcastError(w, e)
			return
		}
		writeData(w, 200, v, nil)
	}
}
func (h *Handler) adminStudioDirect(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	var in recordings.BroadcastMutationInput
	if !broadcastDecode(w, r, &in, 4<<10) {
		return
	}
	v, e := h.recordings.EnsureDirectBroadcast(r.Context(), r.PathValue("sourceId"), actor(r), in)
	if e != nil {
		broadcastError(w, e)
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) adminStudioPrepare(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	var in recordings.BroadcastSourceTargetInput
	if !broadcastDecode(w, r, &in, 4<<10) {
		return
	}
	v, e := h.recordings.PrepareStudioSource(r.Context(), r.PathValue("sourceId"), actor(r), in)
	if e != nil {
		broadcastError(w, e)
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) registerStudioRoutes(admin *http.ServeMux) {
	admin.HandleFunc("POST /api/admin/broadcast-sources/{sourceId}/stream-key/reveal", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminRevealStudioStreamKey)))
	admin.HandleFunc("PUT /api/admin/studio/broadcasts/{id}/source", requireScope("cms:recordings:write", requireScope("cms:recordings:publish", h.adminAuditProvenance(h.adminStudioAssignSource))))
	admin.HandleFunc("POST /api/admin/studio/broadcasts", requireScope("cms:recordings:write", requireScope("cms:recordings:publish", h.adminAuditProvenance(h.adminStudioSchedule))))
	admin.HandleFunc("GET /api/admin/broadcasts/{id}/studio", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminStudioBroadcast)))
	admin.HandleFunc("GET /api/admin/broadcast-sources", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminStudioSources)))
	for _, v := range []struct{ route, kind string }{{"POST /api/admin/broadcast-sources", "create"}, {"PATCH /api/admin/broadcast-sources/{sourceId}", "rename"}, {"DELETE /api/admin/broadcast-sources/{sourceId}", "delete"}, {"PUT /api/admin/broadcast-sources/{sourceId}/stream-key", "key"}} {
		admin.HandleFunc(v.route, requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminStudioSourceMutation(v.kind))))
	}
	admin.HandleFunc("POST /api/admin/broadcast-sources/{sourceId}/direct/ensure", requireScope("cms:recordings:write", requireScope("cms:recordings:publish", h.adminAuditProvenance(h.adminStudioDirect))))
	admin.HandleFunc("PUT /api/admin/broadcast-sources/{sourceId}/target", requireScope("cms:recordings:write", requireScope("cms:recordings:publish", h.adminAuditProvenance(h.adminStudioPrepare))))
}

func (h *Handler) adminStudioSchedule(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	var in recordings.StudioScheduleInput
	if !broadcastDecode(w, r, &in, 16<<10) {
		return
	}
	v, e := h.recordings.CreateStudioSchedule(r.Context(), actor(r), in)
	if e != nil {
		broadcastError(w, e)
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) adminStudioBroadcast(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	v, e := h.recordings.GetStudioBroadcast(r.Context(), r.PathValue("id"))
	if e != nil {
		broadcastError(w, e)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.recording.read", r.PathValue("id"), auditclient.Metadata{}) {
		return
	}
	writeData(w, 200, v, nil)
}

func (h *Handler) adminStudioAssignSource(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	var in recordings.StudioSourceAssignment
	if !broadcastDecode(w, r, &in, 4<<10) {
		return
	}
	v, e := h.recordings.AssignStudioSource(r.Context(), r.PathValue("id"), actor(r), in)
	if e != nil {
		broadcastError(w, e)
		return
	}
	writeData(w, 200, v, nil)
}

func (h *Handler) adminRevealStudioStreamKey(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if !h.broadcastRequest(w, r, false) {
		return
	}
	if h.auditOutbox == nil {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "Audit recording is unavailable.")
		return
	}
	key, e := h.recordings.RevealStudioStreamKey(r.Context(), r.PathValue("sourceId"), actor(r))
	if e != nil {
		broadcastError(w, e)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.broadcast_source.key.reveal", r.PathValue("sourceId"), auditclient.Metadata{}) {
		return
	}
	writeData(w, 200, map[string]string{"streamKey": key}, nil)
}
