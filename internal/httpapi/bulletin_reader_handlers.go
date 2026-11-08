package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinreader"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
)

func privateReaderError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, bulletinreader.ErrMapping):
		writeError(w, 409, "mapping_history_unavailable", "Published mapping history is unavailable. Keep local changes and retry later.")
	case errors.Is(err, bulletinreader.ErrMutationReuse):
		writeError(w, 409, "mutation_id_conflict", "This mutation ID already belongs to a different operation.")
	case errors.Is(err, bulletinreader.ErrInvalid):
		memberOnlineError(w, r, bulletins.ErrInvalid)
	default:
		memberOnlineError(w, r, err)
	}
}
func readerSeriesValid(w http.ResponseWriter, r *http.Request) bool {
	values := r.URL.Query()["series"]
	if len(values) != 1 || values[0] == "" || values[0] != protectedBulletinFrom(r).series {
		memberOnlineError(w, r, bulletins.ErrInvalid)
		return false
	}
	return true
}
func (h *Handler) memberReaderState(w http.ResponseWriter, r *http.Request) {
	if !readerSeriesValid(w, r) {
		return
	}
	var from *int64
	if values, exists := r.URL.Query()["fromRevision"]; exists {
		if len(values) != 1 {
			memberOnlineError(w, r, bulletins.ErrInvalid)
			return
		}
		n, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || strconv.FormatInt(n, 10) != values[0] || n < 1 || n > 9007199254740991 {
			memberOnlineError(w, r, bulletins.ErrInvalid)
			return
		}
		from = &n
	}
	request := protectedBulletinFrom(r)
	actor, _ := r.Context().Value(principalKey{}).(principal)
	value, err := h.service.ReaderState(r.Context(), r.PathValue("issueID"), request.series, request.locale, actor.UserID, from)
	if err != nil {
		privateReaderError(w, r, err)
		return
	}
	writeData(w, 200, value, nil)
}
func (h *Handler) memberReaderMutations(w http.ResponseWriter, r *http.Request) {
	if !readerSeriesValid(w, r) {
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, 413, "request_too_large", "Reader batches are limited to 1 MiB.")
		} else {
			memberOnlineError(w, r, bulletins.ErrInvalid)
		}
		return
	}
	var input struct {
		Mutations []bulletinreader.Mutation `json:"mutations"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if !utf8.Valid(raw) || decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || len(input.Mutations) < 1 || len(input.Mutations) > 100 {
		memberOnlineError(w, r, bulletins.ErrInvalid)
		return
	}
	request := protectedBulletinFrom(r)
	actor, _ := r.Context().Value(principalKey{}).(principal)
	value, err := h.service.MutateReader(r.Context(), r.PathValue("issueID"), request.series, request.locale, actor.UserID, input.Mutations)
	if err != nil {
		privateReaderError(w, r, err)
		return
	}
	writeData(w, 200, value, nil)
}
