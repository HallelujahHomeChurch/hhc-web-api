package httpapi

import (
	"context"
	"errors"
	"net/http"
)

// Classify only cancellation of this incoming request, not an independent
// dependency failure or deadline. This remains an unsuccessful request.
func writeMemberCancellation(w http.ResponseWriter, r *http.Request, err error) bool {
	if r.Context().Err() != context.Canceled || !errors.Is(err, context.Canceled) {
		return false
	}
	writeError(w, 499, "request_canceled", "The request was canceled.")
	return true
}
