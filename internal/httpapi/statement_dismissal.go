package httpapi

import (
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"io"
	"mime"
	"net/http"
	"strconv"
)

func (h *Handler) statementDismissal(w http.ResponseWriter, r *http.Request) {
	ref := content.StatementRef{StatementID: r.PathValue("statementID")}
	switch r.Method {
	case http.MethodGet:
		values := r.URL.Query()["publishedVersion"]
		var err error
		if len(values) != 1 {
			writeError(w, 422, "invalid_request", "A published version is required.")
			return
		}
		ref.PublishedVersion, err = strconv.ParseInt(values[0], 10, 64)
		if err != nil {
			writeError(w, 422, "invalid_request", "Invalid published version.")
			return
		}
	case http.MethodPut:
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeError(w, 415, "invalid_request", "JSON is required.")
			return
		}
		var body struct {
			PublishedVersion int64 `json:"publishedVersion"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
		decoder.DisallowUnknownFields()
		var trailing any
		if decoder.Decode(&body) != nil || decoder.Decode(&trailing) != io.EOF {
			writeError(w, 422, "invalid_request", "Invalid preference.")
			return
		}
		ref.PublishedVersion = body.PublishedVersion
	default:
		w.Header().Set("Allow", "GET, PUT")
		writeError(w, 405, "method_not_allowed", "This method is not supported.")
		return
	}
	var result content.StatementDismissal
	var err error
	if r.Method == http.MethodPut {
		result, err = h.content.DismissStatement(r.Context(), actor(r), ref)
	} else {
		result, err = h.content.GetStatementDismissal(r.Context(), actor(r), ref)
	}
	switch {
	case errors.Is(err, content.ErrInvalid):
		writeError(w, 422, "invalid_request", "Invalid statement preference.")
	case errors.Is(err, content.ErrStatementVersionChanged):
		writeError(w, 409, "statement_version_changed", "The published statement changed. Reload and try again.")
	case errors.Is(err, content.ErrStatementDismissalRestricted):
		writeError(w, 403, "processing_restricted", "Statement preference processing is restricted.")
	case err != nil:
		handleContentError(w, err)
	default:
		writeData(w, 200, result, nil)
	}
}
