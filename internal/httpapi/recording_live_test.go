package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/legal"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLivePlaybackRequiresMembershipAndCurrentLegalBeforeLookup(t *testing.T) {
	body := json.RawMessage(`{"schemaVersion":1,"template":"legal.v1","data":{"heroTitle":"Policy","updatedAtLabel":"Updated","updatedAt":"2026年9月30日","intro":"Notice","sections":[{"title":"Purpose","body":["Policy"]}]}}`)
	translations := map[string]legal.Documents{}
	for _, language := range legal.Locales {
		translations[language] = legal.Documents{Terms: body, Privacy: body}
	}
	draft := legal.Draft{Scope: legal.Common, TermsVersion: "t", PrivacyNoticeVersion: "p", Translations: translations}
	commons, err := legal.BuildSnapshots(draft)
	if err != nil {
		t.Fatal(err)
	}
	draft.Scope = legal.VerifiedMember
	draft.TermsVersion = "mt"
	draft.PrivacyNoticeVersion = "mp"
	draft.CommonTermsVersion = "t"
	draft.CommonPrivacyNoticeVersion = "p"
	members, err := legal.BuildSnapshots(draft)
	if err != nil {
		t.Fatal(err)
	}
	common, member := commons[0], members[0]
	for _, tc := range []struct {
		name                string
		qualified, accepted bool
		err                 error
		want                int
		code                string
	}{{"missing acceptance", true, false, nil, 428, "policy_acceptance_required"}, {"revoked", false, true, nil, 403, "access_required"}, {"dependency unavailable", true, false, errors.New("down"), 503, "authorization_unavailable"}} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(nil, "", nil, time.Now)).WithBulletinEntitlements(entitlementCheckerStub{decision: "allow"}).WithLegal(legal.NewService(legalGateRepo{common: common, member: member}), qualificationStub{allowed: tc.qualified}).WithMemberLegalEnforcement(true, legalCheckStub{tc.accepted, tc.err}).Routes()
			for _, operation := range []string{"GET", "POST", "cover"} {
				method := operation
				if operation == "cover" {
					method = "GET"
				}
				path := "/api/member/recordings/live"
				if method == "POST" {
					path = "/api/member/recordings/00000000-0000-4000-8000-000000000001/live/playback"
				}
				if operation == "cover" {
					path = "/api/member/recordings/00000000-0000-4000-8000-000000000001/live/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/cover"
				}
				req := httptest.NewRequest(method, path, strings.NewReader(`{}`))
				trusted(req, "")
				w := httptest.NewRecorder()
				h.ServeHTTP(w, req)
				if !strings.Contains(w.Body.String(), `"code":"`+tc.code+`"`) || w.Code != tc.want || w.Header().Get("Cache-Control") != "private, no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
					t.Fatalf("%s %d %s", method, w.Code, w.Body)
				}
			}
		})
	}
}
func TestLiveRequestShapeRejectsUnknownQueryBodyAndInvalidIdentity(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(nil, "", nil, time.Now)).WithBulletinEntitlements(entitlementCheckerStub{decision: "allow"}).Routes()
	for _, tc := range []struct{ method, path, body string }{{"GET", "/api/member/recordings/live?ignored=1", ""}, {"POST", "/api/member/recordings/id/live/playback?ignored=1", `{}`}, {"POST", "/api/member/recordings/id/live/playback", `{"captureId":"x","playbackScopeId":"x","userId":"spoof"}`}, {"POST", "/api/member/recordings/id/live/playback", `{"captureId":"x","playbackScopeId":"x"}`}} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		trusted(req, "")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != 400 || !strings.Contains(w.Body.String(), "capture_invalid") {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body)
		}
	}
}

type emptyLiveSearchRepo struct {
	recordings.Repository
	recordings.LiveRepository
}

func (emptyLiveSearchRepo) ListLiveCaptures(context.Context, time.Time) ([]recordings.Capture, error) {
	return nil, nil
}
func TestLiveSearchAllowsOnlyOneBoundedQuery(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(emptyLiveSearchRepo{}, "", nil, time.Now))
	for _, tc := range []struct {
		q    string
		want int
	}{
		{"q=Faith+%E4%B8%BB%E6%97%A5", 200}, {"q=", 200}, {"", 200},
		{"q=a&q=b", 400}, {"q=" + strings.Repeat("a", 101), 400}, {"limit=12", 400}, {"q=%00", 400}, {"q=%ZZ", 400},
	} {
		w := httptest.NewRecorder()
		h.memberListLiveRecordings(w, httptest.NewRequest("GET", "/api/member/recordings/live?"+tc.q, nil))
		if w.Code != tc.want {
			t.Fatalf("%q got=%d body=%s", tc.q, w.Code, w.Body.String())
		}
	}
}

func TestLiveGrantCapacityUsesRetryableUnavailableResponse(t *testing.T) {
	response := httptest.NewRecorder()
	liveError(response, recordings.ErrTransactionCapacity)
	if response.Code != 503 || response.Header().Get("Retry-After") != "5" || !strings.Contains(response.Body.String(), "capture_unavailable") {
		t.Fatalf("capacity response: %d %v %s", response.Code, response.Header(), response.Body.String())
	}
}

func TestRecordingTransactionCapacityIsRetryableAcrossRoutes(t *testing.T) {
	for _, write := range []func(http.ResponseWriter, error){handleRecordingError, captureError, broadcastError, liveError} {
		response := httptest.NewRecorder()
		write(response, recordings.ErrTransactionCapacity)
		if response.Code != 503 || response.Header().Get("Retry-After") != "5" {
			t.Fatalf("capacity not retryable: %d %v", response.Code, response.Header())
		}
	}
}
