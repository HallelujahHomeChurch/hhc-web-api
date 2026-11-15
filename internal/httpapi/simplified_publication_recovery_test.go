package httpapi

import (
	"context"
	"encoding/json"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type simplifiedRecoveryHTTPRepository struct {
	onlineStateRepository
	calls int
}

func (r *simplifiedRecoveryHTTPRepository) RecoverSimplifiedOnlineBulletinPublication(_ context.Context, _ string, in onlinebulletins.SimplifiedRecoveryInput, _ string, _ string, _ time.Time) (onlinebulletins.SimplifiedRecoveryResult, error) {
	r.calls++
	return onlinebulletins.SimplifiedRecoveryResult{Publication: onlinebulletins.ConfirmedRevision{DocumentID: in.TargetDocumentID, Version: 12, Revision: in.Revision}}, nil
}
func TestSimplifiedRecoveryRoutePermissionsSyntaxAndBoundary(t *testing.T) {
	for _, mode := range []string{"publish", "read", "write", "missing-target", "invalid-hash", "duplicate-key", "control-key", "large-body", "unknown-field"} {
		t.Run(mode, func(t *testing.T) {
			repo := &simplifiedRecoveryHTTPRepository{}
			input := map[string]any{"targetDocumentId": "11111111-1111-4111-8111-111111111111", "canonicalVersion": 1, "source": map[string]any{"documentId": "22222222-2222-4222-8222-222222222222", "revision": 1, "contentHash": strings.Repeat("a", 64), "onlineVersion": 1}, "publicationOperationId": "33333333-3333-4333-8333-333333333333", "revision": 1, "layoutValidationHash": strings.Repeat("b", 64)}
			scope := "cms:bulletins:publish"
			want := 200
			key := "key"
			switch mode {
			case "read":
				scope = "cms:bulletins:read"
				want = 403
			case "write":
				scope = "cms:bulletins:write"
				want = 403
			case "missing-target":
				delete(input, "targetDocumentId")
				want = 400
			case "invalid-hash":
				input["layoutValidationHash"] = strings.Repeat("z", 64)
				want = 400
			case "duplicate-key":
				want = 400
			case "control-key":
				key = "a\x7fb"
				want = 400
			case "large-body":
				input["extra"] = strings.Repeat("a", 4096)
				want = 400
			case "unknown-field":
				input["extra"] = true
				want = 400
			}
			raw, _ := json.Marshal(input)
			request := httptest.NewRequest("POST", "/api/admin/bulletins/018f47d2-e5d1-4f3f-8f18-6c8e32621f73/online/general/zh-Hans/recover-publication", strings.NewReader(string(raw)))
			trustedBulletinAudit(request, scope)
			request.Header.Set("If-Match", `"1"`)
			request.Header.Set("Idempotency-Key", key)
			if mode == "duplicate-key" {
				request.Header.Add("Idempotency-Key", key)
			}
			response := httptest.NewRecorder()
			NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, nil, "api-gateway", "", true).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes().ServeHTTP(response, request)
			if response.Code != want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, want, response.Body.String())
			}
			if want == 200 && (repo.calls != 1 || response.Header().Get("Cache-Control") != "private, no-store" || response.Header().Get("ETag") != `"12"`) {
				t.Fatal("route response contract")
			}
			if want != 200 && repo.calls != 0 {
				t.Fatal("rejected input reached repository")
			}
		})
	}
}
