package httpapi

import (
	"errors"
	"net/http"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func (h *Handler) registerRecordingProgressRoutes(member *http.ServeMux) {
	member.HandleFunc("GET /api/member/recordings/{id}/progress", h.requireRecordingProgressMember(h.memberRecordingProgress))
	member.HandleFunc("POST /api/member/recordings/{id}/progress/claim", h.requireRecordingProgressMember(h.memberClaimRecordingProgress))
	member.HandleFunc("PUT /api/member/recordings/{id}/progress", h.requireRecordingProgressMember(h.memberUpdateRecordingProgress))
	member.HandleFunc("GET /api/member/recordings/continue-watching", h.requireRecordingProgressMember(h.memberContinueWatching))
}

func (h *Handler) requireRecordingProgressMember(next http.HandlerFunc) http.HandlerFunc {
	entitled := h.requireRecordingEntitlement(next)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		p, ok := r.Context().Value(principalKey{}).(principal)
		if !ok || p.ActorType != "user" || p.UserID == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Member identity is required.")
			return
		}
		entitled(w, r)
	}
}

func (h *Handler) memberRecordingProgress(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	data, err := h.recordings.GetPlaybackProgress(r.Context(), r.PathValue("id"), actor(r))
	writeProgressResult(w, r, data, err)
}

func (h *Handler) memberClaimRecordingProgress(w http.ResponseWriter, r *http.Request) {
	var input recordings.ProgressClaim
	if !h.broadcastRequest(w, r, false) || !broadcastDecode(w, r, &input, 4096) {
		return
	}
	data, err := h.recordings.ClaimPlaybackProgress(r.Context(), r.PathValue("id"), actor(r), input)
	writeProgressResult(w, r, data, err)
}

func (h *Handler) memberUpdateRecordingProgress(w http.ResponseWriter, r *http.Request) {
	var input recordings.ProgressUpdate
	if !h.broadcastRequest(w, r, false) || !broadcastDecode(w, r, &input, 4096) {
		return
	}
	data, err := h.recordings.UpdatePlaybackProgress(r.Context(), r.PathValue("id"), actor(r), input)
	writeProgressResult(w, r, data, err)
}

func (h *Handler) memberContinueWatching(w http.ResponseWriter, r *http.Request) {
	if !h.broadcastRequest(w, r, false) {
		return
	}
	item, err := h.recordings.GetContinueWatching(r.Context(), actor(r))
	writeProgressResult(w, r, map[string]*recordings.ResumeItem{"item": item}, err)
}

func writeProgressResult(w http.ResponseWriter, r *http.Request, data any, err error) {
	switch {
	case err == nil:
		writeData(w, http.StatusOK, data, nil)
	case errors.Is(err, recordings.ErrProgressConflict):
		writeError(w, http.StatusConflict, "progress_conflict", "Playback progress ownership changed.")
	case errors.Is(err, recordings.ErrProgressSuperseded):
		writeError(w, http.StatusConflict, "progress_superseded", "Another playback session owns progress.")
	default:
		if !writeMemberCancellation(w, r, err) {
			interactionError(w, err)
		}
	}
}
