package bulletinreader

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestPartialHighlightRecolorAndClear(t *testing.T) {
	state := NewState("account-a", "document-a", 3)
	state, _ = apply(t, state, operation("setHighlight", `{"sentenceIds":["a"],"color":"yellow"}`))
	original, _ := json.Marshal(state)
	next, _ := apply(t, state, operation("setHighlight", `{"sentenceIds":["a"],"color":"red","ranges":[{"sentenceId":"a","start":1,"end":3}]}`))
	encoded, _ := json.Marshal(next.Highlights[0])
	var got struct {
		Segments []struct {
			Start, End int
			Color      string
		}
	}
	_ = json.Unmarshal(encoded, &got)
	if len(got.Segments) != 3 || got.Segments[0].Start != 0 || got.Segments[0].End != 1 || got.Segments[1].Color != "red" || got.Segments[2].Start != 3 || got.Segments[2].End != 4 {
		t.Fatalf("wrong overlap: %s", encoded)
	}
	unchanged, _ := json.Marshal(state)
	if string(original) != string(unchanged) {
		t.Fatal("mutated caller snapshot")
	}
	next, _ = apply(t, next, operation("clearHighlight", `{"sentenceIds":["a"],"ranges":[{"sentenceId":"a","start":1,"end":4}]}`))
	encoded, _ = json.Marshal(next.Highlights[0])
	_ = json.Unmarshal(encoded, &got)
	if len(got.Segments) != 1 || got.Segments[0].End != 1 || got.Segments[0].Color != "yellow" {
		t.Fatalf("wrong partial clear: %s", encoded)
	}
	next, _ = apply(t, next, operation("clearHighlight", `{"sentenceIds":["a"],"ranges":[{"sentenceId":"a","start":0,"end":1}]}`))
	if len(next.Highlights) != 0 {
		t.Fatal("empty highlight retained")
	}
}

func TestRangePayloadValidation(t *testing.T) {
	for _, ranges := range []string{`null`, `[]`, `[{"sentenceId":"a","end":2}]`, `[{"sentenceId":"a","start":0,"end":0}]`, `[{"sentenceId":"a","start":-1,"end":2}]`, `[{"sentenceId":"a","start":1.5,"end":2}]`, `[{"sentenceId":"a","start":0,"end":5}]`, `[{"sentenceId":"foreign","start":0,"end":2}]`, `[{"sentenceId":"a","start":0,"end":2,"quote":"forged"}]`, `[{"sentenceId":"a","start":0,"end":2},{"sentenceId":"a","start":2,"end":4}]`} {
		_, _, err := Apply("account-a", NewState("account-a", "document-a", 3), snapshot, operation("setHighlight", `{"sentenceIds":["a"],"color":"blue","ranges":`+ranges+`}`), at)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %s: %v", ranges, err)
		}
	}
}

func TestRangeSegmentLimitAndAdjacentMerge(t *testing.T) {
	state := NewState("account-a", "document-a", 3)
	state, _ = apply(t, state, operation("setHighlight", `{"sentenceIds":["a"],"color":"yellow","ranges":[{"sentenceId":"a","start":0,"end":2}]}`))
	state, _ = apply(t, state, operation("setHighlight", `{"sentenceIds":["a"],"color":"yellow","ranges":[{"sentenceId":"a","start":2,"end":4}]}`))
	if !reflect.DeepEqual(state.Highlights[0].Segments, []HighlightSegment{{Start: 0, End: 4, Color: "yellow"}}) {
		t.Fatal("equal neighbors not merged")
	}
	source := snapshot
	source.Sentences = []Anchor{{ID: "a", Text: strings.Repeat("文", 513), ComponentID: "body"}}
	state.Highlights[0].Quote = source.Sentences[0].Text
	state.Highlights[0].Segments = nil
	for i := 0; i < 256; i++ {
		state.Highlights[0].Segments = append(state.Highlights[0].Segments, HighlightSegment{Start: i * 2, End: i*2 + 1, Color: "yellow"})
	}
	_, _, err := Apply("account-a", state, source, operation("setHighlight", `{"sentenceIds":["a"],"color":"blue","ranges":[{"sentenceId":"a","start":511,"end":512}]}`), at)
	if !errors.Is(err, ErrInvalid) || len(state.Highlights[0].Segments) != 256 {
		t.Fatal("unbounded segments or mutated rollback", err)
	}
}

