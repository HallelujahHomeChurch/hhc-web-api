package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/inboxcontent"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/operationsclient"
	"github.com/google/uuid"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInboxVisibilityExactCallerDisabled(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "inbox-test-app-token", false)
	r := httptest.NewRequest("POST", "/priv/website/inbox-visibility", strings.NewReader(`{}`))
	r.Header.Set("Dapr-Caller-App-Id", "notification-api")
	r.Header.Set("dapr-api-token", "inbox-test-app-token")
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatalf("disabled inbox=%d", w.Code)
	}
}

func TestInboxVisibilityRejectsRetiredOrMissingDaprTransport(t *testing.T) {
	for _, test := range []struct {
		disabled bool
		token    string
	}{{true, "retained-token"}, {false, ""}} {
		h := NewWithContent(nil, nil, nil, nil, "api-gateway", "inbox-test-app-token", false)
		h.WithInboxVisibility(true, &inboxVisibilityRepoStub{}, &inboxAssetsStub{}).WithBulletinEntitlements(&inboxGrantsStub{})
		q := &inboxQualifierStub{qualified: true}
		h.memberQualification = q
		h.daprDisabled, h.daprAPIToken = test.disabled, test.token
		r := httptest.NewRequest("POST", "/priv/website/inbox-visibility", strings.NewReader(`{"actorId":"11111111-1111-4111-8111-111111111111","locale":"en","items":[{"id":"22222222-2222-4222-8222-222222222222","resource":{"type":"recording","id":"recording-a"}}]}`))
		r.Header.Set("Dapr-Caller-App-Id", "notification-api")
		r.Header.Set("dapr-api-token", "inbox-test-app-token")
		r.Header.Set("dapr-api-token", test.token)
		w := httptest.NewRecorder()
		h.inboxVisibility(w, r)
		if w.Code != 503 || q.calls != 0 || !strings.Contains(w.Body.String(), "inbox_transport_unavailable") {
			t.Fatalf("unreviewed transport admitted: status=%d body=%s", w.Code, w.Body.String())
		}
	}
}

type inboxVisibilityRepoStub struct {
	items   []inboxcontent.Bulletin
	err     error
	calls   int
	blocked bool
}

func (s *inboxVisibilityRepoStub) InboxSubjectAllowed(context.Context, string) (bool, error) {
	return !s.blocked, s.err
}
func (s *inboxVisibilityRepoStub) InboxBulletinEntitlements(context.Context) ([]inboxcontent.Entitlement, error) {
	values := []inboxcontent.Entitlement{}
	for _, e := range bulletins.BulletinEditionOrder {
		series, locale := string(e.Series), string(e.Locale)
		values = append(values, inboxcontent.Entitlement{Series: series, Locale: locale, Code: "bulletin." + series + "." + locale + ".access"})
	}
	return values, s.err
}
func (s *inboxVisibilityRepoStub) InboxBulletins(context.Context, []inboxcontent.Resource) ([]inboxcontent.Bulletin, error) {
	s.calls++
	return s.items, s.err
}

type inboxQualifierStub struct {
	calls     int
	qualified bool
	err       error
}

func (s *inboxQualifierStub) CheckMemberQualification(_ context.Context, id string) (operationsclient.MembershipQualification, error) {
	s.calls++
	return operationsclient.MembershipQualification{SubjectID: id, Qualified: s.qualified}, s.err
}

type inboxGrantsStub struct {
	calls int
	codes map[string]bool
	err   error
}

func (s *inboxGrantsStub) CheckEntitlements(_ context.Context, _ string, codes []string) ([]operationsclient.EntitlementDecision, error) {
	s.calls++
	items := []operationsclient.EntitlementDecision{}
	for _, code := range codes {
		decision := "deny"
		if s.codes[code] {
			decision = "allow"
		}
		items = append(items, operationsclient.EntitlementDecision{EntitlementCode: code, Decision: decision})
	}
	return items, s.err
}

type inboxAssetsStub struct {
	calls  int
	ready  bool
	err    error
	refs   []assetclient.WebsiteContentRef
	onRead func()
}

