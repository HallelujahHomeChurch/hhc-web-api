package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
)

func (h *Handler) adminUpdateBulletinPublication(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	if len(r.Header.Values("Idempotency-Key")) != 1 {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	var body struct {
		Metadata json.RawMessage `json:"metadata"`
	}
	if !decodeLimit(w, r, &body, 32<<10) {
		return
	}
	var input bulletins.PublicationUpdateInput
	if len(body.Metadata) > 0 {
		var metadata struct {
			IssueNumber int     `json:"issueNumber"`
			IssueDate   string  `json:"issueDate"`
			Title       string  `json:"title"`
			Subtitle    *string `json:"subtitle"`
		}
		decoder := json.NewDecoder(bytes.NewReader(body.Metadata))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&metadata); err != nil || metadata.Subtitle == nil {
			handleError(w, bulletins.ErrInvalid)
			return
		}
		input.Metadata = &bulletins.PublicationMetadata{IssueNumber: metadata.IssueNumber, IssueDate: metadata.IssueDate, Title: metadata.Title, Subtitle: *metadata.Subtitle}
	}
	result, err := h.service.UpdatePublication(r.Context(), r.PathValue("issueID"), expected, input, r.Header.Get("Idempotency-Key"), actor(r))
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, result.Issue.Version))
	writeData(w, http.StatusAccepted, result, nil)
}