func TestRangeNotesUseTrustedScalarQuote(t *testing.T) {
	source := snapshot
	source.Sentences = []Anchor{{ID: "a", Text: "甲😀乙丙", ComponentID: "body"}}
	next, _, err := Apply("account-a", NewState("account-a", "document-a", 3), source, operation("createNote", `{"noteId":"28449e78-9938-4b80-811a-dffb6e44ed9e","sentenceIds":["a"],"ranges":[{"sentenceId":"a","start":1,"end":3}],"text":"private"}`), at)
	if err != nil {
		t.Fatal(err)
	}
	if next.Notes[0].Quote != "😀乙" {
		t.Fatal("wrong scalar quote", next.Notes[0].Quote)
	}
	encoded, _ := json.Marshal(next.Notes[0])
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(encoded, &fields)
	if string(fields["ranges"]) == "" {
		t.Fatal("missing persisted note ranges")
	}
	cleared, _, err := Apply("account-a", next, source, operation("clearHighlight", `{"sentenceIds":["a"],"ranges":[{"sentenceId":"a","start":1,"end":3}]}`), at)
	if err != nil || !reflect.DeepEqual(cleared.Notes, next.Notes) {
		t.Fatal("clear changed note", err)
	}
}

func TestRangeMigrationNeverWidensOrGuesses(t *testing.T) {
	state := NewState("account-a", "document-a", 3)
	state, _ = apply(t, state, operation("setHighlight", `{"sentenceIds":["a"],"color":"blue","ranges":[{"sentenceId":"a","start":1,"end":3}]}`))
	state, _ = apply(t, state, operation("createNote", `{"noteId":"28449e78-9938-4b80-811a-dffb6e44ed9e","sentenceIds":["a"],"ranges":[{"sentenceId":"a","start":1,"end":3}],"text":"keep me"}`))
	target := snapshot
	target.Revision = 4
	mapping := []onlinebulletins.SentenceMapping{{FromSentenceID: "a", ToSentenceIDs: []string{"a"}, Status: "unchanged"}, {FromSentenceID: "b", ToSentenceIDs: []string{"b"}, Status: "unchanged"}}
	next, err := Migrate("account-a", state, target, mapping, at)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(state.Highlights[0])
	after, _ := json.Marshal(next.Highlights[0])
	var a, b map[string]json.RawMessage
	_ = json.Unmarshal(before, &a)
	_ = json.Unmarshal(after, &b)
	if string(a["segments"]) != string(b["segments"]) || !next.Highlights[0].Active {
		t.Fatal("identical text lost range")
	}
	target.Sentences = append([]Anchor(nil), snapshot.Sentences...)
	target.Sentences[0].Text = "改過的第一句。"
	next, err = Migrate("account-a", state, target, mapping, at)
	if err != nil {
		t.Fatal(err)
	}
	if next.Highlights[0].Active || next.Highlights[0].Quote != "第一句。" || !next.Notes[0].ReanchorRequired || len(next.Notes[0].SentenceIDs) != 0 || next.Notes[0].Quote != "一句" {
		t.Fatal("guessed offsets or lost original private quote", next)
	}
}

