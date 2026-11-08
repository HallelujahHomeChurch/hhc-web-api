package httpapi

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/accountclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/google/uuid"
)

type investigationStoreStub struct {
	*watermarkStoreStub
	calls                    int
	created                  bulletinwatermark.Investigation
	createErr, errorRead     error
	view                     bulletinwatermark.InvestigationView
	receiptCursor, jobCursor string
	exportErr                error
	history                  bulletinwatermark.InvestigationHistoryPage
	versions                 []bulletinwatermark.CandidateVersion
	sortKey, sortDirection   string
}

func (s *investigationStoreStub) ListWatermarkInvestigations(context.Context, string, string, string, int, time.Time) (bulletinwatermark.InvestigationHistoryPage, error) {
	s.calls++
	return s.history, s.errorRead
}
func (s *investigationStoreStub) ListGlobalWatermarkInvestigations(_ context.Context, _, _ string, _ int, sortKey, direction string, _ time.Time) (bulletinwatermark.InvestigationHistoryPage, error) {
	s.calls++
	s.sortKey, s.sortDirection = sortKey, direction
	return s.history, s.errorRead
}

func (s *investigationStoreStub) ListWatermarkVersions(context.Context, string, time.Time) ([]bulletinwatermark.CandidateVersion, error) {
	s.calls++
	if s.versions != nil {
		return s.versions, s.errorRead
	}
	return []bulletinwatermark.CandidateVersion{{CandidateScope: s.view.Scope, PageCount: 1}}, s.errorRead
}
func (s *investigationStoreStub) CreateWatermarkInvestigation(_ context.Context, v bulletinwatermark.Investigation) error {
	s.calls++
	s.created = v
	return s.createErr
}
func (s *investigationStoreStub) GetWatermarkInvestigation(_ context.Context, id, actor string, _ time.Time) (bulletinwatermark.InvestigationView, error) {
	s.calls++
	if actor != s.view.ActorID || id != s.view.ID {
		return bulletinwatermark.InvestigationView{}, bulletinwatermark.ErrNotFound
	}
	return s.view, s.errorRead
}

type investigationPermissionStub struct {
	identities []accountclient.Identity
	calls      int
}

func (p *investigationPermissionStub) ResolveIdentities(context.Context, []string) ([]accountclient.Identity, error) {
	p.calls++
	return p.identities, nil
}

