package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"io"
	"net/http"
)

func (h *Handler) adminOnlineRecoverPublication(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	expected, ok := onlineIfMatch(w, r)
	if !ok {
		return
	}
	if len(r.Header.Values("Idempotency-Key")) != 1 {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	var input onlinebulletins.RecoveryInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		handleError(w, bulletins.ErrInvalid)
		return
	}
	input.OnlineVersion = expected
	result, err := h.service.RecoverOnlinePublication(r.Context(), r.PathValue("issueID"), r.PathValue("series"), r.PathValue("contentLocale"), input, r.Header.Get("Idempotency-Key"), actor(r))
	if err != nil {
		var blocked *onlinebulletins.RecoveryBlockedError
		if errors.As(err, &blocked) {
			recordErrorCode(w, "recovery_blocked")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(envelope{Data: nil, Meta: map[string]any{"blockers": blocked.Blockers}, Error: map[string]string{"code": "recovery_blocked", "message": "The saved draft needs explicit review before publication."}})
			return
		}
		h.onlineWriteError(w, r, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, result.Publication.Version))
	writeData(w, http.StatusOK, result, nil)
}
