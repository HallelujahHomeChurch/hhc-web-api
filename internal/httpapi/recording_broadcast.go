package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

func broadcastError(w http.ResponseWriter, e error) {
	status, code := 503, "unavailable"
	switch {
	case errors.Is(e, recordings.ErrInvalid), errors.Is(e, assetclient.ErrInvalid):
		status, code = 400, "broadcast_invalid"
	case errors.Is(e, recordings.ErrBroadcastOutputReplayUnknown):
		status, code = 409, "broadcast_output_replay_unknown"
	case errors.Is(e, recordings.ErrBroadcastOutputCapacity):
		status, code = 409, "broadcast_output_capacity_exhausted"
	case errors.Is(e, recordings.ErrBroadcastOutputTerminal):
		status, code = 409, "broadcast_output_terminal"
	case errors.Is(e, recordings.ErrBroadcastSourceBusy):
		status, code = 409, "broadcast_source_busy"
	case errors.Is(e, recordings.ErrBroadcastSourceNotPrepared):
		status, code = 409, "broadcast_source_not_prepared"
	case errors.Is(e, recordings.ErrStreamKeyUnavailable):
		status, code = 409, "stream_key_not_retained"
	case errors.Is(e, recordings.ErrBroadcastStreamKey):
		status, code = 403, "broadcast_stream_key_invalid"
	case errors.Is(e, recordings.ErrForbidden), errors.Is(e, assetclient.ErrForbidden):
		status, code = 403, "forbidden"
	case errors.Is(e, recordings.ErrNotFound), errors.Is(e, assetclient.ErrNotFound), errors.Is(e, bulletinwatermark.ErrRestricted):
		status, code = 404, "broadcast_not_found"
	case errors.Is(e, recordings.ErrPrecondition):
		status, code = 412, "broadcast_revision_conflict"
	case errors.Is(e, recordings.ErrBroadcastProtocol):
		status, code = 422, "broadcast_protocol_unsupported"
	case errors.Is(e, recordings.ErrBroadcastActiveLimit):
		status, code = 409, "broadcast_active_limit"
	case errors.Is(e, recordings.ErrBroadcastEpoch):
		status, code = 409, "broadcast_epoch_mismatch"
	case errors.Is(e, recordings.ErrBroadcastBoundary):
		status, code = 409, "broadcast_boundary_conflict"
	case errors.Is(e, recordings.ErrBroadcastBound):
		status, code = 409, "broadcast_already_bound"
	case errors.Is(e, recordings.ErrBroadcastOperation):
		status, code = 409, "broadcast_operation_conflict"
	case errors.Is(e, recordings.ErrConflict), errors.Is(e, assetclient.ErrConflict):
		status, code = 409, "broadcast_state_conflict"
	case errors.Is(e, assetclient.ErrCoverTooLarge):
		status, code = 413, "broadcast_invalid"
	}
	if status == 503 {
		w.Header().Set("Retry-After", "5")
	}
	writeError(w, status, code, code)
}
func (h *Handler) broadcastRequest(w http.ResponseWriter, r *http.Request, query bool) bool {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if h.recordings == nil {
		broadcastError(w, assetclient.ErrUnavailable)
		return false
	}
	if !query && r.URL.RawQuery != "" {
		broadcastError(w, recordings.ErrInvalid)
		return false
	}
	return true
}
func (h *Handler) adminBroadcastCapabilities(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	writeData(w, 200, map[string]any{"versions": []string{recordings.BroadcastProtocolVersion}, "maxActiveCaptures": 1, "markerMode": "next-common-segment"}, nil)
}
func (h *Handler) adminCreateBroadcast(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	var in recordings.BroadcastCreateInput
	if !broadcastDecode(w, r, &in, 16<<10) {
		return
	}
	v, e := h.recordings.CreateBroadcast(r.Context(), in, actor(r), coverPublishScope(r))
	if e != nil {
		broadcastError(w, e)
		return
	}
	writeData(w, 201, v, nil)
}
func broadcastPagination(r *http.Request) (string, int, error) {
	query, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return "", 0, recordings.ErrInvalid
	}
	for key, values := range query {
		if len(values) != 1 || key != "limit" && key != "cursor" && key != "filter" {
			return "", 0, recordings.ErrInvalid
		}
	}
	limit := 20
	if query.Has("limit") {
		v, e := strconv.Atoi(query.Get("limit"))
		if e != nil {
			return "", 0, recordings.ErrInvalid
		}
		limit = v
	}
	if query.Get("filter") != "" && query.Get("filter") != "all" && query.Get("filter") != "selectable" {
		return "", 0, recordings.ErrInvalid
	}
	return query.Get("cursor"), limit, nil
}
func (h *Handler) adminListBroadcasts(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, true) {
		return
	}
	cursor, limit, e := broadcastPagination(r)
	if e != nil {
		broadcastError(w, e)
		return
	}
	items, next, e := h.recordings.ListBroadcasts(r.Context(), cursor, limit)
	if e != nil {
		broadcastError(w, e)
		return
	}
	if r.URL.Query().Get("filter") == "selectable" {
		selected := []recordings.BroadcastView{}
		for _, v := range items {
			if v.Binding == nil && (v.Phase == "draft" || v.Phase == "scheduled") || v.Phase == "failed" && v.Boundaries.StartSequence == nil {
				selected = append(selected, v)
			}
		}
		items = selected
	}
	writeData(w, 200, map[string]any{"items": items, "nextCursor": next}, nil)
}
func (h *Handler) adminGetBroadcast(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	v, e := h.recordings.GetBroadcast(r.Context(), r.PathValue("id"))
	if e != nil {
		broadcastError(w, e)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.recording.read", v.RecordingID, auditclient.Metadata{}) {
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) adminUpdateBroadcast(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	var raw struct {
		recordings.BroadcastMutationInput
		Title       *string                        `json:"title,omitempty"`
		Description *string                        `json:"description,omitempty"`
		ScheduledAt json.RawMessage                `json:"scheduledAt,omitempty"`
		Policy      *recordings.BroadcastPolicy    `json:"policy,omitempty"`
		Cover       *recordings.LiveCoverSelection `json:"cover,omitempty"`
	}
	if !broadcastDecode(w, r, &raw, 16<<10) {
		return
	}
	in := recordings.BroadcastUpdateInput{BroadcastMutationInput: raw.BroadcastMutationInput, Title: raw.Title, Description: raw.Description, Policy: raw.Policy, Cover: raw.Cover}
	if len(raw.ScheduledAt) > 0 {
		in.ScheduleSet = true
		if e := json.Unmarshal(raw.ScheduledAt, &in.ScheduledAt); e != nil {
			broadcastError(w, recordings.ErrInvalid)
			return
		}
	}
	v, e := h.recordings.UpdateBroadcast(r.Context(), r.PathValue("id"), actor(r), in, coverPublishScope(r))
	if e != nil {
		broadcastError(w, e)
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) adminBindBroadcast(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	var in recordings.BroadcastBindInput
	if !broadcastDecode(w, r, &in, 2<<10) {
		return
	}
	v, e := h.recordings.BindBroadcast(r.Context(), r.PathValue("id"), actor(r), in, coverPublishScope(r))
	if e != nil {
		broadcastError(w, e)
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) adminControlBroadcast(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	var in recordings.BroadcastMutationInput
	kind := r.PathValue("operation")
	reason := ""
	if kind == "emergency-close" {
		var emergency recordings.BroadcastEmergencyCloseInput
		if !broadcastDecode(w, r, &emergency, 4<<10) {
			return
		}
		in, reason = emergency.BroadcastMutationInput, emergency.Reason
	} else {
		if !broadcastDecode(w, r, &in, 2<<10) {
			return
		}
	}
	if r.PathValue("commandId") != "" {
		kind = "cancel-command"
	}
	v, e := h.recordings.ControlBroadcast(r.Context(), r.PathValue("id"), actor(r), kind, r.PathValue("commandId"), in, reason)
	if e != nil {
		broadcastError(w, e)
		return
	}
	if kind == "emergency-close" {
		if e = h.recordings.ReconcileBroadcast(r.Context(), v.Broadcast.RecordingID); e != nil {
			broadcastError(w, e)
			return
		}
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) adminBroadcastControl(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, true) {
		return
	}
	q, queryErr := url.ParseQuery(r.URL.RawQuery)
	if queryErr != nil || (len(q) != 2 && len(q) != 3) || len(q["captureId"]) != 1 || len(q["epoch"]) != 1 || (len(q) == 3 && len(q["controlVersion"]) != 1) {
		broadcastError(w, recordings.ErrInvalid)
		return
	}
	_, versioned := q["controlVersion"]
	if versioned && q.Get("controlVersion") != recordings.BroadcastControlVersion && q.Get("controlVersion") != recordings.BroadcastControlD2Version {
		broadcastError(w, recordings.ErrBroadcastProtocol)
		return
	}
	epoch, e := strconv.ParseInt(q.Get("epoch"), 10, 64)
	if e != nil {
		broadcastError(w, recordings.ErrInvalid)
		return
	}
	if q.Get("controlVersion") == recordings.BroadcastControlD2Version {
		value, err := h.recordings.GetBroadcastControlD2(r.Context(), r.PathValue("id"), actor(r), q.Get("captureId"), epoch)
		if err != nil {
			broadcastError(w, err)
			return
		}
		writeData(w, 200, value, nil)
		return
	}
	v, e := h.recordings.GetBroadcast(r.Context(), r.PathValue("id"))
	if e != nil {
		broadcastError(w, e)
		return
	}
	if v.Binding == nil || v.Binding.ActorID != actor(r) {
		broadcastError(w, recordings.ErrNotFound)
		return
	}
	if v.Binding.CaptureID != q.Get("captureId") || v.Binding.Epoch != epoch {
		broadcastError(w, recordings.ErrBroadcastEpoch)
		return
	}
	control := recordings.BroadcastControl{Revision: v.Revision, Phase: v.Phase, PendingCommand: v.PendingCommand}
	if versioned {
		writeData(w, 200, recordings.BroadcastControlD1{BroadcastControl: control, ControlVersion: recordings.BroadcastControlVersion, RecordingID: v.RecordingID, CaptureID: v.Binding.CaptureID, Epoch: v.Binding.Epoch, Boundaries: v.Boundaries}, nil)
		return
	}
	writeData(w, 200, control, nil)
}
func (h *Handler) adminAckBroadcast(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	var in recordings.BroadcastMarkerAckInput
	if !broadcastDecode(w, r, &in, 2<<10) {
		return
	}
	v, e := h.recordings.AcknowledgeBroadcast(r.Context(), r.PathValue("id"), actor(r), r.PathValue("commandId"), in)
	if e != nil {
		broadcastError(w, e)
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) adminBroadcastPreview(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	var in struct {
		PlaybackScopeID string `json:"playbackScopeId"`
		CaptureID       string `json:"captureId"`
		Epoch           int64  `json:"epoch"`
	}
	if !broadcastDecode(w, r, &in, 2<<10) {
		return
	}
	v, e := h.recordings.BroadcastPreview(r.Context(), r.PathValue("id"), actor(r), in.CaptureID, in.Epoch, in.PlaybackScopeID)
	if e != nil {
		broadcastError(w, e)
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) memberRecordingWatch(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	p := r.Context().Value(principalKey{}).(principal)
	id := r.PathValue("id")
	if short := r.PathValue("publicId"); short != "" {
		if r.URL.EscapedPath() != r.URL.Path {
			broadcastError(w, recordings.ErrNotFound)
			return
		}
		var err error
		id, err = h.recordings.RecordingIDByPublicID(r.Context(), short)
		if err != nil {
			broadcastError(w, err)
			return
		}
	}
	v, e := h.recordings.ResolveRecordingWatch(r.Context(), id, p.UserID)
	if e != nil {
		if writeMemberCancellation(w, r, e) {
			return
		}
		broadcastError(w, e)
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) memberUpcomingBroadcasts(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, true) {
		return
	}
	query, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		broadcastError(w, recordings.ErrInvalid)
		return
	}
	for key, values := range query {
		if len(values) != 1 || key != "q" && key != "cursor" && key != "limit" {
			broadcastError(w, recordings.ErrInvalid)
			return
		}
	}
	limit := 20
	if query.Has("limit") {
		limit, e = strconv.Atoi(query.Get("limit"))
	}
	cursor := query.Get("cursor")
	if e != nil || r.URL.Query().Has("filter") {
		broadcastError(w, recordings.ErrInvalid)
		return
	}
	p := r.Context().Value(principalKey{}).(principal)
	items, next, e := h.recordings.ListUpcomingBroadcasts(r.Context(), p.UserID, cursor, limit, query.Get("q"))
	if e != nil {
		broadcastError(w, e)
		return
	}
	writeData(w, 200, map[string]any{"items": items, "nextCursor": next}, nil)
}
func (h *Handler) adminUploadBroadcastCover(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	key, ok := liveCoverKey(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 5<<20)
	v, e := h.recordings.UploadBroadcastCover(r.Context(), r.PathValue("id"), r.Header.Get("Content-Type"), key, r.Body)
	if e != nil {
		broadcastError(w, e)
		return
	}
	writeData(w, 202, v, nil)
}
func (h *Handler) adminBroadcastCoverUpload(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	v, e := h.recordings.GetBroadcastCoverUpload(r.Context(), r.PathValue("id"), r.PathValue("uploadId"))
	if e != nil {
		broadcastError(w, e)
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) broadcastCoverContent(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	member := strings.HasPrefix(r.URL.Path, "/api/member/")
	p := r.Context().Value(principalKey{}).(principal)
	data, e := h.recordings.BroadcastCoverContent(r.Context(), r.PathValue("id"), r.PathValue("uploadId"), p.UserID, member)
	if e != nil {
		broadcastError(w, e)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(data)
}
func (h *Handler) registerBroadcastRoutes(admin, member *http.ServeMux) {
	h.registerStudioRoutes(admin)
	admin.HandleFunc("GET /api/admin/broadcast-source", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminBroadcastSource)))
	admin.HandleFunc("PUT /api/admin/broadcast-source/stream-key", requireScope("cms:recordings:write", requireScope("cms:recordings:publish", h.adminAuditProvenance(h.adminSetBroadcastSourceKey))))
	admin.HandleFunc("PUT /api/admin/broadcast-source/target", requireScope("cms:recordings:write", requireScope("cms:recordings:publish", h.adminAuditProvenance(h.adminSetBroadcastSourceTarget))))
	admin.HandleFunc("POST /api/admin/broadcast-source/resolve", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminResolveBroadcastSource)))
	admin.HandleFunc("POST /api/admin/broadcast-source/bindings", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminBindBroadcastSource)))

	admin.HandleFunc("GET /api/admin/broadcasts/{id}/stream-key", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminBroadcastStreamKey)))
	admin.HandleFunc("PUT /api/admin/broadcasts/{id}/stream-key", requireScope("cms:recordings:write", requireScope("cms:recordings:publish", h.adminAuditProvenance(h.adminSetBroadcastStreamKey))))
	admin.HandleFunc("POST /api/admin/broadcasts/{id}/stream-key-bindings", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminBindBroadcastStreamKey)))
	admin.HandleFunc("GET /api/admin/broadcast-capabilities", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminBroadcastCapabilities)))
	admin.HandleFunc("POST /api/admin/broadcasts", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminCreateBroadcast)))
	admin.HandleFunc("GET /api/admin/broadcasts", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminListBroadcasts)))
	admin.HandleFunc("GET /api/admin/broadcasts/{id}", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminGetBroadcast)))
	admin.HandleFunc("PATCH /api/admin/broadcasts/{id}", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminUpdateBroadcast)))
	admin.HandleFunc("POST /api/admin/broadcasts/{id}/bindings", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminBindBroadcast)))
	control := func(next http.HandlerFunc) http.HandlerFunc {
		return requireScope("cms:recordings:write", requireScope("cms:recordings:publish", h.adminAuditProvenance(next)))
	}
	for _, op := range []string{"announce", "cancel", "start", "end", "emergency-close"} {
		admin.HandleFunc("POST /api/admin/broadcasts/{id}/"+op, control(func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("operation", op)
			h.adminControlBroadcast(w, r)
		}))
	}
	admin.HandleFunc("POST /api/admin/broadcasts/{id}/commands/{commandId}/cancel", control(h.adminControlBroadcast))
	admin.HandleFunc("GET /api/admin/broadcasts/{id}/control", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminBroadcastControl)))
	admin.HandleFunc("POST /api/admin/broadcasts/{id}/commands/{commandId}/ack", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminAckBroadcast)))
	admin.HandleFunc("POST /api/admin/broadcasts/{id}/preview-access", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminBroadcastPreview)))
	admin.HandleFunc("POST /api/admin/broadcasts/{id}/cover-uploads", control(h.adminUploadBroadcastCover))
	for _, path := range []string{"/api/admin/broadcasts/{id}/cover-uploads/{uploadId}/content", "/api/admin/broadcasts/{id}/cover/content"} {
		admin.HandleFunc("GET "+path, requireScope("cms:recordings:read", h.adminAuditProvenance(h.broadcastCoverContent)))
	}
	admin.HandleFunc("GET /api/admin/broadcasts/{id}/cover-uploads/{uploadId}", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminBroadcastCoverUpload)))
	member.HandleFunc("GET /api/member/videos/{publicId}/watch", h.requireRecordingEntitlement(h.memberRecordingWatch))
	member.HandleFunc("GET /api/member/recordings/{id}/watch", h.requireRecordingEntitlement(h.memberRecordingWatch))
	member.HandleFunc("GET /api/member/broadcasts", h.requireRecordingEntitlement(h.memberUpcomingBroadcasts))
	member.HandleFunc("GET /api/member/recordings/{id}/broadcast-cover", h.requireRecordingEntitlement(h.broadcastCoverContent))
}
func broadcastDecode(w http.ResponseWriter, r *http.Request, destination any, limit int64) bool {
	raw, e := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	trimmed := bytes.TrimSpace(raw)
	if e != nil || !utf8.Valid(raw) || len(trimmed) == 0 || trimmed[0] != '{' {
		broadcastError(w, recordings.ErrInvalid)
		return false
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		broadcastError(w, recordings.ErrInvalid)
		return false
	}
	_, progressClaim := destination.(*recordings.ProgressClaim)
	for key, value := range object {
		if key != "scheduledAt" && !(progressClaim && key == "expectedGeneration") && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			broadcastError(w, recordings.ErrInvalid)
			return false
		}
	}
	var required []string
	switch destination.(type) {
	case *recordings.ProgressClaim:
		required = []string{"sessionId", "claimId", "expectedGeneration", "claimToken", "mediaVersion", "startKind", "observedPositionSeconds"}
	case *recordings.ProgressUpdate:
		required = []string{"sessionId", "generation", "sequence", "mediaVersion", "positionSeconds", "state"}
	case *recordings.BroadcastOutputReplayInput:
		required = []string{"protocolVersion", "outputSessionId", "sourceId", "recordingId", "captureId", "epoch", "receipt", "streamKey"}
		for key := range object {
			switch key {
			case "protocolVersion", "outputSessionId", "sourceId", "recordingId", "captureId", "epoch", "receipt", "streamKey", "mediaProfile":
			default:
				broadcastError(w, recordings.ErrInvalid)
				return false
			}
		}
		var receipt map[string]json.RawMessage
		if json.Unmarshal(object["receipt"], &receipt) != nil {
			broadcastError(w, recordings.ErrInvalid)
			return false
		}
		for key, value := range receipt {
			switch key {
			case "operationKey", "operation", "state", "commandId":
				var text string
				if json.Unmarshal(value, &text) != nil || text == "" {
					broadcastError(w, recordings.ErrInvalid)
					return false
				}
			default:
				broadcastError(w, recordings.ErrInvalid)
				return false
			}
		}
	case *recordings.BroadcastCreateInput:
		required = []string{"operationKey", "title", "description", "scheduledAt", "policy"}
	case *recordings.BroadcastMarkerAckInput:
		required = []string{"operationKey", "captureId", "epoch", "boundarySequence"}
	case *recordings.BroadcastStreamKeyInput:
		required = []string{"operationKey", "expectedRevision", "streamKey"}
	case *recordings.BroadcastStreamKeyBindInput:
		required = []string{"operationKey", "expectedRevision", "protocolVersion"}
	case *recordings.BroadcastBindInput:
		required = []string{"operationKey", "expectedRevision", "protocolVersion"}
	case *recordings.BroadcastMutationInput:
		required = []string{"operationKey", "expectedRevision"}
	case *recordings.BroadcastEmergencyCloseInput:
		required = []string{"operationKey", "expectedRevision", "reason"}
	}
	for _, key := range required {
		if _, exists := object[key]; !exists {
			broadcastError(w, recordings.ErrInvalid)
			return false
		}
	}

	if policy, ok := object["policy"]; ok {
		var fields map[string]json.RawMessage
		if json.Unmarshal(policy, &fields) != nil {
			broadcastError(w, recordings.ErrInvalid)
			return false
		}
		for _, key := range []string{"autoStart", "autoEnd", "autoPublish"} {
			value, exists := fields[key]
			if !exists || !bytes.Equal(value, []byte("true")) && !bytes.Equal(value, []byte("false")) {
				broadcastError(w, recordings.ErrInvalid)
				return false
			}
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if e := decoder.Decode(destination); e != nil {
		broadcastError(w, recordings.ErrInvalid)
		return false
	}
	var trailing any
	if e := decoder.Decode(&trailing); e != io.EOF {
		broadcastError(w, recordings.ErrInvalid)
		return false
	}
	return true
}
