package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/translation"
)

type TranslationPreviewer interface {
	Preview(context.Context, translation.PreviewRequest) (translation.Preview, error)
}

type translationPreviewInput struct {
	SourceLocale    string `json:"sourceLocale"`
	ReplaceExisting *bool  `json:"replaceExisting"`
}

func (h *Handler) adminContentTranslationPreview(w http.ResponseWriter, r *http.Request) {
	h.translationPreview(w, r, r.PathValue("module"), r.PathValue("contentID"))
}

func (h *Handler) adminBulletinTranslationPreview(w http.ResponseWriter, r *http.Request) {
	if !bulletins.IsBulletinTranslationTarget(r.PathValue("targetLocale")) {
		writeError(w, http.StatusBadRequest, "invalid_translation_request", "The translation request is invalid.")
		return
	}
	h.translationPreview(w, r, "bulletins", r.PathValue("issueID"))
}

func (h *Handler) translationPreview(w http.ResponseWriter, r *http.Request, module, resourceID string) {
	if err := http.NewResponseController(w).SetWriteDeadline(h.translationNow().Add(h.translationTTL)); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "The request could not be completed.")
		return
	}
	if h.translation == nil {
		writeError(w, http.StatusServiceUnavailable, "translation_disabled", "Translation previews are disabled.")
		return
	}
	expected, ok := translationIfMatch(r.Header.Get("If-Match"))
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_translation_request", "The translation request is invalid.")
		return
	}
	var input *translationPreviewInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input == nil || input.SourceLocale == "" || input.ReplaceExisting == nil {
		writeError(w, http.StatusBadRequest, "invalid_translation_request", "The translation request is invalid.")
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_translation_request", "The translation request is invalid.")
		return
	}
	preview, err := h.translation.Preview(r.Context(), translation.PreviewRequest{
		Module: module, ResourceID: resourceID, SourceLocale: input.SourceLocale, TargetLocale: r.PathValue("targetLocale"),
		ExpectedVersion: expected, Actor: actor(r), ReplaceExisting: *input.ReplaceExisting,
	})
	if err != nil {
		handleTranslationError(w, err)
		return
	}
	writeData(w, http.StatusOK, preview, nil)
}

func translationIfMatch(header string) (int64, bool) {
	header = strings.TrimSpace(header)
	if len(header) < 3 || header[0] != '"' || header[len(header)-1] != '"' {
		return 0, false
	}
	raw := header[1 : len(header)-1]
	value, err := strconv.ParseInt(raw, 10, 64)
	return value, err == nil && value > 0 && strconv.FormatInt(value, 10) == raw
}

func handleTranslationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, translation.ErrInvalidRequest):
		writeError(w, http.StatusBadRequest, "invalid_translation_request", "The translation request is invalid.")
	case errors.Is(err, translation.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "The source resource was not found.")
	case errors.Is(err, translation.ErrTranslationExists):
		writeError(w, http.StatusConflict, "translation_exists", "The target translation already exists.")
	case errors.Is(err, translation.ErrVersionMismatch):
		writeError(w, http.StatusPreconditionFailed, "version_mismatch", "The source changed. Reload and try again.")
	case errors.Is(err, translation.ErrRateLimited):
		writeError(w, http.StatusTooManyRequests, "translation_rate_limited", "The translation request limit was reached.")
	case errors.Is(err, translation.ErrProvider):
		writeError(w, http.StatusBadGateway, "translation_provider_error", "The translation provider could not complete the request.")
	case errors.Is(err, translation.ErrTimeout):
		writeError(w, http.StatusGatewayTimeout, "translation_timeout", "The translation request timed out.")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "The request could not be completed.")
	}
}
