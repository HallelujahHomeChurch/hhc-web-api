package httpapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

type principal struct {
	UserID        string
	Roles, Scopes map[string]bool
	RequestID     string
}
type principalKey struct{}

func requireTrusted(caller, daprAPIToken string, allowDevCaller bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actualCaller := strings.TrimSpace(r.Header.Get("Dapr-Caller-App-Id"))
		if actualCaller == "" && allowDevCaller {
			actualCaller = strings.TrimSpace(r.Header.Get("X-Internal-Caller-App-Id"))
		}
		userID := strings.TrimSpace(r.Header.Get("X-HHC-User-ID"))
		provider := strings.TrimSpace(r.Header.Get("X-HHC-Auth-Provider"))
		tokenValid := daprAPIToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("dapr-api-token")), []byte(daprAPIToken)) == 1
		if !tokenValid || actualCaller != caller || userID == "" || provider != "account-api" {
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
func requireAnyScope(scopes []string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := r.Context().Value(principalKey{}).(principal)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Trusted gateway identity is required.")
			return
		}
		for _, scope := range scopes {
			if p.Scopes[scope] || p.Scopes["*"] {
				next(w, r)
				return
			}
		}
		writeError(w, http.StatusForbidden, "forbidden", "The required capability is missing.")
	}
}
func requireCampaignScheduleCreate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := r.Context().Value(principalKey{}).(principal)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Trusted gateway identity is required.")
			return
		}
		if !p.Scopes["*"] && !p.Scopes["cms:write"] && !(p.Scopes["campaigns:write"] && p.Scopes["campaigns:send"]) {
			writeError(w, http.StatusForbidden, "forbidden", "The required capability is missing.")
			return
		}
		next(w, r)
	}
}
func requireCampaignScheduleUpdate(next http.HandlerFunc) http.HandlerFunc {
	return requireAnyScope([]string{"campaigns:write", "cms:write"}, func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAdminProxyBody))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Request body is too large.")
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		var request struct {
			Enabled bool `json:"enabled"`
		}
		if json.Unmarshal(body, &request) == nil && request.Enabled {
			requireAnyScope([]string{"campaigns:send", "cms:write"}, next)(w, r)
			return
		}
		next(w, r)
	})
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
