package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestBulletinTemplateSettingsReadsFailClosedWhenAuditUnavailable(t *testing.T) {
	for _, suffix := range []string{"", "/revisions"} {
		repo := &templateSettingsRepository{value: onlinebulletins.TemplateSettings{Version: 1, TemplateText: onlinebulletins.TemplateText{VisionMission: "private-copy"}}}
		r := httptest.NewRequest("GET", "/api/admin/bulletins/template-settings"+suffix, nil)
		trustedBulletinAudit(r, "cms:bulletins:read")
		w := httptest.NewRecorder()
		NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, nil, "api-gateway", "", true).WithAuditOutbox(&bulletinAuditOutbox{err: errors.New("unavailable")}, time.Now).Routes().ServeHTTP(w, r)
		if w.Code != 503 || w.Header().Get("ETag") != "" || strings.Contains(w.Body.String(), "private-copy") {
			t.Fatal("private settings returned without durable read audit", w.Code, w.Body.String())
		}
	}
}

type templateSettingsRepository struct {
	apiRepository
	calls int
	value onlinebulletins.TemplateSettings
}

func (r *templateSettingsRepository) BulletinTemplateSettings(context.Context) (onlinebulletins.TemplateSettings, error) {
	r.calls++
	return r.value, nil
}
func (r *templateSettingsRepository) SaveBulletinTemplateSettings(_ context.Context, input onlinebulletins.TemplateText, expected int64, _ string, _ time.Time) (onlinebulletins.TemplateSettings, error) {
	r.calls++
	if expected != r.value.Version {
		return r.value, bulletins.ErrPrecondition
	}
	r.value.Version++
	r.value.TemplateText = input
	return r.value, nil
}
func (r *templateSettingsRepository) BulletinTemplateSettingsHistory(context.Context) ([]onlinebulletins.TemplateSettingsRevision, error) {
	r.calls++
	return []onlinebulletins.TemplateSettingsRevision{}, nil
}

func TestBulletinTemplateSettingsHTTPContract(t *testing.T) {
	body := `{"visionMission":"遍地華人興起、福音傳到地極","visionFellowship":"共同生活、愛與成全、恩膏傳承","visionCommitment":"宣教主導、靈恩神學、團隊事奉、門徒訓練"}`
	for _, tc := range []struct {
		name, method, suffix, scope, body, etag string
		want                                    int
	}{
		{"read", "GET", "", "read", "", "", 200},
		{"history", "GET", "/revisions", "read", "", "", 200},
		{"save", "PUT", "", "write", body, `"1"`, 410},
		{"read-scope", "GET", "", "write", "", "", 403},
		{"write-scope", "PUT", "", "read", body, `"1"`, 403},
		{"cas-required", "PUT", "", "write", body, "", 410},
		{"stale", "PUT", "", "write", body, `"2"`, 410},
		{"unknown", "PUT", "", "write", strings.TrimSuffix(body, "}") + `,"version":99}`, `"1"`, 410},
		{"empty", "PUT", "", "write", `{}`, `"1"`, 410},
		{"multiple", "PUT", "", "write", body + body, `"1"`, 410},
		{"invalid-utf8", "PUT", "", "write", strings.Replace(body, "遍地", string([]byte{0xff}), 1), `"1"`, 410},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &templateSettingsRepository{value: onlinebulletins.TemplateSettings{Version: 1}}
			r := httptest.NewRequest(tc.method, "/api/admin/bulletins/template-settings"+tc.suffix, strings.NewReader(tc.body))
			trustedBulletinAudit(r, "cms:bulletins:"+tc.scope)
			if tc.etag != "" {
				r.Header.Set("If-Match", tc.etag)
			}
			w := httptest.NewRecorder()
			NewWithContent(bulletins.NewService(repo, time.Now), nil, nil, nil, "api-gateway", "", true).WithAuditOutbox(&bulletinAuditOutbox{}, time.Now).Routes().ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("got%d want%d %s", w.Code, tc.want, w.Body.String())
			}
			if tc.want == 200 && w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("settings cacheable")
			}
			if tc.method == "PUT" && repo.calls != 0 {
				t.Fatal("retired endpoint reached repository")
			}
			if tc.want == 403 && repo.calls != 0 {
				t.Fatal("unauthorized repository access")
			}
			if tc.name == "invalid-utf8" && repo.calls != 0 {
				t.Fatal("corrupted input reached settings save")
			}
		})
	}
	for _, method := range []string{"GET", "PUT"} {
		h := NewWithContent(bulletins.NewService(&templateSettingsRepository{}, time.Now), nil, nil, nil, "api-gateway", "", true)
		w := httptest.NewRecorder()
		h.Routes().ServeHTTP(w, serviceRequest(method, "/api/admin/bulletins/template-settings"))
		if w.Code != 403 {
			t.Fatal("service actor admitted", w.Code)
		}
	}
}
