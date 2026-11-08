package bulletinreader

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestBookmarkContinuesAcrossProvenRepagination(t *testing.T) {
	previous := bookmarkSnapshot()
	state, _, err := Apply("account-a", NewState("account-a", previous.DocumentID, previous.Revision), previous, bookmarkCreate(bookmarkID), at)
	if err != nil {
		t.Fatal(err)
	}
	before := clone(state)
	for _, test := range []struct {
		name, text string
		offset     int
	}{
		{"same text on a new page", "A😀中文", 1},
		{"source whitespace changed", " A😀中文", 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := Snapshot{DocumentID: previous.DocumentID, Revision: previous.Revision + 1, Pages: []string{"new-page"}, Sentences: []Anchor{{ID: "a", Text: test.text, PageID: "new-page"}}, SentenceFragments: map[string][]SourceFragment{"a": {{PageID: "new-page", Start: 0, End: len([]rune(test.text))}}}}
			mappings := []onlinebulletins.SentenceMapping{{FromSentenceID: "a", ToSentenceIDs: []string{"a"}, Status: "moved"}}
			next, err := MigrateFromSnapshot("account-a", state, previous, target, mappings, at.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			b := next.Bookmarks[0]
			if b.ReanchorRequired || b.DocumentRevision != target.Revision || b.PageID != "new-page" || b.Offset != test.offset || b.Version != 2 || b.Name != state.Bookmarks[0].Name {
				t.Fatal("bookmark did not continue", b)
			}
			if !reflect.DeepEqual(before, state) {
				t.Fatal("mutated original state")
			}
		})
	}
}

func TestBookmarkMigrationRetainsUnprovenLocations(t *testing.T) {
	previous := bookmarkSnapshot()
	state, _, err := Apply("account-a", NewState("account-a", previous.DocumentID, previous.Revision), previous, bookmarkCreate(bookmarkID), at)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, text string
		fragments  []SourceFragment
	}{
		{"changed meaning", "B😀中文", []SourceFragment{{PageID: "new-page", Start: 0, End: 4}}},
		{"meaningful word separator", "A 😀中文", []SourceFragment{{PageID: "new-page", Start: 0, End: 5}}},
		{"ambiguous page", "A😀中文", []SourceFragment{{PageID: "new-page", Start: 0, End: 4}, {PageID: "other-page", Start: 0, End: 4}}},
		{"missing page evidence", "A😀中文", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := Snapshot{DocumentID: previous.DocumentID, Revision: previous.Revision + 1, Pages: []string{"new-page", "other-page"}, Sentences: []Anchor{{ID: "a", Text: test.text}}, SentenceFragments: map[string][]SourceFragment{"a": test.fragments}}
			next, err := MigrateFromSnapshot("account-a", state, previous, target, []onlinebulletins.SentenceMapping{{FromSentenceID: "a", ToSentenceIDs: []string{"a"}, Status: "moved"}}, at.Add(time.Minute))
			if err != nil {
				t.Fatal(err)
			}
			b := next.Bookmarks[0]
			if !b.ReanchorRequired || b.PageID != state.Bookmarks[0].PageID || b.Offset != state.Bookmarks[0].Offset || b.Name != state.Bookmarks[0].Name {
				t.Fatal("unproven location was lost or guessed", b)
			}
		})
	}
	wrong := previous
	wrong.DocumentID = "other-document"
	if _, err := MigrateFromSnapshot("account-a", state, wrong, previous, nil, at); !errors.Is(err, ErrMapping) {
		t.Fatal("accepted unrelated source snapshot", err)
	}
}
