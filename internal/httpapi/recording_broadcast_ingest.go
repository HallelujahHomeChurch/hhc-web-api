package httpapi

import (
	"context"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http"
	"strings"
)

func ingestError(w http.ResponseWriter, e error) {
	if errors.Is(e, recordings.ErrBroadcastIngestRevoked) {
		writeError(w, 409, "broadcast_ingest_revoked", "broadcast_ingest_revoked")
		return
	}
	if errors.Is(e, recordings.ErrBroadcastIngestSession) {
		writeError(w, 401, "broadcast_ingest_session_invalid", "broadcast_ingest_session_invalid")
		return
	}
	broadcastError(w, e)
}
func (h *Handler) ingestResolve(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	var in recordings.BroadcastSourceResolveInput
	if !broadcastDecode(w, r, &in, 4<<10) {
		return
	}
	out, e := h.recordings.ResolveBroadcastSource(r.Context(), in)
	if e != nil {
		ingestError(w, e)
		return
	}
	writeData(w, 200, out, nil)
}
func (h *Handler) ingestBind(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	var in recordings.BroadcastSourceBindInput
	if !broadcastDecode(w, r, &in, 4<<10) {
		return
	}
	out, e := h.recordings.BindBroadcastIngest(r.Context(), r.Header.Get("X-HHC-Request-ID"), in)
	if e != nil {
		ingestError(w, e)
		return
	}
	writeData(w, 200, out, nil)
}
func (h *Handler) ingestRenew(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	var in recordings.BroadcastIngestRenewInput
	if !broadcastDecode(w, r, &in, 4<<10) {
		return
	}
	out, e := h.recordings.RenewBroadcastIngest(r.Context(), in)
	if e != nil {
		ingestError(w, e)
		return
	}
	writeData(w, 200, out, nil)
}
func (h *Handler) ingestSession(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if h.recordings == nil {
			ingestError(w, assetclient.ErrUnavailable)
			return
		}
		if r.Method != "GET" && r.URL.RawQuery != "" {
			ingestError(w, recordings.ErrInvalid)
			return
		}
		headers := r.Header.Values("Authorization")
		if len(headers) != 1 || !strings.HasPrefix(headers[0], "Bearer ") {
			ingestError(w, recordings.ErrBroadcastIngestSession)
			return
		}
		cleanup := r.PathValue("captureId") != "" && (r.Method == "GET" || r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/abort"))
		owner, _, e := h.recordings.AuthorizeBroadcastIngest(r.Context(), strings.TrimPrefix(headers[0], "Bearer "), r.PathValue("id"), r.PathValue("captureId"), cleanup)
		if e != nil {
			ingestError(w, e)
			return
		}
		requestID := r.Header.Get("X-HHC-Request-ID")
		if !auditclient.ValidActorRequest(owner, requestID) {
			ingestError(w, recordings.ErrInvalid)
			return
		}
		// Server-resolved delegation; public identity headers never supply the owner.
		p := principal{UserID: owner, PrincipalID: owner, ActorType: "user", RequestID: requestID, Scopes: map[string]bool{"cms:recordings:read": true, "cms:recordings:write": true}}
		ctx := context.WithValue(r.Context(), principalKey{}, p)
		next(w, r.WithContext(auditclient.WithProvenance(ctx, owner, requestID)))
	}
}
func (h *Handler) registerIngestRoutes(mux *http.ServeMux) {
	ingest := http.NewServeMux()
	ingest.HandleFunc("POST /api/ingest/streams/open", h.ingestOpenOutput)
	ingest.HandleFunc("POST /api/ingest/streams/replay", h.ingestReplayOutput)
	ingest.HandleFunc("POST /api/ingest/broadcast-source/resolve", h.ingestResolve)
	ingest.HandleFunc("POST /api/ingest/broadcast-source/bindings", h.ingestBind)
	ingest.HandleFunc("POST /api/ingest/broadcast-source/session", h.ingestRenew)
	base := "/api/ingest/broadcasts/{id}/captures/{captureId}"
	ingest.HandleFunc("GET "+base, h.ingestSession(h.adminGetRecordingCapture))
	for _, route := range []struct {
		path    string
		handler http.HandlerFunc
	}{
		{"objects", h.adminDeclareRecordingCapture}, {"sign", h.adminSignRecordingCapture}, {"confirm", h.adminConfirmRecordingCapture}, {"seal", h.adminSealRecordingCapture}, {"abort", h.adminAbortRecordingCapture}, {"stop", h.adminLocalRecordingCapture},
	} {
		ingest.HandleFunc("POST "+base+"/"+route.path, h.ingestSession(route.handler))
	}
	ingest.HandleFunc("GET /api/ingest/broadcasts/{id}/control", h.ingestSession(h.adminBroadcastControl))
	ingest.HandleFunc("POST /api/ingest/broadcasts/{id}/commands/{commandId}/ack", h.ingestSession(h.adminAckBroadcast))
	legacy := requireServiceCaller(map[string]bool{h.trustedCaller: true}, h.daprAPIToken, h.allowDevCaller, ingest)
	mux.Handle("/api/ingest/", privateNoStore(h.gatewayPrivateRoutes(ingest, legacy, ingest)))
}

func (h *Handler) ingestOpenOutput(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	var in recordings.BroadcastOutputOpenInput
	if !broadcastDecode(w, r, &in, 4<<10) {
		return
	}
	out, err := h.recordings.OpenBroadcastOutput(r.Context(), r.Header.Get("X-HHC-Request-ID"), in)
	if err != nil {
		ingestError(w, err)
		return
	}
	writeData(w, 200, out, nil)
}

func (h *Handler) ingestReplayOutput(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	var in recordings.BroadcastOutputReplayInput
	if !broadcastDecode(w, r, &in, 4<<10) {
		return
	}
	out, err := h.recordings.ReplayBroadcastOutput(r.Context(), r.Header.Get("X-HHC-Request-ID"), in)
	if err != nil {
		ingestError(w, err)
		return
	}
	writeData(w, 200, out, nil)
}
