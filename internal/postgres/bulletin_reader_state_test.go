package postgres

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinreader"
	"github.com/google/uuid"
)

func TestPrivateReaderStateRoundTripAndRollback(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, source, revision := seedReaderPublication(t, db, now)
	owner, other := uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM hhc_web.bulletin_reader_migration_state WHERE document_id=$1`, source.DocumentID); err != nil {
			t.Error(err)
		}
	})
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	state, err := loadReaderState(ctx, tx, owner, source.DocumentID, revision, now)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := bulletinreader.Snapshot{DocumentID: source.DocumentID, Revision: revision, Pages: []string{"page"}, Components: []string{"body"}, Sentences: []bulletinreader.Anchor{{ID: "s", Text: "經文。", PageID: "page", ComponentID: "body"}}}
	operations := []struct{ kind, payload string }{{"setHighlight", `{"sentenceIds":["s"],"color":"yellow"}`}, {"createNote", `{"noteId":"56ac2c7b-15d9-4396-bb70-86f41bc01a0a","sentenceIds":["s"],"text":"我的筆記"}`}, {"setProgress", `{"pageId":"page","componentId":"body","sentenceId":"s"}`}}
	for _, op := range operations {
		state, _, err = bulletinreader.Apply(owner, state, snapshot, bulletinreader.Mutation{MutationID: uuid.NewString(), CreatedAt: now, DocumentRevision: revision, Kind: op.kind, Payload: json.RawMessage(op.payload)}, now)
		if err != nil {
			t.Fatal(err)
		}
	}
	historical := state.Highlights[0]
	historical.Active = false
	state.HighlightHistory = []bulletinreader.HighlightHistoryEntry{{ID: uuid.NewString(), Highlight: &historical}, {ID: uuid.NewString()}}
	if err = saveReaderState(ctx, tx, state, now); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	loaded, err := loadReaderState(ctx, tx, owner, source.DocumentID, revision, now)
	if err != nil || !reflect.DeepEqual(state, loaded) {
		t.Fatalf("round trip mismatch: %v\n%#v\n%#v", err, state, loaded)
	}
	isolated, err := loadReaderState(ctx, tx, other, source.DocumentID, revision, now)
	if err != nil || len(isolated.Notes) != 0 || len(isolated.Highlights) != 0 || len(isolated.HighlightHistory) != 0 || isolated.Progress != nil {
		t.Fatal("account leak", err)
	}
	loaded.Highlights = []bulletinreader.Highlight{}
	if err = saveReaderState(ctx, tx, loaded, now); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	loaded, err = loadReaderState(ctx, tx, owner, source.DocumentID, revision, now)
	if err != nil || len(loaded.Highlights) != 1 || len(loaded.Notes) != 1 {
		t.Fatal("rollback lost private state", err)
	}
	// A migration conflict retains the original source/version as inactive
	// evidence. Persist that state change even when its user version is unchanged.
	loaded.Highlights[0].Active = false
	if err = saveReaderState(ctx, tx, loaded, now); err != nil {
		t.Fatal(err)
	}
	loaded, err = loadReaderState(ctx, tx, owner, source.DocumentID, revision, now)
	if err != nil || loaded.Highlights[0].Active {
		t.Fatal("migration evidence stayed active", err)
	}
	loaded.Highlights = []bulletinreader.Highlight{}
	if err = saveReaderState(ctx, tx, loaded, now); err != nil {
		t.Fatal(err)
	}
	loaded, err = loadReaderState(ctx, tx, owner, source.DocumentID, revision, now)
	if err != nil || len(loaded.Highlights) != 0 || len(loaded.Notes) != 1 {
		t.Fatal("clear damaged notes", err)
	}
}

func TestPrivateReaderCurrentRevisionArchivesLegacyInactiveHighlights(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	repo, source, revision := seedReaderPublication(t, db, now)
	owner := uuid.NewString()
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_reader_migration_state WHERE document_id=$1`, source.DocumentID)
	})
	if _, err := repo.ReaderState(ctx, source.Canonical.IssueID, "general", "zh-Hant", owner, nil, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO hhc_web.bulletin_reader_highlight(account_id,document_id,sentence_id,color,quote,active,version,updated_at) VALUES($1,$2,'removed','yellow','retained old quotation',false,1,$3)`, owner, source.DocumentID, now); err != nil {
		t.Fatal(err)
	}
	read := func() bulletinreader.State {
		t.Helper()
		response, err := repo.ReaderState(ctx, source.Canonical.IssueID, "general", "zh-Hant", owner, nil, now)
		if err != nil {
			t.Fatal(err)
		}
		return response.State
	}
	state := read()
	if len(state.HighlightHistory) != 1 || state.HighlightHistory[0].Highlight == nil || state.HighlightHistory[0].Highlight.Quote != "retained old quotation" {
		t.Fatal("existing inactive source missing from history", state.HighlightHistory)
	}
	if !reflect.DeepEqual(state, read()) {
		t.Fatal("history duplicated on repeated read")
	}
	body, _ := json.Marshal(map[string]string{"historyId": state.HighlightHistory[0].ID})
	mutation := bulletinreader.Mutation{MutationID: uuid.NewString(), CreatedAt: now, DocumentRevision: revision, Kind: "discardHighlightHistory", Payload: body}
	response, err := repo.MutateReader(ctx, source.Canonical.IssueID, "general", "zh-Hant", owner, []bulletinreader.Mutation{mutation}, now)
	if err != nil || response.Results[0].Status != "applied" {
		t.Fatal("discard failed", err)
	}
	state = read()
	if len(state.HighlightHistory) != 1 || state.HighlightHistory[0].Highlight != nil || len(state.Highlights) != 0 {
		t.Fatal("discarded history resurrected")
	}
}
