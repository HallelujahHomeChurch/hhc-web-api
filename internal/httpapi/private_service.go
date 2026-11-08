package httpapi

import (
	"context"
	"crypto/subtle"
	"net/http"
	"strings"
)

const privateServiceCallerHeader = "X-HHC-CMS-Service-Caller"
const privateServiceTokenHeader = "X-HHC-CMS-Service-Token"

func (h *Handler) WithPrivateServiceTokens(tokens map[string]string) *Handler {
	h.privateServiceTokens = tokens
	return h
}

func (h *Handler) WithGatewayPrivateTokens(protected, live string) *Handler {
	h.gatewayPrivateToken, h.gatewayLivePrivateToken = protected, live
	return h
}

func (h *Handler) WithoutDapr(disabled bool) *Handler {
	h.daprDisabled = disabled
	return h
}

func hasLegacyCaller(h http.Header) bool {
	return len(h.Values("Dapr-Caller-App-Id")) != 0 ||
		len(h.Values("Dapr-Api-Token")) != 0 ||
		len(h.Values("X-Internal-Caller-App-Id")) != 0 ||
		len(h.Values("X-HHC-Caller-App-Id")) != 0
}

func rejectLegacyCaller(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hasLegacyCaller(r.Header) {
			w.Header().Set("Cache-Control", "private, no-store")
			writeError(w, http.StatusUnauthorized, "unauthorized", "Trusted service identity is required.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type privateServiceCallerKey struct{}

func (h *Handler) requirePrivateService(method string, allowed map[string]bool, next http.Handler) http.Handler {
	return h.privateServiceOrLegacy(method, allowed, requireServiceCaller(allowed, h.daprAPIToken, h.allowDevCaller, next), next)
}

func (h *Handler) privateServiceOrLegacy(method string, allowed map[string]bool, legacy, next http.Handler) http.Handler {
	return privateTransport(legacy, next, func(r *http.Request, caller string) string {
		if r.Method != method || !allowed[caller] {
			return ""
		}
		return h.privateServiceTokens[caller]
	})
}

func privateTransport(legacy, next http.Handler, credential func(*http.Request, string) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callers := r.Header.Values(privateServiceCallerHeader)
		tokens := r.Header.Values(privateServiceTokenHeader)
		if len(callers) == 0 && len(tokens) == 0 {
			legacy.ServeHTTP(w, r)
			return
		}
		if r.URL.RawPath != "" || len(callers) != 1 || len(tokens) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Trusted service identity is required.")
			return
		}
		expected := credential(r, callers[0])
		if expected == "" || subtle.ConstantTimeCompare([]byte(tokens[0]), []byte(expected)) != 1 ||
			hasLegacyCaller(r.Header) {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Trusted service identity is required.")
			return
		}
		r.Header.Del(privateServiceCallerHeader)
		r.Header.Del(privateServiceTokenHeader)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), privateServiceCallerKey{}, callers[0])))
	})
}

// Match the registered mux method and path before changing the transport trust.
func (h *Handler) gatewayPrivateRoutes(routes *http.ServeMux, legacy, next http.Handler) http.Handler {
	return privateTransport(legacy, next, func(r *http.Request, caller string) string {
		_, pattern := routes.Handler(r)
		_, route, matched := strings.Cut(pattern, " ")
		if caller != h.trustedCaller {
			return ""
		}
		if !matched {
			if pattern != "/api/me/statements/{statementID}/dismissal" || (r.Method != http.MethodGet && r.Method != http.MethodPut) {
				return ""
			}
			route = pattern
		}
		if liveGatewayRoute(route) {
			return h.gatewayLivePrivateToken
		}
		return h.gatewayPrivateToken
	})
}

func liveGatewayRoute(route string) bool {
	switch {
	case strings.HasPrefix(route, "/api/ingest/"),
		strings.HasPrefix(route, "/api/admin/broadcast"),
		strings.HasPrefix(route, "/api/admin/studio/"),
		route == "/api/member/broadcasts",
		route == "/api/member/recordings/live":
		return true
	case strings.HasPrefix(route, "/api/admin/recordings/"):
		return strings.Contains(route, "/captures") || strings.Contains(route, "/live-cover-settings")
	case strings.HasPrefix(route, "/api/member/recordings/"), strings.HasPrefix(route, "/api/member/videos/"):
		return strings.Contains(route, "/live/") || strings.HasSuffix(route, "/watch") || strings.HasSuffix(route, "/broadcast-cover")
	default:
		return false
	}
}
