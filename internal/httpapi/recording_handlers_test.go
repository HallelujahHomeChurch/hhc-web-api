package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

type publishedRecordingRepository struct{ recordings.Repository }

type packageRecordingRepository struct{ recordings.Repository }

type progressRecordingRepository struct{ recordings.Repository }

func (progressRecordingRepository) Get(_ context.Context, id string) (recordings.Recording, error) {
	at := time.Now().UTC()
	return recordings.Recording{ID: id, Title: "Meeting", Status: "draft", Version: 1, Broadcast: true, BroadcastStartedAt: &at, ProcessingProgress: &assetclient.RecordingProcessingProgress{Attempt: 1, Phase: "package_validation", AttemptStartedAt: at, PhaseStartedAt: at, LastProgressAt: at, HeartbeatAt: at}}, nil
}

func TestAdminRecordingProgressResponseRequiresReadScope(t *testing.T) {
	const id = "0199a000-0000-4000-8000-000000000003"
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(progressRecordingRepository{}, "", nil, time.Now)).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes()
	for _, service := range []bool{false, true} {
		for _, scope := range []string{"cms:recordings:read", "cms:recordings:write"} {
			r := serviceRequest(http.MethodGet, "/api/admin/recordings/"+id)
			if !service {
				r = httptest.NewRequest(http.MethodGet, "/api/admin/recordings/"+id, nil)
				trustedBulletinAudit(r, scope)
			}
			r.Header.Set("X-HHC-Scopes", scope)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			want := 200
			if scope != "cms:recordings:read" {
				want = 403
			}
			if w.Code != want {
				t.Fatalf("service=%v scope=%s status=%d %s", service, scope, w.Code, w.Body)
			}
			if want == 200 && (!strings.Contains(w.Body.String(), `"processingProgress"`) || !strings.Contains(w.Body.String(), `"isBroadcast":true`) || !strings.Contains(w.Body.String(), `"broadcastStartedAt"`) || w.Header().Get("Cache-Control") != "private, no-store") {
				t.Fatalf("summary or cache boundary missing: %s", w.Body)
			}
		}
	}
}

func (packageRecordingRepository) Get(_ context.Context, id string) (recordings.Recording, error) {
	return recordings.Recording{ID: id, PackageID: "0123456789abcdef0123456789abcdef", DraftExpiresAt: time.Now().Add(time.Hour)}, nil
}

func TestRecordingPackageHTTPPreservesAcceptedAndProtectsWrite(t *testing.T) {
	const id = "0199a000-0000-4000-8000-000000000003"
	const pkg = "0123456789abcdef0123456789abcdef"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/priv/recording-packages/"+pkg+"/complete" || r.Header.Get("X-HHC-Actor-ID") == "" {
			t.Error("incorrect package provenance")
		}
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(assetclient.RecordingPackage{PackageID: pkg, SessionID: pkg, RecordingID: id, State: "freezing"})
	}))
	defer server.Close()
	handler := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(packageRecordingRepository{}, "", nil, time.Now).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes()
	for _, scope := range []string{"cms:recordings:read", "cms:recordings:write"} {
		r := serviceRequest(http.MethodPost, "/api/admin/recordings/"+id+"/packages/"+pkg+"/complete")
		r.Header.Set("X-HHC-Scopes", scope)
		r.Body = io.NopCloser(strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		want := http.StatusAccepted
		if scope == "cms:recordings:read" {
			want = http.StatusForbidden
		}
		if w.Code != want {
			t.Fatalf("scope=%s status=%d body=%s", scope, w.Code, w.Body)
		}
		if want == http.StatusAccepted && (!strings.Contains(w.Body.String(), `"state":"freezing"`) || w.Header().Get("Cache-Control") != "private, no-store") {
			t.Fatal("lost asynchronous status or cache policy")
		}
	}
	if calls != 1 {
		t.Fatalf("forbidden request reached Asset: %d", calls)
	}
}

