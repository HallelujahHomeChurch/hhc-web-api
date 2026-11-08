package httpapi

import (
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/google/uuid"
	"net/http"
)

func (h *Handler) cleanupWatermarkAccount(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	var input struct {
		UserID         string `json:"userId"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if !decode(w, r, &input) {
		return
	}
	if _, err := uuid.Parse(input.UserID); err != nil || len(input.IdempotencyKey) == 0 || len(input.IdempotencyKey) > 200 {
		writeError(w, 422, "invalid_request", "Invalid cleanup request.")
		return
	}
	if h.watermarkRepo == nil {
		writeError(w, 503, "account_cleanup_unavailable", "Cleanup unavailable.")
		return
	}
	count, remaining, err := h.watermarkRepo.CleanupWatermarkAccount(r.Context(), input.UserID, input.IdempotencyKey)
	if err != nil {
		if errors.Is(err, bulletinwatermark.ErrInvalidInvestigation) {
			writeError(w, 409, "idempotency_conflict", "Idempotency key conflicts with another account.")
			return
		}
		watermarkError(w, err)
		return
	}
	status := "completed"
	if remaining != 0 {
		status = "pending"
	}
	writeData(w, 200, map[string]any{"owner": "website_watermark", "status": status, "recordCount": count, "remainingCount": remaining, "reasonCodes": []string{}}, nil)
}
