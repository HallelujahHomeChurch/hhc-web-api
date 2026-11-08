package bulletinreader

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

var at = time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC)
var snapshot = Snapshot{DocumentID: "document-a", Revision: 3, Pages: []string{"page-a"}, Components: []string{"body"}, Sentences: []Anchor{{ID: "a", Text: "第一句。", ComponentID: "body", PageID: "page-a"}, {ID: "b", Text: "第二句。", ComponentID: "body", PageID: "page-a"}}}

func operation(kind, payload string) Mutation {
	return Mutation{MutationID: "a59c90d3-468f-4014-b59e-21865836ed7b", CreatedAt: at, DocumentRevision: 3, Kind: kind, Payload: json.RawMessage(payload)}
}
func apply(t *testing.T, state State, mutation Mutation) (State, Result) {
	t.Helper()
	next, result, err := Apply("account-a", state, snapshot, mutation, at)
	if err != nil {
		t.Fatal(err)
	}
	return next, result
}
func TestAtomicColorAndClearPreserveNotes(t *testing.T) {
	state := NewState("account-a", "document-a", 3)
	state, _ = apply(t, state, operation("setHighlight", `{"sentenceIds":["a"],"color":"red"}`))
	state, _ = apply(t, state, operation("createNote", `{"noteId":"28449e78-9938-4b80-811a-dffb6e44ed9e","sentenceIds":["b","a"],"text":"我的筆記"}`))
	next, result := apply(t, state, operation("setHighlight", `{"sentenceIds":["a","b"],"color":"blue"}`))
	if result.Status != "applied" || len(next.Highlights) != 2 || next.Highlights[0].Color != "blue" || next.Highlights[1].Color != "blue" {
		t.Fatal(next, result)
	}
	if state.Highlights[0].Color != "red" {
		t.Fatal("mutated input before transaction commit")
	}
	if !reflect.DeepEqual(next.Notes[0].SentenceIDs, []string{"a", "b"}) || next.Notes[0].Quote != "第一句。第二句。" {
		t.Fatal("unordered/untrusted quote", next.Notes)
	}
	cleared, _ := apply(t, next, operation("clearHighlight", `{"sentenceIds":["a","b"]}`))
	if len(cleared.Highlights) != 0 || !reflect.DeepEqual(cleared.Notes, next.Notes) {
		t.Fatal("clear changed notes")
	}
	_, _, err := Apply("account-a", next, snapshot, operation("setHighlight", `{"sentenceIds":["a","foreign"],"color":"yellow"}`), at)
	if !errors.Is(err, ErrInvalid) || next.Highlights[0].Color != "blue" {
		t.Fatal("partial group mutation", err)
	}
}
func TestNotesOptimisticVersionsAndDeleteConflict(t *testing.T) {
	state := NewState("account-a", "document-a", 3)
	state, _ = apply(t, state, operation("createNote", `{"noteId":"28449e78-9938-4b80-811a-dffb6e44ed9e","sentenceIds":["a"],"text":"first"}`))
	state, _ = apply(t, state, operation("createNote", `{"noteId":"ac3439e1-52c4-4f7e-a723-5f214c1134fb","sentenceIds":["a"],"text":"second"}`))
	edit := operation("editNote", `{"noteId":"28449e78-9938-4b80-811a-dffb6e44ed9e","text":"changed"}`)
	one := int64(1)
	edit.BaseVersion = &one
	state, _ = apply(t, state, edit)
	unchanged, conflict := apply(t, state, edit)
	if conflict.Status != "note_conflict" || conflict.Note.Version != 2 || !reflect.DeepEqual(state, unchanged) {
		t.Fatal("lost conflicting note", conflict)
	}
	remove := operation("deleteNote", `{"noteId":"28449e78-9938-4b80-811a-dffb6e44ed9e"}`)
	two := int64(2)
	remove.BaseVersion = &two
	state, _ = apply(t, state, remove)
	_, conflict = apply(t, state, edit)
	if conflict.Status != "note_conflict" || !conflict.Note.Deleted || conflict.Note.Text != "" || len(state.Notes) != 2 {
		t.Fatal("delete/edit conflict lost", conflict)
	}
}
func TestScopeRevisionAndProgress(t *testing.T) {
	state := NewState("account-a", "document-a", 3)
	mutation := operation("setProgress", `{"pageId":"page-a","componentId":"body","sentenceId":"b"}`)
	if _, _, err := Apply("account-b", state, snapshot, mutation, at); !errors.Is(err, ErrInvalid) {
		t.Fatal("cross-account write", err)
	}
	other := snapshot
	other.DocumentID = "children-document"
	if _, _, err := Apply("account-a", state, other, mutation, at); !errors.Is(err, ErrInvalid) {
		t.Fatal("cross-series document write", err)
	}
	mutation.DocumentRevision = 2
	_, changed := apply(t, state, mutation)
	if changed.Status != "revision_changed" {
		t.Fatal(changed)
	}
	mutation.DocumentRevision = 3
	state, _ = apply(t, state, mutation)
	older := operation("setProgress", `{"pageId":"page-a","componentId":"body","sentenceId":"a"}`)
	older.CreatedAt = at.Add(-time.Hour)
	state, _ = apply(t, state, older)
	if state.Progress.SentenceID != "b" {
		t.Fatal("older progress won")
	}
}
func TestClosedPayloadBoundsAndRecovery(t *testing.T) {
	state := NewState("account-a", "document-a", 3)
	for _, payload := range []string{`{"sentenceIds":["a"],"color":"green"}`, `{"sentenceIds":["a","a"],"color":"red"}`, `{"sentenceIds":["a"],"color":"red","accountId":"other"}`, `null`} {
		if _, _, err := Apply("account-a", state, snapshot, operation("setHighlight", payload), at); !errors.Is(err, ErrInvalid) {
			t.Fatal("accepted invalid payload", payload, err)
		}
	}
	old := operation("clearHighlight", `{"sentenceIds":["a"]}`)
	old.CreatedAt = at.Add(-90*24*time.Hour - time.Nanosecond)
	_, result := apply(t, state, old)
	if result.Status != "recovery_required" {
		t.Fatal(result)
	}
}

