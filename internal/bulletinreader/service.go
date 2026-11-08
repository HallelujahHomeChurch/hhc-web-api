package bulletinreader

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

func decode(payload []byte, target any) error {
	if !utf8.Valid(payload) || len(bytes.TrimSpace(payload)) == 0 || bytes.TrimSpace(payload)[0] != '{' {
		return ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(target) != nil {
		return ErrInvalid
	}
	if decoder.Decode(new(any)) != io.EOF {
		return ErrInvalid
	}
	return nil
}

func clone(state State) State {
	// Failed action groups must not mutate the caller's transaction snapshot.
	next := state
	next.Bookmarks = append([]Bookmark{}, state.Bookmarks...)
	next.HighlightHistory = slices.Clone(state.HighlightHistory)
	for i := range next.HighlightHistory {
		if original := state.HighlightHistory[i].Highlight; original != nil {
			copy := *original
			copy.Segments = slices.Clone(original.Segments)
			next.HighlightHistory[i].Highlight = &copy
		}
	}
	next.Highlights = slices.Clone(state.Highlights)
	for i := range next.Highlights {
		next.Highlights[i].Segments = slices.Clone(state.Highlights[i].Segments)
	}
	next.Notes = slices.Clone(state.Notes)
	for i := range next.Notes {
		next.Notes[i].Ranges = slices.Clone(state.Notes[i].Ranges)
		next.Notes[i].SentenceIDs = slices.Clone(state.Notes[i].SentenceIDs)
		next.Notes[i].InactiveAnchors = slices.Clone(state.Notes[i].InactiveAnchors)
	}
	next.Conflicts = slices.Clone(state.Conflicts)
	for i := range next.Conflicts {
		next.Conflicts[i].SentenceIDs = slices.Clone(state.Conflicts[i].SentenceIDs)
		next.Conflicts[i].Sources = slices.Clone(state.Conflicts[i].Sources)
	}
	if state.Progress != nil {
		progress := *state.Progress
		next.Progress = &progress
	}
	return next
}

func validColor(color string) bool { return color == "yellow" || color == "red" || color == "blue" }
func validNote(text string) bool {
	return utf8.ValidString(text) && strings.TrimSpace(text) != "" && utf8.RuneCountInString(text) <= MaxNoteLength
}

func orderedAnchors(snapshot Snapshot, requested []string) ([]Anchor, error) {
	if len(requested) == 0 || len(requested) > MaxAnchors {
		return nil, ErrInvalid
	}
	wanted := make(map[string]bool, len(requested))
	for _, id := range requested {
		if wanted[id] {
			return nil, ErrInvalid
		}
		wanted[id] = true
	}
	anchors := make([]Anchor, 0, len(requested))
	for _, anchor := range snapshot.Sentences {
		if wanted[anchor.ID] {
			anchors = append(anchors, anchor)
			delete(wanted, anchor.ID)
		}
	}
	if len(wanted) != 0 {
		return nil, ErrInvalid
	}
	return anchors, nil
}

// Apply is pure. The repository holds the subject/document locks, verifies the
// current publication and persists state plus terminal dedupe in one transaction.
// owner is trusted identity, never a field accepted from a mutation payload.
func Apply(owner string, state State, snapshot Snapshot, mutation Mutation, now time.Time) (State, Result, error) {
	result := Result{MutationID: mutation.MutationID, Status: "applied", Revision: snapshot.Revision}
	invalid := func() (State, Result, error) { return State{}, Result{}, ErrInvalid }
	if owner == "" || state.OwnerID != owner || state.DocumentID != snapshot.DocumentID || state.AppliedRevision != snapshot.Revision || snapshot.Revision < 1 ||
		mutation.DocumentRevision < 1 || mutation.DocumentRevision > 9007199254740991 ||
		uuid.Validate(mutation.MutationID) != nil || mutation.CreatedAt.IsZero() || now.IsZero() || mutation.CreatedAt.After(now.Add(5*time.Minute)) ||
		mutation.BaseVersion != nil && *mutation.BaseVersion < 1 {
		return invalid()
	}
	if mutation.DocumentRevision != snapshot.Revision {
		result.Status = "revision_changed"
		return state, result, nil
	}
	if mutation.CreatedAt.Before(now.Add(-ReplayWindow)) {
		result.Status = "recovery_required"
		return state, result, nil
	}
	next := clone(state)
	switch mutation.Kind {
	case "createBookmark", "renameBookmark", "deleteBookmark", "reanchorBookmark":
		return applyBookmark(state, next, snapshot, mutation, result, now)

	case "restoreHighlight", "discardHighlightHistory":
		var payload struct {
			HistoryID   string          `json:"historyId"`
			SentenceIDs []string        `json:"sentenceIds"`
			Color       string          `json:"color"`
			Ranges      json.RawMessage `json:"ranges"`
		}
		if mutation.BaseVersion != nil || decode(mutation.Payload, &payload) != nil || uuid.Validate(payload.HistoryID) != nil {
			return invalid()
		}
		if mutation.Kind == "discardHighlightHistory" {
			var discard struct {
				HistoryID string `json:"historyId"`
			}
			if decode(mutation.Payload, &discard) != nil {
				return invalid()
			}
		}
		position := slices.IndexFunc(next.HighlightHistory, func(entry HighlightHistoryEntry) bool { return entry.ID == payload.HistoryID && entry.Highlight != nil })
		if position < 0 {
			result.Status = "conflict_not_found"
			return state, result, nil
		}
		if mutation.Kind == "restoreHighlight" {
			if len(payload.Ranges) == 0 || string(payload.Ranges) == "null" {
				return invalid()
			}
			replacement := mutation
			replacement.Kind = "setHighlight"
			replacement.Payload, _ = json.Marshal(struct {
				SentenceIDs []string        `json:"sentenceIds"`
				Color       string          `json:"color"`
				Ranges      json.RawMessage `json:"ranges"`
			}{payload.SentenceIDs, payload.Color, payload.Ranges})
			var err error
			next, result, err = Apply(owner, next, snapshot, replacement, now)
			if err != nil {
				return invalid()
			}
			if result.Status != "applied" {
				return state, result, nil
			}
		}
		forgetHistoricalHighlight(&next, payload.HistoryID)
	case "setHighlight", "clearHighlight":
		var payload struct {
			SentenceIDs []string        `json:"sentenceIds"`
			Color       string          `json:"color"`
			Ranges      json.RawMessage `json:"ranges"`
		}
		if mutation.BaseVersion != nil {
			return invalid()
		}
		if mutation.Kind == "clearHighlight" {
			var clear struct {
				SentenceIDs []string        `json:"sentenceIds"`
				Ranges      json.RawMessage `json:"ranges"`
			}
			if decode(mutation.Payload, &clear) != nil {
				return invalid()
			}
			payload.SentenceIDs = clear.SentenceIDs
			payload.Ranges = clear.Ranges
		} else if decode(mutation.Payload, &payload) != nil || !validColor(payload.Color) {
			return invalid()
		}
		anchors, err := orderedAnchors(snapshot, payload.SentenceIDs)
		if err != nil {
			return invalid()
		}
		ranges, err := requestedRanges(snapshot, anchors, payload.Ranges)
		if err != nil {
			return invalid()
		}
		for _, anchor := range anchors {
			if slices.ContainsFunc(next.Conflicts, func(conflict MigrationConflict) bool { return slices.Contains(conflict.SentenceIDs, anchor.ID) }) {
				result.Status = "migration_conflict"
				return state, result, nil
			}
		}
		colors := make(map[string]Highlight, len(next.Highlights)+len(anchors))
		for _, highlight := range next.Highlights {
			colors[highlight.SentenceID] = highlight
		}
		for i, anchor := range anchors {
			archiveHighlight(&next, colors[anchor.ID])
			if ranges != nil {
				previous := colors[anchor.ID]
				segments, err := replaceSegments(previous, ranges[i], payload.Color)
				if err != nil {
					return invalid()
				}
				if len(segments) == 0 {
					delete(colors, anchor.ID)
					continue
				}
				colors[anchor.ID] = Highlight{SentenceID: anchor.ID, Color: segments[0].Color, Segments: segments, Quote: anchor.Text, Active: true, Version: previous.Version + 1, UpdatedAt: now}
				continue
			}
			if mutation.Kind == "clearHighlight" {
				delete(colors, anchor.ID)
				continue
			}
			previous := colors[anchor.ID]
			if previous.Active && len(previous.Segments) == 0 && previous.Color == payload.Color {
				continue
			}
			colors[anchor.ID] = Highlight{SentenceID: anchor.ID, Color: payload.Color, Quote: anchor.Text, Active: true, Version: previous.Version + 1, UpdatedAt: now}
		}
		next.Highlights = make([]Highlight, 0, len(colors))
		for _, highlight := range colors {
			next.Highlights = append(next.Highlights, highlight)
		}
		slices.SortFunc(next.Highlights, func(a, b Highlight) int { return strings.Compare(a.SentenceID, b.SentenceID) })
	case "createNote":
		var payload struct {
			NoteID      string          `json:"noteId"`
			SentenceIDs []string        `json:"sentenceIds"`
			Text        string          `json:"text"`
			Ranges      json.RawMessage `json:"ranges"`
		}
		if mutation.BaseVersion != nil || decode(mutation.Payload, &payload) != nil || uuid.Validate(payload.NoteID) != nil || !validNote(payload.Text) {
			return invalid()
		}
		anchors, err := orderedAnchors(snapshot, payload.SentenceIDs)
		if err != nil {
			return invalid()
		}
		ranges, err := requestedRanges(snapshot, anchors, payload.Ranges)
		if err != nil {
			return invalid()
		}
		if i := slices.IndexFunc(next.Notes, func(note Note) bool { return note.ID == payload.NoteID }); i >= 0 {
			result.Status = "note_conflict"
			result.Note = &next.Notes[i]
			return state, result, nil
		}
		note := Note{ID: payload.NoteID, Text: payload.Text, SentenceIDs: []string{}, InactiveAnchors: []Anchor{}, Version: 1, CreatedAt: now, UpdatedAt: now}
		for i, anchor := range anchors {
			note.SentenceIDs = append(note.SentenceIDs, anchor.ID)
			if i > 0 && anchor.ComponentID != anchors[i-1].ComponentID {
				note.Quote += "\n\n"
			}
			if ranges == nil {
				note.Quote += anchor.Text
			} else {
				note.Ranges = append(note.Ranges, NoteRange{TextRange: ranges[i], Quote: anchor.Text})
				note.Quote += string([]rune(anchor.Text)[ranges[i].Start:ranges[i].End])
			}
		}
		next.Notes = append(next.Notes, note)
		result.Note = &note
	case "editNote", "deleteNote", "reanchorNote":
		var payload struct {
			NoteID string `json:"noteId"`
			Text   string `json:"text"`
		}
		if mutation.BaseVersion == nil {
			return invalid()
		}
		var anchors []Anchor
		var ranges []TextRange
		if mutation.Kind == "reanchorNote" {
			var selected struct {
				NoteID      string          `json:"noteId"`
				SentenceIDs []string        `json:"sentenceIds"`
				Ranges      json.RawMessage `json:"ranges"`
			}
			if decode(mutation.Payload, &selected) != nil || len(selected.Ranges) == 0 {
				return invalid()
			}
			var err error
			anchors, err = orderedAnchors(snapshot, selected.SentenceIDs)
			if err != nil {
				return invalid()
			}
			ranges, err = requestedRanges(snapshot, anchors, selected.Ranges)
			if err != nil {
				return invalid()
			}
			payload.NoteID = selected.NoteID
		} else if mutation.Kind == "deleteNote" {
			var deleted struct {
				NoteID string `json:"noteId"`
			}
			if decode(mutation.Payload, &deleted) != nil {
				return invalid()
			}
			payload.NoteID = deleted.NoteID
		} else if decode(mutation.Payload, &payload) != nil || !validNote(payload.Text) {
			return invalid()
		}
		if uuid.Validate(payload.NoteID) != nil {
			return invalid()
		}
		i := slices.IndexFunc(next.Notes, func(note Note) bool { return note.ID == payload.NoteID })
		if i < 0 {
			result.Status = "note_conflict"
			return state, result, nil
		}
		note := &next.Notes[i]
		if note.Deleted || note.Version != *mutation.BaseVersion {
			result.Status = "note_conflict"
			result.Note = note
			return state, result, nil
		}
		if mutation.Kind == "reanchorNote" && !note.ReanchorRequired && len(note.InactiveAnchors) == 0 {
			return invalid()
		}
		note.Version++
		note.UpdatedAt = now
		if mutation.Kind == "deleteNote" {
			note.Deleted = true
			note.Text = ""
			note.Quote = ""
			note.SentenceIDs = []string{}
			note.InactiveAnchors = []Anchor{}
			note.Ranges = nil
		} else if mutation.Kind == "reanchorNote" {
			// Keep the original private quote and text; only the selected location changes.
			note.SentenceIDs, note.InactiveAnchors, note.Ranges = []string{}, []Anchor{}, nil
			for i, anchor := range anchors {
				note.SentenceIDs = append(note.SentenceIDs, anchor.ID)
				note.Ranges = append(note.Ranges, NoteRange{TextRange: ranges[i], Quote: anchor.Text})
			}
			note.ReanchorRequired = false
		} else {
			note.Text = payload.Text
		}
		result.Note = note
	case "setProgress":
		var payload struct {
			PageID      string `json:"pageId"`
			ComponentID string `json:"componentId"`
			SentenceID  string `json:"sentenceId"`
		}
		if mutation.BaseVersion != nil || decode(mutation.Payload, &payload) != nil {
			return invalid()
		}
		if payload.PageID != "" && !slices.Contains(snapshot.Pages, payload.PageID) || payload.ComponentID != "" && !slices.Contains(snapshot.Components, payload.ComponentID) {
			return invalid()
		}
		if payload.SentenceID != "" {
			anchors, err := orderedAnchors(snapshot, []string{payload.SentenceID})
			if err != nil || payload.PageID != "" && anchors[0].PageID != payload.PageID && !slices.Contains(snapshot.SentencePages[payload.SentenceID], payload.PageID) || payload.ComponentID != "" && anchors[0].ComponentID != payload.ComponentID {
				return invalid()
			}
		}
		if next.Progress == nil || !mutation.CreatedAt.Before(next.Progress.RecordedAt) {
			next.Progress = &Progress{PageID: payload.PageID, ComponentID: payload.ComponentID, SentenceID: payload.SentenceID, RecordedAt: mutation.CreatedAt, UpdatedAt: now}
		}
	case "resolveHighlightMigrationConflict":
		var payload struct {
			ConflictID      string `json:"conflictId"`
			ChosenColor     string `json:"chosenColor"`
			CurrentRevision int64  `json:"currentRevision"`
		}
		if mutation.BaseVersion != nil || decode(mutation.Payload, &payload) != nil || !validColor(payload.ChosenColor) || payload.CurrentRevision != snapshot.Revision {
			return invalid()
		}
		i := slices.IndexFunc(next.Conflicts, func(conflict MigrationConflict) bool { return conflict.ID == payload.ConflictID })
		if i < 0 {
			result.Status = "conflict_not_found"
			return state, result, nil
		}
		anchors, err := orderedAnchors(snapshot, next.Conflicts[i].SentenceIDs)
		if err != nil {
			return invalid()
		}
		next.Conflicts = slices.Delete(next.Conflicts, i, i+1)
		for _, anchor := range anchors {
			position := slices.IndexFunc(next.Highlights, func(highlight Highlight) bool { return highlight.SentenceID == anchor.ID })
			highlight := Highlight{SentenceID: anchor.ID, Color: payload.ChosenColor, Quote: anchor.Text, Active: true, Version: 1, UpdatedAt: now}
			if position >= 0 {
				highlight.Version = next.Highlights[position].Version + 1
				next.Highlights[position] = highlight
			} else {
				next.Highlights = append(next.Highlights, highlight)
			}
		}
	default:
		return invalid()
	}
	return next, result, nil
}