func (publishedRecordingRepository) Get(context.Context, string) (recordings.Recording, error) {
	return recordings.Recording{ID: "rec-a", Status: "published", Version: 1}, nil
}

func TestRetiredRecordingExposureCannotRestoreHiddenOrFeatured(t *testing.T) {
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPatch, "/api/admin/recordings/rec-a/exposure", `{"featured":true,"hidden":false}`},
	} {
		handler := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(publishedRecordingRepository{}, "", nil, nil)).Routes()
		request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		trusted(request, "cms:recordings:read cms:recordings:publish")
		request.Header.Set("If-Match", `"1"`)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusGone {
			t.Fatalf("%s status=%d body=%s", tc.path, response.Code, response.Body.String())
		}
	}
}

func TestRecordingRejectsLegacyWriteFields(t *testing.T) {
	handler := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(nil, "", nil, nil)).Routes()
	for _, tc := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/admin/recordings", `{"title":"Video","occurrenceId":"meeting"}`},
		{http.MethodPost, "/api/admin/recordings/rec-a/publish", `{"assetVersionId":"other"}`},
	} {
		r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		trusted(r, "cms:recordings:read cms:recordings:write cms:recordings:publish")
		r.Header.Set("If-Match", `"1"`)
		r.Header.Set("Idempotency-Key", "legacy-replay")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d body=%s", tc.path, w.Code, w.Body)
		}
	}
}

func TestMemberRecordingRoutesAuthorizeBeforeAnyDataRead(t *testing.T) {
	for _, tc := range []struct{ method, path, body string }{
		{"GET", "/api/member/recordings/rec-a/interactions", ""},
		{"GET", "/api/member/recordings/rec-a/metrics", ""},
		{"POST", "/api/member/recordings/rec-a/heartbeat", `{}`},
		{"PUT", "/api/member/recordings/rec-a/like", `{"liked":true}`},
		{"GET", "/api/member/recordings/rec-a/comments", ""},
		{"POST", "/api/member/recordings/rec-a/comments", `{}`},
		{"PATCH", "/api/member/recordings/rec-a/comments/c1", `{}`},
		{"DELETE", "/api/member/recordings/rec-a/comments/c1", `{}`},
		{"PUT", "/api/member/recordings/rec-a/comments/c1/report", `{}`},
		{http.MethodGet, "/api/member/recordings", ""},
		{http.MethodGet, "/api/member/recordings/live", ""},
		{http.MethodPost, "/api/member/recordings/rec-a/live/playback", `{"captureId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","playbackScopeId":"00000000-0000-4000-8000-000000000003"}`},
		{http.MethodGet, "/api/member/recordings/rec-a/cover", ""},
		{http.MethodPost, "/api/member/recordings/rec-a/playback", `{"playbackScopeId":"scope-a"}`},
	} {
		var codes []string
		handler := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).
			WithBulletinEntitlements(entitlementCheckerStub{decision: "deny", codes: &codes}).
			WithRecordings(recordings.NewService(nil, "", nil, nil)).Routes()
		request := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		trusted(request, "")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusNotFound || len(codes) != 1 || codes[0] != recordings.EntitlementCode {
			t.Fatalf("%s %s status=%d codes=%v", tc.method, tc.path, response.Code, codes)
		}
	}
}

func TestRecordingRoutesSeparateGatewayAndPublishCapability(t *testing.T) {
	handler := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).
		WithBulletinEntitlements(entitlementCheckerStub{decision: "allow"}).
		WithRecordings(recordings.NewService(nil, "", nil, nil)).Routes()
	bot := httptest.NewRequest(http.MethodGet, "/api/member/recordings", nil)
	trusted(bot, "")
	bot.Header.Set("Dapr-Caller-App-Id", "hhc-line-function-bot")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, bot)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("bot status=%d", response.Code)
	}
	writeOnly := httptest.NewRequest(http.MethodPatch, "/api/admin/recordings/rec-a/exposure", strings.NewReader(`{"featured":true}`))
	trusted(writeOnly, "cms:recordings:write")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, writeOnly)
	if response.Code != http.StatusForbidden {
		t.Fatalf("write-only exposure status=%d", response.Code)
	}
}

