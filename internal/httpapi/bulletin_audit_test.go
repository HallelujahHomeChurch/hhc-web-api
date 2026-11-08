package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

const (
	bulletinAuditActor   = "018f47d2-e5d1-7f3f-8f18-6c8e32621f71"
	bulletinAuditRequest = "request-bulletin-1"
)

type bulletinAuditOutbox struct {
	events []auditclient.Event
	err    error
}

func (s *bulletinAuditOutbox) Enqueue(_ context.Context, event auditclient.Event, _ time.Time) (publication.Event, error) {
	s.events = append(s.events, event)
	return publication.Event{}, s.err
}

func TestAdminBulletinReadsPersistAuditBeforeResponse(t *testing.T) {
	now := time.Date(2026, 9, 16, 3, 0, 0, 0, time.UTC)
	issueID := "018f47d2-e5d1-4f3f-8f18-6c8e32621f73"
	assetID := strings.Repeat("a", 32)
	tests := []struct {
		path, action, resourceID string
		metadata                 auditclient.Metadata
	}{
		{"/api/admin/bulletins?status=draft&q=week&page=2", "cms.bulletin.list", "", auditclient.Metadata{}},
		{"/api/admin/bulletins/" + issueID, "cms.bulletin.read", issueID, auditclient.Metadata{}},
		{"/api/admin/bulletins/" + strings.ToUpper(issueID) + "/revisions", "cms.bulletin.revisions.read", issueID, auditclient.Metadata{}},
		{"/api/admin/bulletins/" + issueID + "/assets/" + assetID, "cms.bulletin.asset_status.read", assetID, auditclient.Metadata{}},
	}
	for _, test := range tests {
		t.Run(test.action, func(t *testing.T) {
			repo := &apiRepository{issue: bulletins.Issue{ID: issueID, Version: 7}, revisions: []bulletins.Revision{}}
			uploads := &apiUploads{completed: assetclient.Asset{ID: assetID, Namespace: "cms.weekly.pdf", OwnerService: "hhc-web-api", OwnerType: "bulletin_issue", OwnerID: issueID}}
			outbox := &bulletinAuditOutbox{}
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			trustedBulletinAudit(request, "cms:bulletins:read")
			response := httptest.NewRecorder()
			New(bulletins.NewService(repo, func() time.Time { return now }), nil, uploads).
				WithAuditOutbox(outbox, func() time.Time { return now }).Routes().ServeHTTP(response, request)
			if response.Code != http.StatusOK || len(outbox.events) != 1 {
				t.Fatalf("status=%d events=%d body=%s", response.Code, len(outbox.events), response.Body.String())
			}
			event := outbox.events[0]
			if event.Action != test.action || event.ResourceID != test.resourceID || event.ActorID != bulletinAuditActor || event.RequestID != bulletinAuditRequest || !reflect.DeepEqual(event.Metadata, test.metadata) {
				t.Fatalf("event=%#v", event)
			}
		})
	}
}

func TestAdminBulletinAuditFailureDoesNotReturnData(t *testing.T) {
	issueID := "018f47d2-e5d1-4f3f-8f18-6c8e32621f73"
	repo := &apiRepository{issue: bulletins.Issue{ID: issueID, Version: 7}}
	request := httptest.NewRequest(http.MethodGet, "/api/admin/bulletins/"+issueID, nil)
	trustedBulletinAudit(request, "cms:bulletins:read")
	response := httptest.NewRecorder()
	New(bulletins.NewService(repo, time.Now), nil, &apiUploads{}).
		WithAuditOutbox(&bulletinAuditOutbox{err: errors.New("injected")}, time.Now).Routes().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || response.Header().Get("ETag") != "" || strings.Contains(response.Body.String(), issueID) || !strings.Contains(response.Body.String(), `"code":"audit_unavailable"`) {
		t.Fatalf("status=%d headers=%v body=%s", response.Code, response.Header(), response.Body.String())
	}
}

func TestAdminBulletinAuditRejectsUntrustedProvenanceBeforeAssetCall(t *testing.T) {
	issueID := "018f47d2-e5d1-4f3f-8f18-6c8e32621f73"
	assetID := strings.Repeat("a", 32)
	uploads := &apiUploads{completed: assetclient.Asset{ID: assetID, Namespace: "cms.weekly.pdf", OwnerService: "hhc-web-api", OwnerType: "bulletin_issue", OwnerID: issueID}}
	request := httptest.NewRequest(http.MethodGet, "/api/admin/bulletins/"+issueID+"/assets/"+assetID, nil)
	trustedBulletinAudit(request, "cms:bulletins:read")
	request.Header.Add("X-HHC-Request-ID", bulletinAuditRequest)
	response := httptest.NewRecorder()
	New(bulletins.NewService(&apiRepository{}, time.Now), nil, uploads).
		WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable || uploads.getCalls != 0 {
		t.Fatalf("status=%d assetCalls=%d body=%s", response.Code, uploads.getCalls, response.Body.String())
	}
}

func TestAssetOwnedBulletinMutationsDoNotEmitHHCAuditEvents(t *testing.T) {
	issueID := "018f47d2-e5d1-4f3f-8f18-6c8e32621f73"
	assetID := strings.Repeat("a", 32)
	tests := []struct {
		name, path, body string
		prepare          func(*apiUploads)
		want             int
	}{
		{"upload session", "/api/admin/bulletins/" + issueID + "/upload-sessions", `{"locale":"en","fileName":"weekly.pdf","mimeType":"application/pdf","sizeBytes":100}`, func(*apiUploads) {}, http.StatusCreated},
		{"scan retry", "/api/admin/bulletins/" + issueID + "/assets/" + assetID + "/scan/retry", "", func(uploads *apiUploads) {
			uploads.completed = assetclient.Asset{ID: assetID, Namespace: "cms.weekly.pdf", OwnerService: "hhc-web-api", OwnerType: "bulletin_issue", OwnerID: issueID, ScanStatus: "failed"}
		}, http.StatusOK},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			uploads := &apiUploads{}
			test.prepare(uploads)
			outbox := &bulletinAuditOutbox{}
			request := httptest.NewRequest(http.MethodPost, test.path, strings.NewReader(test.body))
			trustedBulletinAudit(request, "cms:bulletins:write")
			request.Header.Set("Idempotency-Key", "asset-owned")
			response := httptest.NewRecorder()
			New(bulletins.NewService(&apiRepository{issue: bulletins.Issue{ID: issueID, Version: 1}}, time.Now), nil, uploads).
				WithAuditOutbox(outbox, time.Now).Routes().ServeHTTP(response, request)
			if response.Code != test.want || len(outbox.events) != 0 {
				t.Fatalf("status=%d events=%d body=%s", response.Code, len(outbox.events), response.Body.String())
			}
		})
	}
}

func trustedBulletinAudit(request *http.Request, scopes string) {
	request.Header.Set("Dapr-Caller-App-Id", "api-gateway")
	request.Header.Set("X-HHC-User-ID", bulletinAuditActor)
	request.Header.Set("X-HHC-Auth-Provider", "account-api")
	request.Header.Set("X-HHC-Scopes", scopes)
	request.Header.Set("X-HHC-Request-ID", bulletinAuditRequest)
}
