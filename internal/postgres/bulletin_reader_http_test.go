package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinreader"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/httpapi"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/operationsclient"
	"github.com/google/uuid"
)

type readerHTTPGrant struct{ revoked atomic.Bool }

func (g *readerHTTPGrant) CheckEntitlements(_ context.Context, _ string, codes []string) ([]operationsclient.EntitlementDecision, error) {
	decisions := make([]operationsclient.EntitlementDecision, len(codes))
	for i, code := range codes {
		decision := "deny"
		if !g.revoked.Load() && code == "bulletin.general.zh-Hant.access" {
			decision = "allow"
		}
		decisions[i] = operationsclient.EntitlementDecision{EntitlementCode: code, Decision: decision}
	}
	return decisions, nil
}

// Exercise the real HTTP/service/Postgres boundary; only the external grant authority is controlled.
func TestReaderHTTPPersistenceReplayAndRevocation(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	repo, source, revision := seedReaderPublication(t, db, now)
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM hhc_web.bulletin_reader_migration_state WHERE document_id=$1`, source.DocumentID); err != nil {
			t.Error(err)
		}
	})
	var raw []byte
	if err := db.QueryRow(`SELECT document_json FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=$2`, source.DocumentID, revision).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var document onlinebulletins.Document
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	sentence := document.Components[0].Cover.Welcome[0].Sentences[0].ID
	page, offset := "", 0
	for _, p := range document.LayoutManifest.Pages {
		for _, slot := range p.Slots {
			for _, fragment := range slot.Fragments {
				if fragment.SentenceID == sentence {
					page, offset = p.PageID, fragment.Start
				}
			}
		}
	}
	if page == "" {
		t.Fatal("missing source fragment")
	}
	owner, other := uuid.NewString(), uuid.NewString()
	grant := &readerHTTPGrant{}
	handler := httpapi.NewWithContent(bulletins.NewService(repo, func() time.Time { return now }), nil, db, nil, "api-gateway", "", true).
		WithBulletinEntitlements(grant).Routes()
	server := httptest.NewServer(handler)
	defer server.Close()
	base := server.URL + "/api/member/bulletins/" + source.Canonical.IssueID + "/versions/zh-Hant/online/reader"
	request := func(method, path, account string, body []byte, status int) []byte {
		t.Helper()
		req, err := http.NewRequest(method, base+path+"?series=general", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Dapr-Caller-App-Id", "api-gateway")
		req.Header.Set("X-HHC-User-ID", account)
		req.Header.Set("X-HHC-Auth-Provider", "account-api")
		req.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		result, err := io.ReadAll(response.Body)
		if err != nil || response.StatusCode != status || response.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("%s %s: status=%d body=%s error=%v", method, path, response.StatusCode, result, err)
		}
		return result
	}
	payload := func(value any) json.RawMessage {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	ops := []bulletinreader.Mutation{
		{MutationID: uuid.NewString(), CreatedAt: now, DocumentRevision: revision, Kind: "createBookmark", Payload: payload(map[string]any{"bookmarkId": uuid.NewString(), "name": "HTTP bookmark", "sentenceId": sentence, "offset": offset, "pageId": page})},
		{MutationID: uuid.NewString(), CreatedAt: now, DocumentRevision: revision, Kind: "setHighlight", Payload: payload(map[string]any{"sentenceIds": []string{sentence}, "color": "yellow"})},
		{MutationID: uuid.NewString(), CreatedAt: now, DocumentRevision: revision, Kind: "createNote", Payload: payload(map[string]any{"noteId": uuid.NewString(), "sentenceIds": []string{sentence}, "text": "離線後待同步的筆記"})},
		{MutationID: uuid.NewString(), CreatedAt: now, DocumentRevision: revision, Kind: "setProgress", Payload: payload(map[string]any{"sentenceId": sentence})},
	}
	body := payload(map[string]any{"mutations": ops})
	for range 2 {
		var response struct {
			Data bulletinreader.MutationResponse `json:"data"`
		}
		if err := json.Unmarshal(request("POST", "/mutations", owner, body, 200), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Data.Results) != len(ops) {
			t.Fatal("missing acknowledgement", response.Data.Results)
		}
		for _, result := range response.Data.Results {
			if result.Status != "applied" {
				t.Fatal("operation was not applied", result)
			}
		}
	}
	read := func(account string) bulletinreader.State {
		t.Helper()
		var response struct {
			Data bulletinreader.StateResponse `json:"data"`
		}
		if err := json.Unmarshal(request("GET", "/state", account, nil, 200), &response); err != nil {
			t.Fatal(err)
		}
		return response.Data.State
	}
	state := read(owner)
	if len(state.Bookmarks) != 1 || state.Bookmarks[0].Name != "HTTP bookmark" || state.Bookmarks[0].Version != 1 || state.OwnerID != owner || len(state.Highlights) != 1 || state.Highlights[0].Version != 1 || state.Highlights[0].Color != "yellow" || len(state.Notes) != 1 || state.Notes[0].Text != "離線後待同步的筆記" || state.Notes[0].Version != 1 || state.Progress == nil || state.Progress.SentenceID != sentence {
		t.Fatal("HTTP replay changed or lost persisted private data", state)
	}
	isolated := read(other)
	if len(isolated.Bookmarks) != 0 || isolated.OwnerID != other || len(isolated.Highlights) != 0 || len(isolated.Notes) != 0 || isolated.Progress != nil {
		t.Fatal("private state crossed accounts", isolated)
	}
	grant.revoked.Store(true)
	request("POST", "/mutations", owner, body, 404)
	request("GET", "/state", owner, nil, 404)
	grant.revoked.Store(false)
	if restored := read(owner); len(restored.Notes) != 1 || restored.Notes[0].Text != state.Notes[0].Text {
		t.Fatal("authorization denial destroyed private notes", restored)
	}
}
