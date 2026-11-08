package bulletinreader

import (
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"reflect"
	"strings"
	"testing"
)

func TestPreserveHighlightHistoryDeduplicatesLegacySourcesWithoutRevivingTombstones(t *testing.T) {
	state := NewState("account-a", "document-a", 3)
	old := Highlight{SentenceID: "removed", Color: "yellow", Quote: "Original", Version: 1, Segments: []HighlightSegment{{Start: 0, End: 2, Color: "yellow"}}}
	active := old
	active.SentenceID, active.Active = "current", true
	state.Highlights = []Highlight{old, active}
	state.Conflicts = []MigrationConflict{{ID: "conflict", Sources: []Highlight{old}}}
	next := PreserveHighlightHistory(state)
	if len(next.HighlightHistory) != 1 || len(state.HighlightHistory) != 0 {
		t.Fatal("duplicate evidence or mutated original")
	}
	next.HighlightHistory[0].Highlight.Segments[0].End = 1
	if state.Highlights[0].Segments[0].End != 2 {
		t.Fatal("history aliases original segments")
	}
	next.HighlightHistory[0].Highlight = nil
	again := PreserveHighlightHistory(next)
	if len(again.HighlightHistory) != 1 || again.HighlightHistory[0].Highlight != nil {
		t.Fatal("consumed history revived")
	}
}

func TestRestoreHistoricalHighlightRejectsUnsafeOrConflictingSelection(t *testing.T) {
	state := NewState("account-a", "document-a", 3)
	archiveHighlight(&state, Highlight{SentenceID: "a", Color: "yellow", Quote: "Old", Version: 1, UpdatedAt: at})
	id := state.HighlightHistory[0].ID
	valid := `{"historyId":"` + id + `","sentenceIds":["b"],"color":"red","ranges":[{"sentenceId":"b","start":0,"end":2}]}`
	for _, payload := range []string{`{"historyId":"` + id + `","sentenceIds":["b"],"color":"red"}`, strings.Replace(valid, `"end":2`, `"end":99`, 1), strings.Replace(valid, `"red"`, `"green"`, 1)} {
		if _, _, err := Apply("account-a", state, snapshot, operation("restoreHighlight", payload), at); !errors.Is(err, ErrInvalid) {
			t.Fatal("unsafe restore accepted", err)
		}
	}
	mutation := operation("restoreHighlight", valid)
	if _, _, err := Apply("other-account", state, snapshot, mutation, at); !errors.Is(err, ErrInvalid) {
		t.Fatal("cross-account history accepted")
	}
	mutation.DocumentRevision = 2
	unchanged, result, err := Apply("account-a", state, snapshot, mutation, at)
	if err != nil || result.Status != "revision_changed" || !reflect.DeepEqual(unchanged, state) {
		t.Fatal("stale range reused", err, result)
	}
	mutation.DocumentRevision = 3
	state.Conflicts = []MigrationConflict{{ID: "pending", SentenceIDs: []string{"b"}, Sources: []Highlight{*state.HighlightHistory[0].Highlight}}}
	unchanged, result, err = Apply("account-a", state, snapshot, mutation, at)
	if err != nil || result.Status != "migration_conflict" || !reflect.DeepEqual(unchanged, state) {
		t.Fatal("conflicting restore consumed evidence", err, result)
	}
}

