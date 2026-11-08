package bulletinreader

import (
	"errors"
	"reflect"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestReanchorNotePreservesIdentityTextAndOriginalQuote(t *testing.T) {
	state := NewState("account-a", "document-a", 3)
	state, _ = apply(t, state, operation("createNote", `{"noteId":"28449e78-9938-4b80-811a-dffb6e44ed9e","sentenceIds":["a"],"text":"保留筆記"}`))
	state.Notes[0].SentenceIDs = []string{}
	state.Notes[0].ReanchorRequired = true
	state.Notes[0].InactiveAnchors = []Anchor{{ID: "a", Text: "第一句。"}}
	original := state.Notes[0]
	mutation := operation("reanchorNote", `{"noteId":"28449e78-9938-4b80-811a-dffb6e44ed9e","sentenceIds":["b"],"ranges":[{"sentenceId":"b","start":1,"end":3}]}`)
	version := int64(1)
	mutation.BaseVersion = &version
	next, result, err := Apply("account-a", state, snapshot, mutation, at)
	if err != nil || result.Status != "applied" {
		t.Fatalf("reanchor: %+v %v", result, err)
	}
	note := next.Notes[0]
	if note.ID != original.ID || note.Text != original.Text || note.Quote != original.Quote || note.CreatedAt != original.CreatedAt || note.Version != 2 || note.ReanchorRequired || len(note.InactiveAnchors) != 0 || !reflect.DeepEqual(note.SentenceIDs, []string{"b"}) || len(note.Ranges) != 1 || note.Ranges[0].Quote != "第二句。" || note.Ranges[0].Start != 1 || note.Ranges[0].End != 3 {
		t.Fatalf("note changed or wrong anchor: %+v", note)
	}
	if !reflect.DeepEqual(state.Notes[0], original) {
		t.Fatal("mutated original snapshot")
	}
	unchanged, result, err := Apply("account-a", next, snapshot, mutation, at)
	if err != nil || result.Status != "note_conflict" || !reflect.DeepEqual(unchanged, next) {
		t.Fatal("stale note version overwritten", err, result)
	}
	target := snapshot
	target.Revision = 4
	migrated, err := Migrate("account-a", next, target, []onlinebulletins.SentenceMapping{{FromSentenceID: "b", ToSentenceIDs: []string{"b"}, Status: "moved"}}, at)
	if err != nil || migrated.Notes[0].ReanchorRequired || migrated.Notes[0].Quote != original.Quote || !reflect.DeepEqual(migrated.Notes[0].Ranges, note.Ranges) {
		t.Fatal("reanchored note lost on next publication", err)
	}
	version = 2
	mutation.DocumentRevision = 2
	unchanged, result, err = Apply("account-a", next, snapshot, mutation, at)
	if err != nil || result.Status != "revision_changed" || !reflect.DeepEqual(unchanged, next) {
		t.Fatal("stale document changed anchor", err)
	}
}

func TestReanchorNoteRejectsMissingVersionAndUntrustedAnchors(t *testing.T) {
	state := NewState("account-a", "document-a", 3)
	state, _ = apply(t, state, operation("createNote", `{"noteId":"28449e78-9938-4b80-811a-dffb6e44ed9e","sentenceIds":["a"],"text":"保留筆記"}`))
	state.Notes[0].ReanchorRequired = true
	for _, payload := range []string{
		`{"noteId":"28449e78-9938-4b80-811a-dffb6e44ed9e","sentenceIds":["b"]}`,
		`{"noteId":"28449e78-9938-4b80-811a-dffb6e44ed9e","sentenceIds":["foreign"],"ranges":[{"sentenceId":"foreign","start":0,"end":1}]}`,
		`{"noteId":"28449e78-9938-4b80-811a-dffb6e44ed9e","sentenceIds":["b"],"ranges":[{"sentenceId":"b","start":0,"end":99}]}`,
		`{"noteId":"28449e78-9938-4b80-811a-dffb6e44ed9e","sentenceIds":["b"],"ranges":[{"sentenceId":"b","start":0,"end":1}],"quote":"forged"}`,
	} {
		mutation := operation("reanchorNote", payload)
		version := int64(1)
		mutation.BaseVersion = &version
		if _, _, err := Apply("account-a", state, snapshot, mutation, at); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted invalid anchor %s: %v", payload, err)
		}
	}
	mutation := operation("reanchorNote", `{"noteId":"28449e78-9938-4b80-811a-dffb6e44ed9e","sentenceIds":["b"],"ranges":[{"sentenceId":"b","start":0,"end":1}]}`)
	if _, _, err := Apply("account-a", state, snapshot, mutation, at); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing version accepted", err)
	}
}
