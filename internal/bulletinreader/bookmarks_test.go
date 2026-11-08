package bulletinreader

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/google/uuid"
)

var bookmarkID = "49f10a14-688e-4726-b46f-51b68f950128"

func bookmarkSnapshot() Snapshot {
	s := snapshot
	s.Pages = []string{"page-a", "page-b"}
	s.Sentences = []Anchor{{ID: "a", Text: "A😀中文", PageID: "page-a"}, {ID: "canonical-title", Text: "Title", PageID: "page-b"}}
	s.SentenceFragments = map[string][]SourceFragment{"a": {{PageID: "page-a", Start: 0, End: 2}, {PageID: "page-b", Start: 2, End: 4}}, "canonical-title": {{PageID: "page-b", Start: 0, End: 5}}}
	return s
}
func bookmarkOperation(kind string, base int64, payload any) Mutation {
	m := operation(kind, "{}")
	m.Payload, _ = json.Marshal(payload)
	m.MutationID = uuid.NewString()
	if base > 0 {
		m.BaseVersion = &base
	}
	return m
}
func bookmarkCreate(id string) Mutation {
	return bookmarkOperation("createBookmark", 0, map[string]any{"bookmarkId": id, "name": "  same 😀  ", "sentenceId": "a", "offset": 1, "pageId": "page-a"})
}
func TestBookmarkCASAndPrivateTombstone(t *testing.T) {
	s := NewState("account-a", "document-a", 3)
	target := bookmarkSnapshot()
	run := func(m Mutation) Result {
		t.Helper()
		before := clone(s)
		next, r, err := Apply("account-a", s, target, m, at)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, s) {
			t.Fatal("mutated caller")
		}
		s = next
		return r
	}
	if r := run(bookmarkCreate(bookmarkID)); r.Status != "applied" || r.Bookmark.Name != "same 😀" || r.Bookmark.Version != 1 {
		t.Fatal(r)
	}
	if r := run(bookmarkCreate(bookmarkID)); r.Status != "bookmark_conflict" {
		t.Fatal(r)
	}
	rename := bookmarkOperation("renameBookmark", 1, map[string]any{"bookmarkId": bookmarkID, "name": "new"})
	if r := run(rename); r.Bookmark.Version != 2 || r.Bookmark.CreatedAt != at {
		t.Fatal(r)
	}
	if r := run(rename); r.Status != "bookmark_conflict" || r.Bookmark.Version != 2 || r.Bookmark.Name != "new" {
		t.Fatal(r)
	}
	reanchor := bookmarkOperation("reanchorBookmark", 2, map[string]any{"bookmarkId": bookmarkID, "sentenceId": "a", "offset": 3, "pageId": "page-b"})
	if r := run(reanchor); r.Bookmark.PageID != "page-b" || r.Bookmark.Offset != 3 {
		t.Fatal(r)
	}
	if r := run(bookmarkOperation("deleteBookmark", 3, map[string]any{"bookmarkId": bookmarkID})); !r.Bookmark.Deleted || r.Bookmark.Name != "" || r.Bookmark.SentenceID != "" || r.Bookmark.PageID != "" || r.Bookmark.Offset != 0 {
		t.Fatal(r)
	}
	if r := run(bookmarkOperation("deleteBookmark", 4, map[string]any{"bookmarkId": bookmarkID})); r.Status != "bookmark_conflict" || !r.Bookmark.Deleted {
		t.Fatal(r)
	}
	if r := run(bookmarkCreate(bookmarkID)); r.Status != "bookmark_conflict" {
		t.Fatal("resurrected", r)
	}
	if len(s.Notes) != 0 || len(s.Highlights) != 0 || s.Progress != nil {
		t.Fatal("changed other private data")
	}
}
func TestBookmarkStrictTargetNameAndScope(t *testing.T) {
	s := NewState("account-a", "document-a", 3)
	target := bookmarkSnapshot()
	for _, change := range []map[string]any{{"offset": -1}, {"offset": 4}, {"offset": 2}, {"offset": nil}, {"pageId": "page-b"}, {"pageId": "foreign"}, {"sentenceId": "foreign"}, {"name": "  \u3000"}, {"name": strings.Repeat("😀", 81)}, {"quote": "forged"}, {"accountId": "other"}} {
		m := bookmarkCreate(bookmarkID)
		p := map[string]any{}
		_ = json.Unmarshal(m.Payload, &p)
		for k, v := range change {
			p[k] = v
		}
		m.Payload, _ = json.Marshal(p)
		if _, _, err := Apply("account-a", s, target, m, at); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted %#v: %v", change, err)
		}
	}
	m := bookmarkCreate(bookmarkID)
	if _, _, err := Apply("other", s, target, m, at); !errors.Is(err, ErrInvalid) {
		t.Fatal("cross account", err)
	}
	foreign := target
	foreign.DocumentID = "children"
	if _, _, err := Apply("account-a", s, foreign, m, at); !errors.Is(err, ErrInvalid) {
		t.Fatal("cross document", err)
	}
	for _, kind := range []string{"renameBookmark", "deleteBookmark", "reanchorBookmark"} {
		bad := bookmarkOperation(kind, 0, map[string]any{"bookmarkId": bookmarkID})
		if _, _, err := Apply("account-a", s, target, bad, at); !errors.Is(err, ErrInvalid) {
			t.Fatal("missing CAS", kind)
		}
	}
	m.BaseVersion = new(int64(1))
	if _, _, err := Apply("account-a", s, target, m, at); !errors.Is(err, ErrInvalid) {
		t.Fatal("create with CAS")
	}
	for _, p := range []map[string]any{{"bookmarkId": bookmarkID, "name": strings.Repeat("😀", 80), "sentenceId": "a", "offset": 1, "pageId": "page-a"}, {"bookmarkId": bookmarkID, "name": "Title", "sentenceId": "canonical-title", "offset": 4, "pageId": "page-b"}} {
		if _, r, err := Apply("account-a", s, target, bookmarkOperation("createBookmark", 0, p), at); err != nil || r.Status != "applied" {
			t.Fatal(r, err)
		}
	}
	m = bookmarkCreate(bookmarkID)
	m.Payload = json.RawMessage(`{"bookmarkId":"49f10a14-688e-4726-b46f-51b68f950128","name":"missing offset","sentenceId":"a","pageId":"page-a"}`)
	if _, _, err := Apply("account-a", s, target, m, at); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing zero offset accepted")
	}
}
func TestBookmarkLimitDuplicatesAndRevisionReanchor(t *testing.T) {
	s := NewState("account-a", "document-a", 3)
	target := bookmarkSnapshot()
	for range MaxBookmarks {
		var err error
		s, _, err = Apply("account-a", s, target, bookmarkCreate(uuid.NewString()), at)
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, r, err := Apply("account-a", s, target, bookmarkCreate(bookmarkID), at); err != nil || r.Status != "bookmark_limit" {
		t.Fatal(r, err)
	}
	s.Notes = []Note{{ID: "untouched", Text: "private", SentenceIDs: []string{"a"}, InactiveAnchors: []Anchor{}}}
	target.Revision = 4
	mappings := []onlinebulletins.SentenceMapping{{FromSentenceID: "a", ToSentenceIDs: []string{"a"}, Status: "unchanged"}, {FromSentenceID: "canonical-title", ToSentenceIDs: []string{"canonical-title"}, Status: "unchanged"}}
	before := clone(s)
	next, err := Migrate("account-a", s, target, mappings, at.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(s, before) || !next.Bookmarks[0].ReanchorRequired || next.Bookmarks[0].Version != 2 || next.Bookmarks[0].DocumentRevision != 3 {
		t.Fatal("bad migration")
	}
	target.Revision = 5
	next, err = Migrate("account-a", next, target, mappings, at.Add(2*time.Minute))
	if err != nil || next.Bookmarks[0].Version != 2 {
		t.Fatal("incremented invalid bookmark", err)
	}
	b := next.Bookmarks[0]
	m := bookmarkOperation("reanchorBookmark", 2, map[string]any{"bookmarkId": b.ID, "sentenceId": "a", "offset": 3, "pageId": "page-b"})
	m.DocumentRevision = 5
	next, r, err := Apply("account-a", next, target, m, at.Add(2*time.Minute))
	if err != nil || r.Bookmark.ReanchorRequired || r.Bookmark.DocumentRevision != 5 || r.Bookmark.Version != 3 {
		t.Fatal(next, r, err)
	}
}

func TestBookmarkUUIDSpellingsUseOneIdentity(t *testing.T) {
	s := NewState("account-a", "document-a", 3)
	target := bookmarkSnapshot()
	m := bookmarkCreate(strings.ToUpper(bookmarkID))
	next, r, err := Apply("account-a", s, target, m, at)
	if err != nil || r.Bookmark.ID != bookmarkID {
		t.Fatal(r, err)
	}
	m = bookmarkOperation("renameBookmark", 1, map[string]any{"bookmarkId": strings.ToUpper(bookmarkID), "name": "updated"})
	next, r, err = Apply("account-a", next, target, m, at)
	if err != nil || r.Status != "applied" || r.Bookmark.Version != 2 {
		t.Fatal(r, err)
	}
	_, r, err = Apply("account-a", next, target, bookmarkCreate("{"+bookmarkID+"}"), at)
	if err != nil || r.Status != "bookmark_conflict" {
		t.Fatal("alternate spelling recreated", r, err)
	}
}