func TestHistoricalHighlightRestoreAndDiscardAreExplicitAndCannotResurrect(t *testing.T) {
	for _, kind := range []string{"restoreHighlight", "discardHighlightHistory"} {
		t.Run(kind, func(t *testing.T) {
			state := NewState("account-a", "document-a", 3)
			old := Highlight{SentenceID: "a", Color: "yellow", Quote: "Private old quotation", Version: 2, UpdatedAt: at, Segments: []HighlightSegment{{Start: 1, End: 3, Color: "yellow"}}}
			state.Highlights = []Highlight{old}
			archiveHighlight(&state, old)
			id := state.HighlightHistory[0].ID
			payload := map[string]any{"historyId": id}
			if kind == "restoreHighlight" {
				payload["sentenceIds"] = []string{"b"}
				payload["color"] = "blue"
				payload["ranges"] = []TextRange{{SentenceID: "b", Start: 0, End: 2}}
			}
			body, _ := json.Marshal(payload)
			next, result, err := Apply("account-a", state, snapshot, operation(kind, string(body)), at)
			if err != nil || result.Status != "applied" {
				t.Fatal("history action rejected", err, result)
			}
			encoded, _ := json.Marshal(next)
			if strings.Contains(string(encoded), old.Quote) {
				t.Fatal("retired quote retained after explicit action")
			}
			if kind == "restoreHighlight" && (len(next.Highlights) != 1 || next.Highlights[0].SentenceID != "b" || !reflect.DeepEqual(next.Highlights[0].Segments, []HighlightSegment{{Start: 0, End: 2, Color: "blue"}})) {
				t.Fatal("wrong restored range", next)
			}
			repeated, missing, err := Apply("account-a", next, snapshot, operation(kind, string(body)), at)
			if err != nil || missing.Status != "conflict_not_found" || !reflect.DeepEqual(next, repeated) {
				t.Fatal("consumed history resurrected", err, missing)
			}
			target := snapshot
			target.Revision = 4
			next, err = Migrate("account-a", next, target, []onlinebulletins.SentenceMapping{{FromSentenceID: "a", ToSentenceIDs: []string{"a"}, Status: "unchanged"}, {FromSentenceID: "b", ToSentenceIDs: []string{"b"}, Status: "unchanged"}}, at)
			if err != nil || len(next.HighlightHistory) != 1 {
				t.Fatal("history replayed", err, next)
			}
			if state.Highlights[0].Quote != old.Quote {
				t.Fatal("input changed before commit")
			}
		})
	}
}

func TestNewHighlightPreservesInactiveQuoteInHistory(t *testing.T) {
	state := NewState("account-a", "document-a", 3)
	old := Highlight{SentenceID: "a", Color: "yellow", Quote: "Previous wording", Active: false, Version: 2, UpdatedAt: at, Segments: []HighlightSegment{{Start: 1, End: 3, Color: "yellow"}}}
	state.Highlights = []Highlight{old}
	next, result := apply(t, state, operation("setHighlight", `{"sentenceIds":["a"],"color":"red","ranges":[{"sentenceId":"a","start":0,"end":2}]}`))
	if result.Status != "applied" || len(next.HighlightHistory) != 1 || !reflect.DeepEqual(*next.HighlightHistory[0].Highlight, old) {
		t.Fatal("lost original private selection", next)
	}
	if len(state.HighlightHistory) != 0 || !reflect.DeepEqual(state.Highlights[0], old) {
		t.Fatal("mutated input")
	}
	next.HighlightHistory[0].Highlight.Segments[0].Start = 0
	if state.Highlights[0].Segments[0].Start != 1 {
		t.Fatal("history shares mutable source segments")
	}
}

func TestChangedPartialHighlightHistoryIsStableAcrossRevisions(t *testing.T) {
	state := NewState("account-a", "document-a", 3)
	state, _ = apply(t, state, operation("setHighlight", `{"sentenceIds":["a"],"color":"yellow","ranges":[{"sentenceId":"a","start":1,"end":3}]}`))
	target := snapshot
	target.Revision = 4
	target.Sentences = append([]Anchor(nil), snapshot.Sentences...)
	target.Sentences[0].Text = "Updated wording"
	mapping := []onlinebulletins.SentenceMapping{{FromSentenceID: "a", ToSentenceIDs: []string{"a"}, Status: "unchanged"}, {FromSentenceID: "b", ToSentenceIDs: []string{"b"}, Status: "unchanged"}}
	next, err := Migrate("account-a", state, target, mapping, at)
	if err != nil || len(next.HighlightHistory) != 1 || next.HighlightHistory[0].Highlight.Quote != snapshot.Sentences[0].Text {
		t.Fatal("missing migrated evidence", err, next)
	}
	id := next.HighlightHistory[0].ID
	target.Revision = 5
	next, err = Migrate("account-a", next, target, mapping, at)
	if err != nil || len(next.HighlightHistory) != 1 || next.HighlightHistory[0].ID != id {
		t.Fatal("duplicate history", err, next)
	}
	mutation := operation("clearHighlight", `{"sentenceIds":["a"]}`)
	mutation.DocumentRevision = 5
	next, result, err := Apply("account-a", next, target, mutation, at)
	if err != nil || result.Status != "applied" || len(next.Highlights) != 0 || len(next.HighlightHistory) != 1 {
		t.Fatal("clear erased unmatched old quote", err, next)
	}
}