func TestWhitespaceOnlyMigrationKeepsExactSelectedCharacters(t *testing.T) {
	for _, item := range []struct {
		old, next                      string
		start, end, wantStart, wantEnd int
		active                         bool
	}{
		{"神的話 和神的靈。 ", "神的話和神的靈。", 5, 8, 4, 7, true},
		{"神的話和神的靈。", "神的話 和神的靈。 ", 4, 7, 5, 8, true},
		{"ice cream", "icecream", 4, 9, 4, 9, false},
		{"teachers' union", "teachers'union", 10, 15, 10, 15, false},
	} {
		state := NewState("account-a", "document-a", 3)
		state.Highlights = []Highlight{{SentenceID: "a", Quote: item.old, Active: true, Version: 1, Color: "blue", Segments: []HighlightSegment{{Start: item.start, End: item.end, Color: "blue"}}}}
		state.Notes = []Note{{ID: "note", Text: "private", Quote: "神的靈", SentenceIDs: []string{"a"}, Ranges: []NoteRange{{TextRange: TextRange{SentenceID: "a", Start: item.start, End: item.end}, Quote: item.old}}}}
		target := Snapshot{DocumentID: "document-a", Revision: 4, Sentences: []Anchor{{ID: "a", Text: item.next}}}
		got, err := Migrate("account-a", state, target, []onlinebulletins.SentenceMapping{{FromSentenceID: "a", ToSentenceIDs: []string{"a"}, Status: "unchanged"}}, at)
		if err != nil {
			t.Fatal(err)
		}
		if got.Highlights[0].Active != item.active || got.Notes[0].ReanchorRequired == item.active {
			t.Fatalf("wrong whitespace migration: %+v", got)
		}
		if item.active {
			segment, selected := got.Highlights[0].Segments[0], got.Notes[0].Ranges[0]
			if segment.Start != item.wantStart || segment.End != item.wantEnd || selected.Start != item.wantStart || selected.End != item.wantEnd || selected.Quote != item.next || got.Highlights[0].Quote != item.next {
				t.Fatalf("range shifted/widened: %+v", got)
			}
		}
		if state.Highlights[0].Segments[0].Start != item.start || state.Notes[0].Ranges[0].Quote != item.old {
			t.Fatal("private source state mutated")
		}
	}
}

func TestMergedLegacyHighlightRetainsPartialSourceUntilExplicitResolution(t *testing.T) {
	state := NewState("account-a", "document-a", 3)
	state, _ = apply(t, state, operation("setHighlight", `{"sentenceIds":["a"],"color":"blue","ranges":[{"sentenceId":"a","start":1,"end":3}]}`))
	state, _ = apply(t, state, operation("setHighlight", `{"sentenceIds":["b"],"color":"yellow"}`))
	original := state.Highlights[0]
	target := snapshot
	target.Revision = 4
	target.Sentences = []Anchor{{ID: "a", Text: "第一句。第二句。", ComponentID: "body"}}
	mapping := []onlinebulletins.SentenceMapping{{FromSentenceID: "a", ToSentenceIDs: []string{"a"}, Status: "merged"}, {FromSentenceID: "b", ToSentenceIDs: []string{"a"}, Status: "merged"}}
	next, err := Migrate("account-a", state, target, mapping, at)
	if err != nil || len(next.Conflicts) != 1 {
		t.Fatalf("missing partial-source conflict: %v %+v", err, next)
	}
	for revision := int64(4); revision <= 5; revision++ {
		if revision == 5 {
			target.Revision = revision
			next, err = Migrate("account-a", next, target, []onlinebulletins.SentenceMapping{{FromSentenceID: "a", ToSentenceIDs: []string{"a"}, Status: "unchanged"}}, at)
		}
		if err != nil || len(next.Conflicts) != 1 {
			t.Fatal("lost subsequent conflict", err)
		}
		index := slices.IndexFunc(next.Highlights, func(h Highlight) bool { return h.SentenceID == "a" })
		if index < 0 || next.Highlights[index].Active || next.Highlights[index].Quote != original.Quote || !reflect.DeepEqual(next.Highlights[index].Segments, original.Segments) {
			t.Fatal("lost retained partial source", next)
		}
	}
	payload, _ := json.Marshal(map[string]any{"conflictId": next.Conflicts[0].ID, "chosenColor": "red", "currentRevision": target.Revision})
	mutation := operation("resolveHighlightMigrationConflict", string(payload))
	mutation.DocumentRevision = target.Revision
	resolved, result, err := Apply("account-a", next, target, mutation, at)
	if err != nil || result.Status != "applied" || len(resolved.Conflicts) != 0 {
		t.Fatal("explicit resolution failed", err, result)
	}
	index := slices.IndexFunc(resolved.Highlights, func(h Highlight) bool { return h.SentenceID == "a" })
	if index < 0 || !resolved.Highlights[index].Active || resolved.Highlights[index].Color != "red" || len(resolved.Highlights[index].Segments) != 0 {
		t.Fatal("wrong explicit full target resolution")
	}
}
