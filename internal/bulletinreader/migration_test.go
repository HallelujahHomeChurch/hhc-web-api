package bulletinreader

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestLargeSplitKeepsNoteAsExplicitRecoveryInsteadOfBlockingAllState(t *testing.T) {
	state := NewState("account-a", "document-a", 3)
	state, _ = apply(t, state, operation("createNote", `{"noteId":"28449e78-9938-4b80-811a-dffb6e44ed9e","sentenceIds":["a"],"text":"keep every word"}`))
	target := Snapshot{DocumentID: "document-a", Revision: 4}
	ids := []string{}
	for i := 0; i <= MaxAnchors; i++ {
		id := fmt.Sprintf("split-%d", i)
		ids = append(ids, id)
		target.Sentences = append(target.Sentences, Anchor{ID: id, Text: "part"})
	}
	next, err := Migrate("account-a", state, target, []onlinebulletins.SentenceMapping{{FromSentenceID: "a", Status: "split", ToSentenceIDs: ids}}, at)
	if err != nil {
		t.Fatal(err)
	}
	if !next.Notes[0].ReanchorRequired || len(next.Notes[0].SentenceIDs) != 0 || next.Notes[0].Quote != state.Notes[0].Quote || next.Notes[0].Text != state.Notes[0].Text || len(next.Notes[0].InactiveAnchors) != 1 {
		t.Fatal("large split discarded private data", next.Notes)
	}
}

func TestCorruptMappingCardinalityFailsClosed(t *testing.T) {
	state := NewState("account-a", "document-a", 3)
	state, _ = apply(t, state, operation("setHighlight", `{"sentenceIds":["a"],"color":"red"}`))
	target := Snapshot{DocumentID: "document-a", Revision: 4, Sentences: []Anchor{{ID: "a"}, {ID: "b"}}}
	for _, mapping := range []onlinebulletins.SentenceMapping{
		{FromSentenceID: "a", Status: "split", ToSentenceIDs: []string{"a"}},
		{FromSentenceID: "a", Status: "merged", ToSentenceIDs: []string{"a", "b"}},
		{FromSentenceID: "a", Status: "unchanged", ToSentenceIDs: []string{"b"}},
	} {
		if _, err := Migrate("account-a", state, target, []onlinebulletins.SentenceMapping{mapping}, at); !errors.Is(err, ErrMapping) {
			t.Fatalf("accepted corrupt mapping %#v: %v", mapping, err)
		}
	}
}

func TestPublishedSplitRemovalAndIdempotentMigration(t *testing.T) {
	state := NewState("account-a", "document-a", 3)
	state, _ = apply(t, state, operation("setHighlight", `{"sentenceIds":["a","b"],"color":"red"}`))
	state, _ = apply(t, state, operation("createNote", `{"noteId":"28449e78-9938-4b80-811a-dffb6e44ed9e","sentenceIds":["a","b"],"text":"keep this"}`))
	target := Snapshot{DocumentID: "document-a", Revision: 40, Sentences: []Anchor{{ID: "x", Text: "第一", ComponentID: "body"}, {ID: "y", Text: "句。", ComponentID: "body"}}}
	mappings := []onlinebulletins.SentenceMapping{{FromSentenceID: "a", Status: "split", ToSentenceIDs: []string{"x", "y"}}, {FromSentenceID: "b", Status: "removed", ToSentenceIDs: []string{}}}
	next, err := Migrate("account-a", state, target, mappings, at)
	if err != nil {
		t.Fatal(err)
	}
	if next.AppliedRevision != 40 || len(next.Highlights) != 3 || !reflect.DeepEqual(next.Notes[0].SentenceIDs, []string{"x", "y"}) || len(next.Notes[0].InactiveAnchors) != 1 || next.Notes[0].Quote != state.Notes[0].Quote {
		t.Fatal("lost private evidence", next)
	}
	again, err := Migrate("account-a", next, target, nil, at)
	if err != nil || !reflect.DeepEqual(next, again) {
		t.Fatal("non-idempotent migration", err)
	}
	if state.AppliedRevision != 3 || len(state.Highlights) != 2 {
		t.Fatal("mutated prior snapshot")
	}
	_, err = Migrate("account-a", state, target, mappings[:1], at)
	if !errors.Is(err, ErrMapping) {
		t.Fatal("missing mapping silently dropped private data", err)
	}
}
func TestDifferentColorMergeNeedsExplicitResolution(t *testing.T) {
	state := NewState("account-a", "document-a", 3)
	state, _ = apply(t, state, operation("setHighlight", `{"sentenceIds":["a"],"color":"red"}`))
	state, _ = apply(t, state, operation("setHighlight", `{"sentenceIds":["b"],"color":"blue"}`))
	target := Snapshot{DocumentID: "document-a", Revision: 4, Sentences: []Anchor{{ID: "z", Text: "merged", ComponentID: "body"}}}
	mappings := []onlinebulletins.SentenceMapping{{FromSentenceID: "a", Status: "merged", ToSentenceIDs: []string{"z"}}, {FromSentenceID: "b", Status: "merged", ToSentenceIDs: []string{"z"}}}
	next, err := Migrate("account-a", state, target, mappings, at)
	if err != nil || len(next.Conflicts) != 1 {
		t.Fatal("merge color was silently chosen", next, err)
	}
	repeated, err := Migrate("account-a", state, target, mappings, at)
	if err != nil || !reflect.DeepEqual(next, repeated) {
		t.Fatal("conflict identity changed")
	}
	mutation := operation("resolveHighlightMigrationConflict", `{"conflictId":"`+next.Conflicts[0].ID+`","chosenColor":"yellow","currentRevision":4}`)
	mutation.DocumentRevision = 4
	resolved, result, err := Apply("account-a", next, target, mutation, at)
	if err != nil || result.Status != "applied" || len(resolved.Conflicts) != 0 {
		t.Fatal(result, err)
	}
	found := false
	for _, highlight := range resolved.Highlights {
		if highlight.SentenceID == "z" && highlight.Active && highlight.Color == "yellow" {
			found = true
		}
	}
	if !found {
		t.Fatal("resolved choice missing")
	}
}
