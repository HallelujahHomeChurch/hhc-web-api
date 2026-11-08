package httpapi

import "net/http"

func (h *Handler) adminDeleteRecording(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if h.recordings == nil {
		recordingUnavailable(w)
		return
	}
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	p := r.Context().Value(principalKey{}).(principal)
	if p.ActorType == "service" {
		writeError(w, http.StatusForbidden, "forbidden", "Recording deletion requires a human administrator.")
		return
	}
	if err := h.recordings.Delete(r.Context(), r.PathValue("id"), expected, actor(r), p.Scopes["cms:recordings:publish"] || p.Scopes["*"]); err != nil {
		handleRecordingError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
