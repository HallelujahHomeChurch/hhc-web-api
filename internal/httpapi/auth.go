package httpapi

import (
	"context"
	"net/http"
	"strings"
)

type principal struct {
	UserID        string
	Roles, Scopes map[string]bool
	RequestID     string
}
type principalKey struct{}

func requireTrusted(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID := strings.TrimSpace(r.Header.Get("X-HHC-User-ID"))
		provider := strings.TrimSpace(r.Header.Get("X-HHC-Auth-Provider"))
		if userID == "" || provider != "account-api" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Trusted gateway identity is required.")
			return
		}
		p := principal{UserID: userID, Roles: set(r.Header.Get("X-HHC-Roles")), Scopes: set(r.Header.Get("X-HHC-Scopes")), RequestID: r.Header.Get("X-HHC-Request-ID")}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	})
}
func requireScope(scope string, next http.HandlerFunc) http.HandlerFunc {
	return requireScopes([]string{scope}, next)
}
func requireScopes(scopes []string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := r.Context().Value(principalKey{}).(principal)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Trusted gateway identity is required.")
			return
		}
		for _, scope := range scopes {
			if !p.Scopes[scope] && !p.Scopes["*"] {
				writeError(w, http.StatusForbidden, "forbidden", "The required capability is missing.")
				return
			}
		}
		next(w, r)
	}
}
func set(value string) map[string]bool {
	result := map[string]bool{}
	for _, v := range strings.FieldsFunc(value, func(r rune) bool { return r == ' ' || r == ',' }) {
		result[v] = true
	}
	return result
}
func actor(r *http.Request) string {
	if p, ok := r.Context().Value(principalKey{}).(principal); ok {
		return p.UserID
	}
	return ""
}