func investigationHTTPFixture(t *testing.T) (http.Handler, *investigationStoreStub, *investigationPermissionStub) {
	t.Helper()
	scope := bulletinwatermark.CandidateScope{IssueID: uuid.NewString(), Series: "general", Locale: "zh-Hant", Revision: 1, SourceAssetID: uuid.NewString(), SourceSHA256: strings.Repeat("a", 64), RendererVersion: "test-renderer", ModelSHA256: strings.Repeat("b", 64)}
	v := bulletinwatermark.InvestigationView{Investigation: bulletinwatermark.Investigation{ID: uuid.NewString(), ActorID: uuid.NewString(), Scope: scope, Status: "completed", Result: "matched", Evidence: []byte("private-evidence"), LeaseToken: "private-lease", CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}, Receipt: &bulletinwatermark.Receipt{ID: uuid.NewString(), UserID: uuid.NewString(), IssueID: scope.IssueID, Locale: scope.Locale, Revision: 1, FingerprintPayload: "private-payload", RendererVersion: "test-renderer"}}
	store := &investigationStoreStub{watermarkStoreStub: &watermarkStoreStub{}, view: v}
	permission := &investigationPermissionStub{}
	handler := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithBulletinWatermark(store, permission, "").Routes()
	return handler, store, permission
}
func investigationHeaders(request *http.Request, actor, scopes string) {
	request.Header.Set("X-Internal-Caller-App-Id", "api-gateway")
	request.Header.Set("X-HHC-User-ID", actor)
	request.Header.Set("X-HHC-Auth-Provider", "account-api")
	request.Header.Set("X-HHC-Scopes", scopes)
}
func investigationUpload(t *testing.T, metadata string, evidence []byte, duplicate bool) ([]byte, string) {
	return investigationUploadFiles(t, metadata, evidence, 1, duplicate)
}
func investigationUploadFiles(t *testing.T, metadata string, evidence []byte, count int, duplicate bool) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("metadata", metadata); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		part, err := writer.CreateFormFile("files", fmt.Sprintf("screenshot-%d.png", i))
		if err != nil {
			t.Fatal(err)
		}
		part.Write(evidence)
	}
	if duplicate {
		writer.WriteField("metadata", metadata)
	}
	writer.Close()
	return body.Bytes(), writer.FormDataContentType()
}
func tinyInvestigationPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestInvestigationRoutesRequireCurrentPermission(t *testing.T) {
	paths := []struct{ method, path string }{{"GET", "/api/admin/bulletins/00000000-0000-4000-8000-000000000001/watermark-versions"}, {"POST", "/api/admin/bulletin-watermark-investigations"}, {"GET", "/api/admin/bulletin-watermark-investigations"}, {"GET", "/api/admin/bulletin-watermark-investigations/00000000-0000-4000-8000-000000000001"}}
	for _, route := range paths {
		for _, mode := range []string{"scope", "untrusted"} {
			t.Run(route.method+route.path+mode, func(t *testing.T) {
				handler, store, _ := investigationHTTPFixture(t)
				request := httptest.NewRequest(route.method, route.path, nil)
				investigationHeaders(request, store.view.ActorID, "cms:bulletins:investigate")
				want := 403
				switch mode {
				case "scope":
					request.Header.Set("X-HHC-Scopes", "cms:news:read")
				case "untrusted":
					request.Header.Set("X-Internal-Caller-App-Id", "other")
					want = 401
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if response.Code != want || store.calls != 0 {
					t.Fatalf("permission boundary: status=%d calls=%d", response.Code, store.calls)
				}
			})
		}
	}
}

func TestInvestigationCreateValidatesUpload(t *testing.T) {
	for _, mode := range []string{"valid", "issue-only", "five-files", "six-files", "missing-key", "corrupt", "pixel-limit", "byte-limit", "duplicate", "unknown-metadata", "conflict"} {
		t.Run(mode, func(t *testing.T) {
			handler, store, _ := investigationHTTPFixture(t)
			if mode == "issue-only" {
				other := store.view.Scope
				other.Locale, other.Revision, other.SourceAssetID = "en", 2, uuid.NewString()
				children := store.view.Scope
				children.Series, children.SourceAssetID = "children", uuid.NewString()
				store.versions = []bulletinwatermark.CandidateVersion{{CandidateScope: store.view.Scope, PageCount: 1}, {CandidateScope: other, PageCount: 1}, {CandidateScope: children, PageCount: 1}}
			}
			value := map[string]any{"issueId": store.view.Scope.IssueID, "locale": store.view.Scope.Locale}
			if mode == "unknown-metadata" {
				value["url"] = "https://example.invalid/image"
			}
			if mode == "issue-only" {
				delete(value, "locale")
			}
			metadata, _ := json.Marshal(value)
			evidence := tinyInvestigationPNG(t)
			want := 422
			switch mode {
			case "valid", "issue-only", "five-files":
				want = 202
			case "corrupt":
				evidence = []byte("not an image")
			case "pixel-limit":
				binary.BigEndian.PutUint32(evidence[16:20], 5000)
				binary.BigEndian.PutUint32(evidence[20:24], 5000)
				binary.BigEndian.PutUint32(evidence[29:33], crc32.ChecksumIEEE(evidence[12:29]))
			case "byte-limit":
				evidence = make([]byte, (10<<20)+1)
				want = 413
			case "conflict":
				store.createErr = bulletinwatermark.ErrInvestigationConflict
				want = 409
			}
			fileCount := 1
			if mode == "five-files" {
				fileCount = 5
			} else if mode == "six-files" {
				fileCount = 6
			}
			body, contentType := investigationUploadFiles(t, string(metadata), evidence, fileCount, mode == "duplicate")
			request := httptest.NewRequest("POST", "/api/admin/bulletin-watermark-investigations", bytes.NewReader(body))
			investigationHeaders(request, store.view.ActorID, "cms:bulletins:investigate")
			request.Header.Set("Content-Type", contentType)
			key := uuid.NewString()
			if mode != "missing-key" {
				request.Header.Set("Idempotency-Key", key)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != want {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
			if want == 202 {
				files, unpackErr := bulletinwatermark.UnpackEvidence(store.created.Evidence)
				wantScopes := 1
				if mode == "issue-only" {
					wantScopes = 2
				}
				if store.created.ID != key || store.created.ActorID != store.view.ActorID || unpackErr != nil || len(files) != 1 || !bytes.Equal(files[0].Data, evidence) || len(store.created.Scopes) != wantScopes || response.Header().Get("Location") != "/api/admin/bulletin-watermark-investigations/"+key {
					t.Fatal("issuance upload contract")
				}
			} else if want != 409 && store.calls != 0 {
				t.Fatal("invalid upload was persisted")
			}
		})
	}
}

func TestInvestigationResultUsesSafeDTO(t *testing.T) {
	handler, store, _ := investigationHTTPFixture(t)
	request := httptest.NewRequest("GET", "/api/admin/bulletin-watermark-investigations/"+store.view.ID, nil)
	investigationHeaders(request, store.view.ActorID, "cms:bulletins:investigate")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 200 || !strings.Contains(response.Body.String(), store.view.Receipt.UserID) || strings.Contains(response.Body.String(), "private-") || strings.Contains(response.Body.String(), "fingerprintPayload") || strings.Contains(response.Body.String(), `"page"`) || strings.Contains(response.Body.String(), `"reason"`) {
		t.Fatal("unsafe result DTO", response.Body.String())
	}
	request = httptest.NewRequest("GET", "/api/admin/bulletin-watermark-investigations/"+store.view.ID, nil)
	investigationHeaders(request, uuid.NewString(), "cms:bulletins:investigate")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != 404 || strings.Contains(response.Body.String(), store.view.Receipt.UserID) {
		t.Fatal("another actor could read result")
	}
	store.view.Result = "inconclusive"
	request = httptest.NewRequest("GET", "/api/admin/bulletin-watermark-investigations/"+store.view.ID, nil)
	investigationHeaders(request, store.view.ActorID, "cms:bulletins:investigate")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if strings.Contains(response.Body.String(), store.view.Receipt.UserID) || strings.Contains(response.Body.String(), `"receipt"`) {
		t.Fatal("inconclusive leaked identity")
	}
}

func TestInvestigationHistoryReturnsIssueRowsAndSeparatesAccountNavigation(t *testing.T) {
	handler, store, permission := investigationHTTPFixture(t)
	matched := uuid.NewString()
	actualIssue := uuid.NewString()
	store.history = bulletinwatermark.InvestigationHistoryPage{Items: []bulletinwatermark.InvestigationHistoryRow{{ID: uuid.NewString(), ActorID: store.view.ActorID, IssueID: store.view.Scope.IssueID, IssueNumber: 1738, IssueDate: "2026-09-20", ActualIssueID: actualIssue, ActualIssueNumber: 1737, ActualIssueDate: "2026-09-13", Locale: "zh-Hant", Revision: 3, Status: "completed", Result: "matched", MatchedAccountID: matched, CreatedAt: time.Now()}}, NextCursor: uuid.NewString()}
	permission.identities = []accountclient.Identity{{ID: store.view.ActorID, DisplayName: "管理員", Email: "admin@example.com"}, {ID: matched, DisplayName: "會員", Email: "member@example.com"}}
	for _, tc := range []struct {
		scopes   string
		navigate bool
	}{{"cms:bulletins:investigate", false}, {"*", true}, {"cms:bulletins:investigate users:read", true}} {
		request := httptest.NewRequest(http.MethodGet, "/api/admin/bulletins/"+store.view.Scope.IssueID+"/watermark-investigations?limit=20", nil)
		investigationHeaders(request, store.view.ActorID, tc.scopes)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 200 || !strings.Contains(response.Body.String(), "member@example.com") || strings.Contains(response.Body.String(), "fingerprint") || strings.Contains(response.Body.String(), "candidate") {
			t.Fatalf("status=%d body=%s", response.Code, response.Body)
		}
		want := `"canNavigate":true`
		if strings.Contains(response.Body.String(), want) != tc.navigate {
			t.Fatalf("navigate=%v body=%s", tc.navigate, response.Body)
		}
	}
}

func TestGlobalInvestigationHistoryProjectsRequestedAndActualIssues(t *testing.T) {
	handler, store, permission := investigationHTTPFixture(t)
	matched := uuid.NewString()
	actualIssue := uuid.NewString()
	store.history = bulletinwatermark.InvestigationHistoryPage{Items: []bulletinwatermark.InvestigationHistoryRow{{ID: uuid.NewString(), ActorID: store.view.ActorID, ActorDisplayName: "管理員", ActorAvailable: true, IssueID: store.view.Scope.IssueID, IssueNumber: 1738, IssueDate: "2026-09-20", ActualIssueID: actualIssue, ActualIssueNumber: 1737, ActualIssueDate: "2026-09-13", Locale: "zh-Hant", Revision: 4, Status: "completed", Result: "matched", MatchMethod: bulletinwatermark.MatchOutputSHA256, MatchedAccountID: matched, MatchedDisplayName: "會員", MatchedAvailable: true, CreatedAt: time.Now()}}, NextCursor: uuid.NewString()}
	request := httptest.NewRequest(http.MethodGet, "/api/admin/bulletin-watermark-investigations?limit=20", nil)
	investigationHeaders(request, store.view.ActorID, "cms:bulletins:investigate")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != 200 || !strings.Contains(body, `"issueNumber":1738`) || !strings.Contains(body, `"issueNumber":1737`) || !strings.Contains(body, `"resolvedRevision":4`) || !strings.Contains(body, actualIssue) || strings.Contains(body, `"locale":`) || strings.Contains(body, `"canNavigate":true`) {
		t.Fatalf("status=%d body=%s", response.Code, body)
	}
	if response.Header().Get("Cache-Control") != "private, no-store" || store.calls != 1 || permission.calls != 0 {
		t.Fatalf("headers=%v storeCalls=%d identityCalls=%d", response.Header(), store.calls, permission.calls)
	}
}

func TestGlobalInvestigationHistoryValidatesAndForwardsOrdering(t *testing.T) {
	for _, key := range []string{"submitted", "requested", "actual", "status", "operator", "account", "issued"} {
		handler, store, _ := investigationHTTPFixture(t)
		request := httptest.NewRequest(http.MethodGet, "/api/admin/bulletin-watermark-investigations?sort="+key+"&direction=asc", nil)
		investigationHeaders(request, store.view.ActorID, "cms:bulletins:investigate")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || store.sortKey != key || store.sortDirection != "asc" {
			t.Fatalf("key=%s status=%d forwarded=%s/%s", key, response.Code, store.sortKey, store.sortDirection)
		}
	}
	for _, query := range []string{"sort=unknown&direction=asc", "sort=submitted&direction=sideways", "sort=submitted"} {
		handler, store, _ := investigationHTTPFixture(t)
		request := httptest.NewRequest(http.MethodGet, "/api/admin/bulletin-watermark-investigations?"+query, nil)
		investigationHeaders(request, store.view.ActorID, "cms:bulletins:investigate")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != 422 || store.calls != 0 {
			t.Fatalf("query=%s status=%d calls=%d", query, response.Code, store.calls)
		}
	}
}
