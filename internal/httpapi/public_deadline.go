package httpapi

import (
	"context"
	"net/http"
	"time"
)

// Bound published JSON reads before the server's 30s write timeout. Socket
// deadlines alone do not cancel SQL or pool checkout. Long-running authenticated
// routes and workers have separate budgets and are not registered on this mux.
func publicReadDeadline(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
