package httpapi

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinreader"
)

func (r *memberOnlineRepository) ReaderState(_ context.Context, issue, series, locale, owner string, from *int64, now time.Time) (bulletinreader.StateResponse, error) {
	r.dataReads++
	r.actor, r.series, r.locale = owner, series, locale
	return bulletinreader.StateResponse{State: bulletinreader.NewState(owner, "document", 1)}, r.err
}
func (r *memberOnlineRepository) MutateReader(_ context.Context, issue, series, locale, owner string, mutations []bulletinreader.Mutation, now time.Time) (bulletinreader.MutationResponse, error) {
	r.dataReads++
	r.actor, r.series, r.locale = owner, series, locale
	return bulletinreader.MutationResponse{State: bulletinreader.NewState(owner, "document", 1), Results: []bulletinreader.Result{}}, r.err
}

const privateReaderBase = "/api/member/bulletins/b08a80e8-6388-4166-9a2a-23d336f74b7a/versions/zh-Hant/online/reader"
const privateReaderBody = `{"mutations":[{"mutationId":"94aff842-b88a-4942-8ecb-2817955fa9de","createdAt":"2026-10-02T00:00:00Z","documentRevision":1,"kind":"clearHighlight","payload":{"sentenceIds":["a"]}}]}`

func TestPrivateReaderUsesExactDownloadAuthorityBeforeLookup(t *testing.T) {
	for _, decision := range []string{"allow", "deny", "unavailable"} {
		for _, method := range []string{"GET", "POST"} {
			repo := &memberOnlineRepository{protectedAPIRepository: &protectedAPIRepository{apiRepository: &apiRepository{}}}
			path := privateReaderBase + "/state?series=general"
			if method == "POST" {
				path = privateReaderBase + "/mutations?series=general"
			}
			req := httptest.NewRequest(method, path, strings.NewReader(privateReaderBody))
			trusted(req, "")
			req.Header.Set("X-HHC-User-ID", "4a5df71f-6bdc-467c-8879-68ec251fdc26")
			out := httptest.NewRecorder()
			var failure error
			want, reads := 200, 1
			if decision == "deny" {
				want, reads = 404, 0
			}
			if decision == "unavailable" {
				want, reads = 503, 0
				failure = errors.New("dependency")
			}
			memberOnlineHandler(repo, decision, failure).ServeHTTP(out, req)
			if out.Code != want || repo.dataReads != reads || repo.entitlementReads != 1 || out.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatalf("%s %s status=%d reads=%d auth=%d", method, decision, out.Code, repo.dataReads, repo.entitlementReads)
			}
			if reads == 1 && repo.actor != "4a5df71f-6bdc-467c-8879-68ec251fdc26" {
				t.Fatal("trusted account ignored")
			}
			if reads == 1 && !strings.Contains(out.Body.String(), `"accountId":"4a5df71f-6bdc-467c-8879-68ec251fdc26"`) {
				t.Fatal("response cannot be bound to the requesting account")
			}
		}
	}
}
func TestPrivateReaderBoundsAndTypedErrors(t *testing.T) {
	for _, test := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "/state", "", 400}, {"GET", "/state?series=general&fromRevision=01", "", 400},
		{"GET", "/state?series=general&series=children", "", 400},
		{"POST", "/mutations?series=general", `{"mutations":[]}`, 400},
		{"POST", "/mutations?series=general", strings.Replace(privateReaderBody, `"mutations":`, `"accountId":"other","mutations":`, 1), 400},
		{"POST", "/mutations?series=general", `{"mutations":[` + strings.Repeat(`{},`, 100) + `{}]}`, 400},
		{"POST", "/mutations?series=general", `{"padding":"` + strings.Repeat("a", 1<<20) + `"}`, 413},
	} {
		repo := &memberOnlineRepository{protectedAPIRepository: &protectedAPIRepository{apiRepository: &apiRepository{}}}
		req := httptest.NewRequest(test.method, privateReaderBase+test.path, strings.NewReader(test.body))
		trusted(req, "")
		req.Header.Set("X-HHC-User-ID", "4a5df71f-6bdc-467c-8879-68ec251fdc26")
		out := httptest.NewRecorder()
		memberOnlineHandler(repo, "allow", nil).ServeHTTP(out, req)
		if out.Code != test.status || repo.dataReads != 0 || out.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s got=%d reads=%d", test.path, out.Code, repo.dataReads)
		}
	}
	for _, failure := range []struct {
		err    error
		code   string
		status int
	}{{bulletinreader.ErrMapping, "mapping_history_unavailable", 409}, {bulletinreader.ErrMutationReuse, "mutation_id_conflict", 409}, {bulletinreader.ErrInvalid, "invalid_request", 400}} {
		repo := &memberOnlineRepository{protectedAPIRepository: &protectedAPIRepository{apiRepository: &apiRepository{}}, err: failure.err}
		req := httptest.NewRequest("GET", privateReaderBase+"/state?series=general", nil)
		trusted(req, "")
		req.Header.Set("X-HHC-User-ID", "4a5df71f-6bdc-467c-8879-68ec251fdc26")
		out := httptest.NewRecorder()
		memberOnlineHandler(repo, "allow", nil).ServeHTTP(out, req)
		if out.Code != failure.status || !strings.Contains(out.Body.String(), failure.code) || out.Header().Get("X-HHC-Bulletin-Access") != "" {
			t.Fatal("wrong nonterminal error", out.Code)
		}
	}
}
