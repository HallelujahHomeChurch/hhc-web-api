package postgres

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinreader"
	"github.com/google/uuid"
	"reflect"
	"testing"
	"time"
)

func TestReaderRangePersistence(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, source, revision := seedReaderPublication(t, db, now)
	owner := uuid.NewString()
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_reader_migration_state WHERE document_id=$1`, source.DocumentID)
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
	state.Highlights = []bulletinreader.Highlight{{SentenceID: "a", Color: "yellow", Quote: "甲😀乙", Active: true, Version: 1, UpdatedAt: now, Segments: []bulletinreader.HighlightSegment{{Start: 1, End: 2, Color: "yellow"}}}}
	state.Notes = []bulletinreader.Note{{ID: uuid.NewString(), Text: "private", SentenceIDs: []string{"a"}, InactiveAnchors: []bulletinreader.Anchor{}, Quote: "😀", Version: 1, CreatedAt: now, UpdatedAt: now, Ranges: []bulletinreader.NoteRange{{TextRange: bulletinreader.TextRange{SentenceID: "a", Start: 1, End: 2}, Quote: "甲😀乙"}}}}
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
	restored, err := loadReaderState(ctx, tx, owner, source.DocumentID, revision, now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.Highlights, state.Highlights) || !reflect.DeepEqual(restored.Notes, state.Notes) {
		t.Fatal("range data lost on reload", restored)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}