func (s *inboxAssetsStub) WebsiteContentReadiness(_ context.Context, refs []assetclient.WebsiteContentRef) ([]assetclient.WebsiteContentDecision, error) {
	s.calls++
	if s.onRead != nil {
		s.onRead()
	}
	s.refs = append(s.refs, refs...)
	items := []assetclient.WebsiteContentDecision{}
	for _, ref := range refs {
		items = append(items, assetclient.WebsiteContentDecision{Ref: ref, Ready: s.ready})
	}
	return items, s.err
}
func TestInboxVisibilityUsesCurrentExactEditionAndBoundedQualification(t *testing.T) {
	actor, row1, row2 := uuid.NewString(), uuid.NewString(), uuid.NewString()
	issue := "issue-a"
	number := 1800
	item := func(series, locale, title string, online bool) inboxcontent.Bulletin {
		return inboxcontent.Bulletin{ProtectedBulletin: bulletins.ProtectedBulletin{IssueID: issue, Series: series, Locale: locale, Title: title, IssueNumber: &number, ProtectedAssetID: series + "-" + locale}, Online: online}
	}
	for _, tc := range []struct {
		name, locale, wantLocale, wantTitle, series string
		allow                                       map[string]bool
		ready, qualified, blocked, legal            bool
		dependencyErr                               bool
		want                                        int
	}{
		{name: "requested authorized edition", locale: "en", wantLocale: "en", wantTitle: "English", allow: map[string]bool{"bulletin.general.en.access": true, "bulletin.general.zh-Hant.access": true}, ready: true, qualified: true, want: 200},
		{name: "denied requested falls back", locale: "en", wantLocale: "zh-Hant", wantTitle: "Traditional", allow: map[string]bool{"bulletin.general.zh-Hant.access": true}, ready: true, qualified: true, want: 200},
		{name: "Japanese fallback", locale: "ja", wantLocale: "zh-Hant", wantTitle: "Traditional", allow: map[string]bool{"bulletin.general.zh-Hant.access": true}, ready: true, qualified: true, want: 200},
		{name: "children online exact edition", locale: "en", series: "children", wantLocale: "en", wantTitle: "Children", allow: map[string]bool{"bulletin.children.en.access": true}, qualified: true, want: 200},
		{name: "current entitlement denied", locale: "en", allow: map[string]bool{}, ready: true, qualified: true, want: 200},
		{name: "only other series allowed", locale: "en", allow: map[string]bool{"bulletin.children.en.access": true}, ready: true, qualified: true, want: 200},
		{name: "unready media", locale: "en", allow: map[string]bool{"bulletin.general.en.access": true}, qualified: true, want: 200},
		{name: "unqualified", locale: "en", allow: map[string]bool{"bulletin.general.en.access": true}, ready: true, want: 200},
		{name: "cleanup blocked", locale: "en", allow: map[string]bool{"bulletin.general.en.access": true}, ready: true, qualified: true, blocked: true, want: 200},
		{name: "legal dependency missing", locale: "en", allow: map[string]bool{"bulletin.general.en.access": true}, ready: true, qualified: true, legal: true, want: 503},
		{name: "asset dependency unavailable", locale: "en", allow: map[string]bool{"bulletin.general.en.access": true}, qualified: true, dependencyErr: true, want: 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &inboxVisibilityRepoStub{items: []inboxcontent.Bulletin{item("general", "en", "English", false), item("general", "zh-Hant", "Traditional", false), item("children", "en", "Children", true)}, blocked: tc.blocked}
			q := &inboxQualifierStub{qualified: tc.qualified}
			grants := &inboxGrantsStub{codes: tc.allow}
			assets := &inboxAssetsStub{ready: tc.ready}
			if tc.dependencyErr {
				assets.err = errors.New("offline")
			}
			h := NewWithContent(nil, nil, nil, nil, "api-gateway", "inbox-test-app-token", false).WithInboxVisibility(true, repo, assets).WithBulletinEntitlements(grants)
			h.memberQualification = q
			h.memberLegalEnforcement = tc.legal
			series := tc.series
			if series == "" {
				series = "general"
			}
			input := inboxcontent.Request{ActorID: actor, Locale: tc.locale, Items: []inboxcontent.Ref{{ID: row1, Resource: inboxcontent.Resource{Type: "bulletin", ID: issue, Series: series}}, {ID: row2, Resource: inboxcontent.Resource{Type: "bulletin", ID: issue, Series: series}}}}
			raw, _ := json.Marshal(input)
			r := httptest.NewRequest("POST", "/priv/website/inbox-visibility", bytes.NewReader(raw))
			r.Header.Set("Dapr-Caller-App-Id", "notification-api")
			r.Header.Set("dapr-api-token", "inbox-test-app-token")
			w := httptest.NewRecorder()
			h.Routes().ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got %d %s", w.Code, w.Body.String())
			}
			if q.calls != 1 || grants.calls > 1 || assets.calls > 1 || repo.calls > 2 {
				t.Fatalf("N+1 calls: qualification=%d grants=%d assets=%d data=%d", q.calls, grants.calls, assets.calls, repo.calls)
			}
			if tc.want != 200 {
				return
			}
			var body struct {
				Data struct {
					Items []inboxcontent.Decision `json:"items"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Data.Items) != 2 {
				t.Fatal("decisions lost")
			}
			for i, d := range body.Data.Items {
				if d.ID != input.Items[i].ID || d.Visible != (tc.wantLocale != "") {
					t.Fatalf("%+v", d)
				}
				if d.Visible {
					p := d.Projection
					if p == nil || p.Parameters["title"] != tc.wantTitle || p.Parameters["issueNumber"] != "1800" || p.Action.Locale != tc.wantLocale || p.Action.Resource != input.Items[i].Resource {
						t.Fatalf("unsafe edition %+v", p)
					}
				} else if d.Projection != nil {
					t.Fatal("denial leaked projection")
				}
			}
		})
	}
}
func TestInboxVisibilityRejectsWrongAndDuplicateCallerAndClosedInput(t *testing.T) {
	actor, row := uuid.NewString(), uuid.NewString()
	valid := fmt.Sprintf(`{"actorId":%q,"locale":"en","items":[{"id":%q,"resource":{"type":"recording","id":"rec"}}]}`, actor, row)
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "inbox-test-app-token", false).WithInboxVisibility(true, &inboxVisibilityRepoStub{}, &inboxAssetsStub{}).WithBulletinEntitlements(&inboxGrantsStub{})
	h.memberQualification = &inboxQualifierStub{qualified: true}
	for _, tc := range []struct {
		caller, body string
		duplicate    bool
		want         int
	}{{"account-api", valid, false, 401}, {"", valid, false, 401}, {"notification-api", valid, true, 401}, {"notification-api", strings.Replace(valid, `"rec"`, `"rec","unitId":"bad"`, 1), false, 400}, {"notification-api", strings.Replace(valid, `"locale":"en"`, `"locale":"bad"`, 1), false, 422}} {
		r := httptest.NewRequest("POST", "/priv/website/inbox-visibility", strings.NewReader(tc.body))
		r.Header.Set("Dapr-Caller-App-Id", tc.caller)
		r.Header.Set("dapr-api-token", "inbox-test-app-token")
		if tc.duplicate {
			r.Header.Add("Dapr-Caller-App-Id", tc.caller)
		}
		r.Header.Set("X-Internal-Caller-App-Id", "notification-api")
		w := httptest.NewRecorder()
		h.Routes().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("got %d want %d body %s", w.Code, tc.want, w.Body.String())
		}
	}
}

func TestInboxBulletinRechecksCurrentPublicationAfterReadiness(t *testing.T) {
	number := 1800
	edition := func(id, series, locale, asset, title string, online bool) inboxcontent.Bulletin {
		return inboxcontent.Bulletin{ProtectedBulletin: bulletins.ProtectedBulletin{IssueID: id, Series: series, Locale: locale, ProtectedAssetID: asset, Title: title, IssueNumber: &number}, Online: online}
	}
	pdf := edition("pdf-issue", "general", "en", "original-asset", "Original PDF", false)
	online := edition("online-issue", "children", "en", "", "Original Online", true)
	for _, tc := range []struct {
		name                string
		current             []inboxcontent.Bulletin
		wantPDF, wantOnline string
		err                 bool
		sharedIssue         bool
	}{
		{name: "PDF withdrawn", current: []inboxcontent.Bulletin{online}, wantOnline: "Original Online"},
		{name: "PDF binding replaced", current: []inboxcontent.Bulletin{edition("pdf-issue", "general", "en", "replacement", "Replacement PDF", false), online}, wantOnline: "Original Online"},
		{name: "PDF title changed on same ready binding", current: []inboxcontent.Bulletin{edition("pdf-issue", "general", "en", "original-asset", "Current PDF", false), online}, wantPDF: "Current PDF", wantOnline: "Original Online"},
		{name: "PDF series changed", current: []inboxcontent.Bulletin{edition("pdf-issue", "children", "en", "original-asset", "Other series", false), online}, wantOnline: "Original Online"},
		{name: "PDF locale changed", current: []inboxcontent.Bulletin{edition("pdf-issue", "general", "zh-Hant", "original-asset", "Other locale", false), online}, wantOnline: "Original Online"},
		{name: "Online withdrawn in mixed batch", current: []inboxcontent.Bulletin{pdf}, wantPDF: "Original PDF"},
		{name: "Online changed in mixed batch", current: []inboxcontent.Bulletin{pdf, edition("online-issue", "children", "en", "", "Current Online", true)}, wantPDF: "Original PDF", wantOnline: "Current Online"},
		{name: "Online became unchecked PDF", current: []inboxcontent.Bulletin{pdf, edition("online-issue", "children", "en", "new-pdf", "Unchecked PDF", false)}, wantPDF: "Original PDF"},
		{name: "readiness cannot cross series", sharedIssue: true, current: []inboxcontent.Bulletin{pdf, edition("pdf-issue", "children", "en", "original-asset", "New child PDF", false)}, wantPDF: "Original PDF"},
		{name: "current query unavailable", err: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			currentOnline := online
			if tc.sharedIssue {
				currentOnline.IssueID = "pdf-issue"
			}
			repo := &inboxVisibilityRepoStub{items: []inboxcontent.Bulletin{pdf, currentOnline}}
			assets := &inboxAssetsStub{ready: true, onRead: func() {
				repo.items = tc.current
				if tc.err {
					repo.err = errors.New("current publication unavailable")
				}
			}}
			q := &inboxQualifierStub{qualified: true}
			grants := &inboxGrantsStub{codes: map[string]bool{"bulletin.general.en.access": true, "bulletin.general.zh-Hant.access": true, "bulletin.children.en.access": true}}
			h := NewWithContent(nil, nil, nil, nil, "api-gateway", "inbox-test-app-token", false).WithInboxVisibility(true, repo, assets).WithBulletinEntitlements(grants)
			h.memberQualification = q
			input := inboxcontent.Request{ActorID: uuid.NewString(), Locale: "en", Items: []inboxcontent.Ref{{ID: uuid.NewString(), Resource: inboxcontent.Resource{Type: "bulletin", ID: "pdf-issue", Series: "general"}}, {ID: uuid.NewString(), Resource: inboxcontent.Resource{Type: "bulletin", ID: "online-issue", Series: "children"}}}}
			if tc.sharedIssue {
				input.Items[1].Resource.ID = "pdf-issue"
			}
			raw, _ := json.Marshal(input)
			r := httptest.NewRequest("POST", "/priv/website/inbox-visibility", bytes.NewReader(raw))
			r.Header.Set("Dapr-Caller-App-Id", "notification-api")
			r.Header.Set("dapr-api-token", "inbox-test-app-token")
			w := httptest.NewRecorder()
			h.Routes().ServeHTTP(w, r)
			if tc.err {
				if w.Code != 503 {
					t.Fatalf("current query failure exposed stale content: %d %s", w.Code, w.Body.String())
				}
				return
			}
			if w.Code != 200 {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
			var body struct {
				Data struct {
					Items []inboxcontent.Decision `json:"items"`
				} `json:"data"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Data.Items) != 2 {
				t.Fatal("missing decisions")
			}
			for i, want := range []string{tc.wantPDF, tc.wantOnline} {
				d := body.Data.Items[i]
				if d.Visible != (want != "") {
					t.Fatalf("stale decision %d: %+v", i, d)
				}
				if want == "" {
					if d.Projection != nil {
						t.Fatal("denied projection leaked")
					}
				} else if d.Projection == nil || d.Projection.Parameters["title"] != want || d.Projection.Action.Resource != input.Items[i].Resource || d.Projection.Action.Locale != "en" {
					t.Fatalf("stale projection %d: %+v", i, d.Projection)
				}
			}
			if assets.calls != 1 || repo.calls != 2 || q.calls != 1 || grants.calls != 1 {
				t.Fatalf("unbounded or absent recheck: assets=%d publication=%d qualification=%d entitlements=%d", assets.calls, repo.calls, q.calls, grants.calls)
			}
		})
	}
}

