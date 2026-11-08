package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
)

type watermarkRepository interface {
	PublishedWatermarkSource(context.Context, string, string) (bulletinwatermark.Source, error)
	InsertWatermarkReceipt(context.Context, bulletinwatermark.Receipt) error
	LookupWatermarkReceipt(context.Context, string, string, time.Time) (bulletinwatermark.Receipt, error)
	CleanupWatermarkAccount(context.Context, string, string) (int64, int64, error)
}

func (h *Handler) WithBulletinWatermark(repo watermarkRepository, identities investigationIdentityResolver, privacyVersion string) *Handler {
	h.watermarkRepo = repo
	h.watermarkIdentities = identities
	h.watermarkPrivacyVersion = privacyVersion
	return h
}
func watermarkError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, bulletinwatermark.ErrRateLimited):
		w.Header().Set("Retry-After", "60")
		writeError(w, 429, "watermark_rate_limited", "Too many lookups. Please retry later.")
	case errors.Is(err, bulletinwatermark.ErrNotFound):
		writeError(w, 404, "watermark_not_found", "No matching record was found.")
	case errors.Is(err, bulletinwatermark.ErrRestricted):
		writeError(w, 403, "bulletin_processing_restricted", "Document processing is restricted.")
	default:
		writeError(w, 503, "bulletin_download_unavailable", "Document processing is unavailable.")
	}
}
func (h *Handler) lookupBulletinWatermark(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if h.watermarkRepo == nil {
		watermarkError(w, bulletinwatermark.ErrUnavailable)
		return
	}
	p, _ := r.Context().Value(principalKey{}).(principal)
	var input struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &input) {
		return
	}
	code, err := bulletinwatermark.NormalizeCode(input.Code)
	if err != nil {
		writeError(w, 422, "invalid_request", "A valid code is required.")
		return
	}
	v, err := h.watermarkRepo.LookupWatermarkReceipt(r.Context(), code, p.UserID, time.Now().UTC())
	if err != nil {
		watermarkError(w, err)
		return
	}
	channel := v.Channel
	if channel == "" {
		channel = "pdf"
	}
	writeData(w, 200, map[string]any{"receiptId": v.ID, "userId": v.UserID, "issueId": v.IssueID, "series": v.Series, "locale": v.Locale, "revision": v.Revision, "issuedAt": v.IssuedAt, "rendererVersion": v.RendererVersion, "channel": channel}, nil)
}