func TestServicePublicationRequiresOneBoundedOperationKeyBeforeDataRead(t *testing.T) {
	handler := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(nil, "", nil, nil)).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes()
	for _, keys := range [][]string{nil, {""}, {"first", "second"}, {"bad key"}, {strings.Repeat("a", 129)}} {
		r := serviceRequest(http.MethodPost, "/api/admin/recordings/0199a000-0000-4000-8000-000000000003/publish")
		r.Body = io.NopCloser(strings.NewReader(`{}`))
		r.Header.Set("If-Match", `"1"`)
		for _, key := range keys {
			r.Header.Add("Idempotency-Key", key)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest || w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("keys=%q status=%d body=%s", keys, w.Code, w.Body)
		}
	}
}

type publicationReplayRepository struct{ recordings.Repository }

func (publicationReplayRepository) GetPublishOperation(_ context.Context, id string, expected int64, actor, key string, _ time.Time) (recordings.PublishResult, error) {
	return recordings.PublishResult{Receipt: recordings.PublishReceipt{OperationKey: key, ActorID: actor, ExpectedVersion: expected, PublishedVersion: 2}, Current: recordings.Recording{ID: id, Status: "draft", Version: 3}, Outcome: "state_changed"}, nil
}

func TestServicePublicationHTTPReturnsReceiptAndChangedCurrentState(t *testing.T) {
	handler := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(publicationReplayRepository{}, "", nil, time.Now)).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes()
	r := serviceRequest(http.MethodPost, "/api/admin/recordings/0199a000-0000-4000-8000-000000000003/publish")
	r.Body = io.NopCloser(strings.NewReader(`{}`))
	r.Header.Set("If-Match", `"1"`)
	r.Header.Set("Idempotency-Key", "operation-1")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	var response struct {
		Data recordings.PublishResult `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusOK || response.Data.Outcome != "state_changed" || response.Data.Current.Status != "draft" || response.Data.Receipt.OperationKey != "operation-1" {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
}

func TestRecordingDescriptionRejectsInvalidWireValues(t *testing.T) {
	const id = "0199a000-0000-4000-8000-000000000003"
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(progressRecordingRepository{}, "", nil, time.Now)).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes()
	for _, description := range []string{`null`, `42`, `true`, `{}`, `"\u0000"`, `"` + strings.Repeat("😀", 5001) + `"`} {
		r := httptest.NewRequest(http.MethodPatch, "/api/admin/recordings/"+id, strings.NewReader(`{"title":"Meeting","description":`+description+`}`))
		trustedBulletinAudit(r, "cms:recordings:write")
		r.Header.Set("If-Match", `"1"`)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 400 {
			t.Fatalf("invalid description status=%d %s", w.Code, w.Body)
		}
	}
}

func TestRecordingConflictErrorCodes(t *testing.T) {
	for _, tt := range []struct {
		name string
		err  error
		code string
	}{
		{"draft quota", recordings.ErrDraftLimit, "recording_draft_limit"},
		{"wrapped draft quota", fmt.Errorf("create: %w", recordings.ErrDraftLimit), "recording_draft_limit"},
		{"recording conflict", recordings.ErrConflict, "recording_conflict"},
		{"asset conflict", assetclient.ErrConflict, "recording_conflict"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			handleRecordingError(w, tt.err)
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusConflict || body.Error.Code != tt.code {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
}

func TestRecordingAdminMetadataDoesNotChangeFrozenMemberWire(t *testing.T) {
	at := time.Now().UTC()
	value := recordings.Recording{ID: "r1", Broadcast: true, BroadcastStartedAt: &at}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "isBroadcast") || strings.Contains(string(raw), "broadcastStartedAt") {
		t.Fatalf("admin metadata leaked into member/wire response: %s", raw)
	}
}