func TestMutationRevisionAndUnicodeBounds(t *testing.T) {
	state := NewState("account-a", "document-a", 3)
	for _, revision := range []int64{0, -1, 9007199254740992} {
		m := operation("clearHighlight", `{"sentenceIds":["a"]}`)
		m.DocumentRevision = revision
		if _, _, err := Apply("account-a", state, snapshot, m, at); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid revision %d: %v", revision, err)
		}
	}
	for _, length := range []int{10000, 10001} {
		raw, _ := json.Marshal(map[string]any{"noteId": "28449e78-9938-4b80-811a-dffb6e44ed9e", "sentenceIds": []string{"a"}, "text": strings.Repeat("😀", length)})
		_, _, err := Apply("account-a", state, snapshot, operation("createNote", string(raw)), at)
		if (err == nil) != (length == 10000) {
			t.Fatal("Unicode code point limit", length, err)
		}
	}
}

func TestProgressAcceptsContinuationPageOnlyWhenSentenceIsThere(t *testing.T) {
	extended := snapshot
	extended.Pages = []string{"page-a", "page-b", "page-c"}
	extended.SentencePages = map[string][]string{"a": {"page-a", "page-b"}}
	state := NewState("account-a", "document-a", 3)
	mutation := operation("setProgress", `{"pageId":"page-b","componentId":"body","sentenceId":"a"}`)
	next, _, err := Apply("account-a", state, extended, mutation, at)
	if err != nil || next.Progress.PageID != "page-b" {
		t.Fatal("continuation rejected", err)
	}
	mutation.Payload = json.RawMessage(`{"pageId":"page-c","componentId":"body","sentenceId":"a"}`)
	if _, _, err = Apply("account-a", state, extended, mutation, at); !errors.Is(err, ErrInvalid) {
		t.Fatal("unrelated page accepted", err)
	}
}
