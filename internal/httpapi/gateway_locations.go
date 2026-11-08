package httpapi

import (
	"context"
	"crypto/subtle"
	"net/http"
	"path"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

const gatewayLocationsPath = "/priv/gateway/published-locations"
const gatewayLocationsTokenHeader = "X-HHC-CMS-Locations-Token"

var gatewayLocationsRequestID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func (h *Handler) WithGatewayLocationsToken(token string) *Handler {
	h.gatewayLocationsToken = token
	return h
}

func gatewayLocationsGuard(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check before ServeMux can redirect a normalized spelling of the alias.
		if path.Clean(r.URL.Path) != gatewayLocationsPath {
			next.ServeHTTP(w, r)
			return
		}
		rawPath, _, _ := strings.Cut(r.RequestURI, "?")
		if rawPath != gatewayLocationsPath {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		tokens := r.Header.Values(gatewayLocationsTokenHeader)
		if len(tokens) != 1 || subtle.ConstantTimeCompare([]byte(tokens[0]), []byte(token)) != 1 ||
			len(r.Header.Values("Dapr-Caller-App-Id")) != 0 ||
			len(r.Header.Values("Dapr-Api-Token")) != 0 ||
			len(r.Header.Values("X-Internal-Caller-App-Id")) != 0 {
			w.Header().Set("Cache-Control", "no-store")
			writeError(w, http.StatusForbidden, "forbidden", "Trusted gateway credential is required.")
			return
		}
		sanitized := r.Clone(r.Context())
		sanitized.Header = make(http.Header)
		if ids := r.Header.Values("X-HHC-Request-ID"); len(ids) == 1 && gatewayLocationsRequestID.MatchString(ids[0]) {
			sanitized.Header.Set("X-HHC-Request-ID", ids[0])
		}
		if values := r.Header.Values("Traceparent"); len(values) == 1 {
			parent := values[0]
			if len(parent) == 55 && strings.HasPrefix(parent, "00-") && (parent[53:] == "00" || parent[53:] == "01") {
				sanitized.Header.Set("Traceparent", parent)
				ctx := (propagation.TraceContext{}).Extract(context.Background(), propagation.HeaderCarrier(sanitized.Header))
				if !trace.SpanContextFromContext(ctx).IsValid() {
					sanitized.Header.Del("Traceparent")
				}
			}
		}
		next.ServeHTTP(w, sanitized)
	})
}
