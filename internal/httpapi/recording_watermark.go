package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func (h *Handler) lookupRecordingWatermark(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	p, ok := r.Context().Value(principalKey{}).(principal)
	if !ok || p.ActorType != "user" {
		writeError(w, 403, "forbidden", "Human investigator required.")
		return
	}
	if h.recordings == nil {
		recordingUnavailable(w)
		return
	}
	var input struct {
		RecordingID string `json:"recordingId"`
		Code        string `json:"code"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || decoder.Decode(&struct{}{}) != io.EOF || len(input.Code) > 64 || r.URL.RawQuery != "" {
		// Invalid requests consume the same actor budget and audit path, without retaining input.
		input.RecordingID, input.Code = "", ""
	}
	v, err := h.recordings.LookupWatermark(r.Context(), p.UserID, input.RecordingID, input.Code)
	if err != nil {
		switch {
		case errors.Is(err, recordings.ErrInvalid):
			writeError(w, 400, "invalid_request", "Enter a complete recording watermark code and recording ID.")
		case errors.Is(err, recordings.ErrNotFound):
			writeError(w, 404, "watermark_not_found", "No matching record was found.")
		case errors.Is(err, bulletinwatermark.ErrRateLimited):
			w.Header().Set("Retry-After", "60")
			writeError(w, 429, "watermark_rate_limited", "Too many lookups. Please retry later.")
		case errors.Is(err, bulletinwatermark.ErrRestricted):
			writeError(w, 403, "processing_restricted", "Processing is restricted.")
		default:
			writeError(w, 503, "watermark_unavailable", "Lookup is temporarily unavailable.")
		}
		return
	}
	writeData(w, 200, map[string]any{"receiptId": v.ID, "recordingId": v.RecordingID, "packageId": v.PackageID, "userId": v.UserID, "issuedAt": v.IssuedAt, "expiresAt": v.ExpiresAt}, nil)
}
