package httpapi

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
)

type principal struct {
	UserID                                         string
	PrincipalID, ActorType, ClientID, CredentialID string
	Roles, Scopes                                  map[string]bool
	RequestID                                      string
}
type principalKey struct{}

func requireTrusted(caller, daprAPIToken string, allowDevCaller bool, next http.Handler) http.Handler {
	return requireTrustedCallers(map[string]bool{caller: true}, daprAPIToken, allowDevCaller, next)
}

func requireTrustedCallers(allowed map[string]bool, daprAPIToken string, allowDevCaller bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actualCaller, direct := r.Context().Value(privateServiceCallerKey{}).(string)
		if !direct {
			actualCaller = strings.TrimSpace(r.Header.Get("Dapr-Caller-App-Id"))
			if actualCaller == "" && allowDevCaller {
				actualCaller = strings.TrimSpace(r.Header.Get("X-Internal-Caller-App-Id"))
			}
		}
		userID := strings.TrimSpace(r.Header.Get("X-HHC-User-ID"))
		provider := strings.TrimSpace(r.Header.Get("X-HHC-Auth-Provider"))
		tokenValid := direct || daprAPIToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("dapr-api-token")), []byte(daprAPIToken)) == 1
		if !tokenValid || !allowed[actualCaller] || provider != "account-api" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Trusted gateway identity is required.")
			return
		}
		p := principal{UserID: userID, Roles: set(r.Header.Get("X-HHC-Roles")), Scopes: set(r.Header.Get("X-HHC-Scopes")), RequestID: r.Header.Get("X-HHC-Request-ID")}
		p.PrincipalID, p.ActorType = r.Header.Get("X-HHC-Principal-ID"), r.Header.Get("X-HHC-Principal-Type")
		p.ClientID, p.CredentialID = r.Header.Get("X-HHC-Client-ID"), r.Header.Get("X-HHC-Credential-ID")
		valid := false
		switch p.ActorType {
		case "":
			// Known legacy Gateway human contract; never infer a service as user.
			valid = p.PrincipalID == "" && p.CredentialID == "" && userID != ""
			p.ActorType, p.PrincipalID = "user", userID
		case "user":
			valid = userID != "" && p.PrincipalID == userID && p.CredentialID == ""
		case "service":
			valid = userID == "" && len(p.Roles) == 0 && r.Header.Get("X-HHC-Role-IDs") == "" && r.Header.Get("X-HHC-Session-ID") == "" && serviceClientID.MatchString(p.ClientID) && auditclient.ValidActorRequest(p.PrincipalID, p.RequestID) && auditclient.ValidActorRequest(p.CredentialID, p.RequestID) && len(p.Scopes) > 0
			for _, header := range []string{"Principal-ID", "Principal-Type", "Client-ID", "Credential-ID", "Request-ID"} {
				valid = valid && len(r.Header.Values("X-HHC-"+header)) == 1
			}
			for scope := range p.Scopes {
				valid = valid && (scope == "cms:recordings:read" || scope == "cms:recordings:write" || scope == "cms:recordings:publish")
			}
		}
		if !valid {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Trusted gateway identity is required.")
			return
		}
		if p.ActorType == "service" && !serviceRecordingRequest(r) {
			writeError(w, http.StatusForbidden, "forbidden", "This API does not support service principals.")
			return
		}
		ctx := context.WithValue(r.Context(), principalKey{}, p)
		next.ServeHTTP(w, r.WithContext(auditclient.WithPrincipalProvenance(ctx, p.ActorType, p.PrincipalID, p.RequestID, p.CredentialID)))
	})
}

var serviceClientID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var packageID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func serviceRecordingRequest(r *http.Request) bool {
	if strings.Contains(r.URL.EscapedPath(), "%") {
		return false
	}
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 || parts[0] != "" || parts[1] != "api" || parts[2] != "admin" || parts[3] != "recordings" {
		return false
	}
	if len(parts) == 4 {
		return r.Method == http.MethodGet || r.Method == http.MethodPost
	}
	if !auditclient.ValidActorRequest(parts[4], "recording-route") {
		return false
	}
	if len(parts) == 5 {
		return r.Method == http.MethodGet || r.Method == http.MethodPatch
	}
	if len(parts) == 6 {
		switch parts[5] {
		case "covers":
			return r.Method == http.MethodGet
		case "cover-uploads":
			return r.Method == http.MethodPost
		case "cover":
			return r.Method == http.MethodPut
		case "publish", "unpublish", "packages":
			return r.Method == http.MethodPost
		case "exposure":
			return r.Method == http.MethodPatch
		}
	}
	if len(parts) == 8 && parts[5] == "covers" && assetclient.RecordingCoverID.MatchString(parts[6]) && parts[7] == "content" {
		return r.Method == http.MethodGet
	}
	if len(parts) >= 7 && parts[5] == "packages" && packageID.MatchString(parts[6]) {
		if len(parts) == 7 {
			return r.Method == http.MethodGet
		}
		return len(parts) == 8 && r.Method == http.MethodPost && (parts[7] == "sign" || parts[7] == "complete")
	}
	return false
}

func requireServiceCaller(allowed map[string]bool, daprAPIToken string, allowDevCaller bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller := strings.TrimSpace(r.Header.Get("Dapr-Caller-App-Id"))
		if caller == "" && allowDevCaller {
			caller = strings.TrimSpace(r.Header.Get("X-Internal-Caller-App-Id"))
		}
		tokenValid := daprAPIToken == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("dapr-api-token")), []byte(daprAPIToken)) == 1
		if !tokenValid || !allowed[caller] {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Trusted service identity is required.")
			return
		}
		next.ServeHTTP(w, r)
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
func requireContentScope(action string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		family := ""
		switch r.PathValue("module") {
		case "news":
			family = "news"
		case "history", "videos", "locations", "pages":
			family = "pages"
		default:
			writeError(w, http.StatusForbidden, "forbidden", "The required capability is missing.")
			return
		}
		requireScope("cms:"+family+":"+action, next)(w, r)
	}
}
func requireCampaignScheduleCreate(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := r.Context().Value(principalKey{}).(principal)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Trusted gateway identity is required.")
			return
		}
		if !p.Scopes["*"] && !(p.Scopes["campaigns:write"] && p.Scopes["campaigns:send"]) {
			writeError(w, http.StatusForbidden, "forbidden", "The required capability is missing.")
			return
		}
		next(w, r)
	}
}
func requireCampaignScheduleUpdate(next http.HandlerFunc) http.HandlerFunc {
	return requireScope("campaigns:write", func(w http.ResponseWriter, r *http.Request) {
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
			requireScope("campaigns:send", next)(w, r)
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
		if p.ActorType == "service" {
			return p.PrincipalID
		}
		return p.UserID
	}
	return ""
}
