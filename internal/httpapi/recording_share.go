package httpapi

import (
	"net/http"
	"strings"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func (h *Handler) recordingShare(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex, nofollow, noarchive")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	short := r.PathValue("publicId")
	id := r.PathValue("id")
	valid := auditclient.ValidActorRequest(id, "recording-share")
	if short != "" {
		valid = recordings.ValidPublicID(short)
	}
	if !valid || r.URL.RawQuery != "" || r.URL.EscapedPath() != r.URL.Path {
		writeError(w, http.StatusNotFound, "not_found", "Recording preview not found.")
		return
	}
	if h.recordings == nil {
		recordingUnavailable(w)
		return
	}
	if short != "" {
		var err error
		id, err = h.recordings.RecordingIDByPublicID(r.Context(), short)
		if err != nil {
			handleRecordingError(w, err)
			return
		}
	}
	if strings.HasSuffix(r.Pattern, "/cover") {
		data, err := h.recordings.ShareCover(r.Context(), id)
		if err != nil {
			handleRecordingError(w, err)
			return
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(data)
		return
	}
	v, err := h.recordings.SharePreview(r.Context(), id)
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	if recordings.ValidPublicID(v.PublicID) {
		w.Header().Set("Link", "</video/"+v.PublicID+">; rel=\"canonical\"")
	}
	writeData(w, http.StatusOK, v, nil)
}
