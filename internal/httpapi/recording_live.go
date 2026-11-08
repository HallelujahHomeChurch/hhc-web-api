package httpapi

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func liveError(w http.ResponseWriter, err error) {
	if errors.Is(err, bulletinwatermark.ErrRestricted) {
		captureError(w, recordings.ErrNotFound)
		return
	}
	captureError(w, err)
}
func (h *Handler) memberListLiveRecordings(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["q"]) > 1 {
		liveError(w, recordings.ErrInvalid)
		return
	}
	for key := range query {
		if key != "q" {
			liveError(w, recordings.ErrInvalid)
			return
		}
	}
	values, err := h.recordings.ListLive(r.Context(), query.Get("q"))
	if err != nil {
		if writeMemberCancellation(w, r, err) {
			return
		}
		liveError(w, err)
		return
	}
	writeData(w, http.StatusOK, values, nil)
}
func (h *Handler) memberLiveRecordingPlayback(w http.ResponseWriter, r *http.Request) {
	if r.URL.RawQuery != "" {
		liveError(w, recordings.ErrInvalid)
		return
	}
	var input struct {
		CaptureID       string `json:"captureId"`
		PlaybackScopeID string `json:"playbackScopeId"`
	}
	if !captureDecode(w, r, &input, 256<<10) {
		return
	}
	p := r.Context().Value(principalKey{}).(principal)
	value, err := h.recordings.LivePlayback(r.Context(), r.PathValue("id"), input.CaptureID, input.PlaybackScopeID, p.UserID)
	if err != nil {
		liveError(w, err)
		return
	}
	writeData(w, http.StatusOK, value, nil)
}
