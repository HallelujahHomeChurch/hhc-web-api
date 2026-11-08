package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
)

func serviceRequest(method, path string) *http.Request {
	r := httptest.NewRequest(method, path, nil)
	r.Header.Set("Dapr-Caller-App-Id", "api-gateway")
	r.Header.Set("X-HHC-Auth-Provider", "account-api")
	r.Header.Set("X-HHC-Principal-Type", "service")
	r.Header.Set("X-HHC-Principal-ID", "0199a000-0000-7000-8000-000000000001")
	r.Header.Set("X-HHC-Client-ID", "recording-uploader")
	r.Header.Set("X-HHC-Credential-ID", "0199a000-0000-7000-8000-000000000002")
	r.Header.Set("X-HHC-Request-ID", "service-recording-request")
	r.Header.Set("X-HHC-Scopes", "cms:recordings:read cms:recordings:write cms:recordings:publish")
	return r
}

func TestServiceActorIsNotMemberAndRetainsAuditProvenance(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.Context().Value(principalKey{}).(principal)
		provenance, ok := auditclient.ProvenanceFromContext(r.Context())
		if p.UserID != "" || actor(r) != "0199a000-0000-7000-8000-000000000001" || !ok || provenance.ActorType != "service" || provenance.ActorID != actor(r) {
			t.Fatalf("service treated as user: %#v %#v", p, provenance)
		}
		w.WriteHeader(204)
	})
	h := requireTrusted("api-gateway", "", false, next)
	id := "0199a000-0000-4000-8000-000000000003"
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{"POST", "/api/admin/recordings", 204}, {"GET", "/api/admin/recordings/" + id, 204},
		{"POST", "/api/admin/recordings/" + id + "/publish", 204},
		{"GET", "/api/member/recordings", 403}, {"GET", "/api/admin/bulletins", 403},
		{"POST", "/api/admin/recordings/" + id + "/upload-sessions", 403},
		{"DELETE", "/api/admin/recordings/" + id, 403}, {"GET", "/api/admin/recordings/garbage", 403},
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, serviceRequest(tc.method, tc.path))
		if w.Code != tc.want {
			t.Fatalf("%s %s status=%d want=%d", tc.method, tc.path, w.Code, tc.want)
		}
	}
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.Header.Set("X-HHC-User-ID", "forged-user") },
		func(r *http.Request) { r.Header.Set("X-HHC-Principal-Type", "robot") },
		func(r *http.Request) { r.Header.Del("X-HHC-Credential-ID") },
		func(r *http.Request) { r.Header.Set("X-HHC-Roles", "admin") },
		func(r *http.Request) { r.Header.Set("X-HHC-Scopes", "*") },
		func(r *http.Request) { r.Header.Add("X-HHC-Principal-ID", "other") },
	} {
		r := serviceRequest("POST", "/api/admin/recordings")
		mutate(r)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 401 {
			t.Fatalf("ambiguous identity accepted: %d", w.Code)
		}
	}
}
