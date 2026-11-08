package bulletinreader

import (
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

const MaxBookmarks = 100
const MaxBookmarkName = 80

func validBookmarkName(name string) bool {
	return utf8.ValidString(name) && name != "" && utf8.RuneCountInString(name) <= MaxBookmarkName
}

func validBookmarkTarget(snapshot Snapshot, sentence, page string, offset *int) bool {
	if offset == nil || *offset < 0 || !slices.Contains(snapshot.Pages, page) {
		return false
	}
	position := slices.IndexFunc(snapshot.Sentences, func(a Anchor) bool { return a.ID == sentence })
	if position < 0 || *offset >= utf8.RuneCountInString(snapshot.Sentences[position].Text) {
		return false
	}
	return slices.ContainsFunc(snapshot.SentenceFragments[sentence], func(f SourceFragment) bool {
		return f.PageID == page && f.Start <= *offset && *offset < f.End
	})
}

func applyBookmark(state, next State, snapshot Snapshot, mutation Mutation, result Result, now time.Time) (State, Result, error) {
	var id, name, sentence, page string
	var offset *int
	switch mutation.Kind {
	case "createBookmark":
		var p struct {
			ID       string `json:"bookmarkId"`
			Name     string `json:"name"`
			Sentence string `json:"sentenceId"`
			Offset   *int   `json:"offset"`
			Page     string `json:"pageId"`
		}
		if mutation.BaseVersion != nil || decode(mutation.Payload, &p) != nil {
			return State{}, Result{}, ErrInvalid
		}
		id, name, sentence, offset, page = p.ID, strings.TrimSpace(p.Name), p.Sentence, p.Offset, p.Page
	case "renameBookmark":
		var p struct {
			ID   string `json:"bookmarkId"`
			Name string `json:"name"`
		}
		if mutation.BaseVersion == nil || decode(mutation.Payload, &p) != nil {
			return State{}, Result{}, ErrInvalid
		}
		id, name = p.ID, strings.TrimSpace(p.Name)
	case "deleteBookmark":
		var p struct {
			ID string `json:"bookmarkId"`
		}
		if mutation.BaseVersion == nil || decode(mutation.Payload, &p) != nil {
			return State{}, Result{}, ErrInvalid
		}
		id = p.ID
	case "reanchorBookmark":
		var p struct {
			ID       string `json:"bookmarkId"`
			Sentence string `json:"sentenceId"`
			Offset   *int   `json:"offset"`
			Page     string `json:"pageId"`
		}
		if mutation.BaseVersion == nil || decode(mutation.Payload, &p) != nil {
			return State{}, Result{}, ErrInvalid
		}
		id, sentence, offset, page = p.ID, p.Sentence, p.Offset, p.Page
	}
	if uuid.Validate(id) != nil || ((mutation.Kind == "createBookmark" || mutation.Kind == "renameBookmark") && !validBookmarkName(name)) ||
		((mutation.Kind == "createBookmark" || mutation.Kind == "reanchorBookmark") && !validBookmarkTarget(snapshot, sentence, page, offset)) {
		return State{}, Result{}, ErrInvalid
	}
	// PostgreSQL UUID output is canonical; keep lookup identity stable across valid spellings.
	id = uuid.MustParse(id).String()
	position := slices.IndexFunc(next.Bookmarks, func(b Bookmark) bool { return b.ID == id })
	conflict := mutation.Kind == "createBookmark" && position >= 0 || mutation.Kind != "createBookmark" &&
		(position < 0 || next.Bookmarks[position].Deleted || next.Bookmarks[position].Version != *mutation.BaseVersion)
	if conflict {
		result.Status = "bookmark_conflict"
		if position >= 0 {
			b := next.Bookmarks[position]
			result.Bookmark = &b
		}
		return state, result, nil
	}
	if mutation.Kind == "createBookmark" {
		active := 0
		for _, b := range next.Bookmarks {
			if !b.Deleted {
				active++
			}
		}
		if active >= MaxBookmarks {
			result.Status = "bookmark_limit"
			return state, result, nil
		}
		next.Bookmarks = append(next.Bookmarks, Bookmark{ID: id, Name: name, SentenceID: sentence, Offset: *offset, PageID: page, DocumentRevision: snapshot.Revision, Version: 1, CreatedAt: now, UpdatedAt: now})
		position = len(next.Bookmarks) - 1
	} else {
		b := &next.Bookmarks[position]
		b.Version++
		b.UpdatedAt = now
		switch mutation.Kind {
		case "renameBookmark":
			b.Name = name
		case "reanchorBookmark":
			b.SentenceID, b.Offset, b.PageID, b.DocumentRevision, b.ReanchorRequired = sentence, *offset, page, snapshot.Revision, false
		case "deleteBookmark":
			b.Name, b.SentenceID, b.PageID, b.Offset, b.Deleted, b.ReanchorRequired = "", "", "", 0, true, false
		}
	}
	b := next.Bookmarks[position]
	result.Bookmark = &b
	return next, result, nil
}