func TestInboxSelectedNotificationEnvironment(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "inbox-test-app-token", false).WithInboxVisibility(true, nil, nil, "notification-api-test")
	for _, tc := range []struct {
		caller string
		want   int
	}{{"notification-api-test", 503}, {"notification-api", 401}, {"notification-api-test-other", 401}, {"notification-api-test,notification-api", 401}, {" notification-api-test", 401}} {
		r := httptest.NewRequest("POST", "/priv/website/inbox-visibility", strings.NewReader(`{}`))
		r.Header.Set("Dapr-Caller-App-Id", tc.caller)
		r.Header.Set("dapr-api-token", "inbox-test-app-token")
		w := httptest.NewRecorder()
		h.Routes().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("caller %q: %d want %d", tc.caller, w.Code, tc.want)
		}
	}
	for _, header := range []string{"Dapr-Caller-App-Id", "dapr-api-token"} {
		r := httptest.NewRequest("POST", "/priv/website/inbox-visibility", strings.NewReader(`{}`))
		r.Header.Set("Dapr-Caller-App-Id", "notification-api-test")
		r.Header.Set("dapr-api-token", "inbox-test-app-token")
		r.Header.Set("dapr-api-token", "")
		r.Header.Add(header, "second")
		w := httptest.NewRecorder()
		h.Routes().ServeHTTP(w, r)
		if w.Code < 400 || w.Code >= 500 {
			t.Fatalf("multiple %s reached domain: %d", header, w.Code)
		}
	}

}
