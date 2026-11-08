package bulletinreader

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/google/uuid"
)

// Migrate consumes a direct mapping composed only from published checkpoints.
// It never guesses from similar text or counts intervening draft saves.
func Migrate(owner string, state State, target Snapshot, mappings []onlinebulletins.SentenceMapping, now time.Time) (State, error) {
	if owner == "" || state.OwnerID != owner || state.DocumentID != target.DocumentID || target.Revision < state.AppliedRevision {
		return State{}, ErrMapping
	}
	if target.Revision == state.AppliedRevision {
		return state, nil
	}
	anchors := make(map[string]Anchor, len(target.Sentences))
	for _, anchor := range target.Sentences {
		if anchor.ID == "" {
			return State{}, ErrMapping
		}
		if _, exists := anchors[anchor.ID]; exists {
			return State{}, ErrMapping
		}
		anchors[anchor.ID] = anchor
	}
	index := make(map[string][]string, len(mappings))
	for _, mapping := range mappings {
		if mapping.FromSentenceID == "" || mapping.Status == "requires_review" {
			return State{}, ErrMapping
		}
		if _, duplicate := index[mapping.FromSentenceID]; duplicate {
			return State{}, ErrMapping
		}
		seen := map[string]bool{}
		for _, id := range mapping.ToSentenceIDs {
			if _, exists := anchors[id]; !exists || seen[id] {
				return State{}, ErrMapping
			}
			seen[id] = true
		}
		if !slices.Contains([]string{"unchanged", "moved", "split", "merged", "removed"}, mapping.Status) || (mapping.Status == "removed") != (len(mapping.ToSentenceIDs) == 0) {
			return State{}, ErrMapping
		}
		switch mapping.Status {
		case "unchanged", "moved":
			if len(mapping.ToSentenceIDs) != 1 || mapping.ToSentenceIDs[0] != mapping.FromSentenceID {
				return State{}, ErrMapping
			}
		case "split":
			if len(mapping.ToSentenceIDs) < 2 {
				return State{}, ErrMapping
			}
		case "merged":
			if len(mapping.ToSentenceIDs) != 1 {
				return State{}, ErrMapping
			}
		}
		index[mapping.FromSentenceID] = slices.Clone(mapping.ToSentenceIDs)
	}
	next := clone(state)
	for i := range next.Bookmarks {
		b := &next.Bookmarks[i]
		if !b.Deleted && !b.ReanchorRequired {
			b.ReanchorRequired = true
			b.Version++
			b.UpdatedAt = now
		}
	}
	groups := map[string][]Highlight{}
	kept := map[string]Highlight{}
	for _, highlight := range state.Highlights {
		if !highlight.Active {
			archiveHighlight(&next, highlight)
			kept[highlight.SentenceID] = highlight
			continue
		}
		ids, exists := index[highlight.SentenceID]
		if !exists {
			return State{}, ErrMapping
		}
		if len(highlight.Segments) > 0 {
			archiveHighlight(&next, highlight)
			highlight.Active = len(ids) == 1 && ids[0] == highlight.SentenceID
			segments := slices.Clone(highlight.Segments)
			if highlight.Active {
				for i, segment := range segments {
					start, end, ok := onlinebulletins.ReanchorWhitespaceRange(highlight.Quote, anchors[ids[0]].Text, segment.Start, segment.End)
					if !ok {
						highlight.Active = false
						break
					}
					segments[i].Start, segments[i].End = start, end
				}
			}
			if highlight.Active {
				highlight.Segments, highlight.Quote = segments, anchors[ids[0]].Text
			}
			highlight.Version++
			highlight.UpdatedAt = now
			archiveHighlight(&next, highlight)
			kept[highlight.SentenceID] = highlight
			continue
		}
		if len(ids) == 0 {
			highlight.Active = false
			highlight.Version++
			highlight.UpdatedAt = now
			archiveHighlight(&next, highlight)
			kept[highlight.SentenceID] = highlight
		}
		for _, id := range ids {
			groups[id] = append(groups[id], highlight)
		}
	}
	for _, conflict := range state.Conflicts {
		for _, from := range conflict.SentenceIDs {
			ids, exists := index[from]
			if !exists {
				return State{}, ErrMapping
			}
			for _, id := range ids {
				groups[id] = append(groups[id], conflict.Sources...)
			}
			if len(ids) == 0 {
				for _, source := range conflict.Sources {
					source.Active = false
					kept[source.SentenceID] = source
				}
			}
		}
	}
	next.Conflicts = []MigrationConflict{}
	for _, anchor := range target.Sentences {
		sources := groups[anchor.ID]
		if len(sources) == 0 {
			continue
		}
		if existing := kept[anchor.ID]; len(existing.Segments) > 0 {
			sources = append(sources, existing)
		}
		colors := map[string]bool{}
		var version int64
		unique := map[string]Highlight{}
		for _, source := range sources {
			colors[source.Color] = true
			if len(source.Segments) > 0 {
				colors["partial-range-requires-choice"] = true
			}
			version = max(version, source.Version)
			unique[source.SentenceID+"/"+source.Color] = source
		}
		if len(colors) == 1 {
			kept[anchor.ID] = Highlight{SentenceID: anchor.ID, Color: sources[0].Color, Quote: anchor.Text, Active: true, Version: version + 1, UpdatedAt: now}
			continue
		}
		sources = make([]Highlight, 0, len(unique))
		for _, source := range unique {
			source.Active = false
			sources = append(sources, source)
			if !kept[source.SentenceID].Active {
				kept[source.SentenceID] = source
			}
		}
		slices.SortFunc(sources, func(a, b Highlight) int {
			if a.SentenceID == b.SentenceID {
				return strings.Compare(a.Color, b.Color)
			}
			return strings.Compare(a.SentenceID, b.SentenceID)
		})
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(owner+"\x00"+target.DocumentID+"\x00"+strconv.FormatInt(target.Revision, 10)+"\x00"+anchor.ID)).String()
		next.Conflicts = append(next.Conflicts, MigrationConflict{ID: id, SentenceIDs: []string{anchor.ID}, Sources: sources})
	}
	next.Highlights = make([]Highlight, 0, len(kept))
	for _, highlight := range kept {
		archiveHighlight(&next, highlight)
		next.Highlights = append(next.Highlights, highlight)
	}
	slices.SortFunc(next.Highlights, func(a, b Highlight) int { return strings.Compare(a.SentenceID, b.SentenceID) })
	slices.SortFunc(next.HighlightHistory, func(a, b HighlightHistoryEntry) int { return strings.Compare(a.ID, b.ID) })
	for i := range next.Notes {
		note := &next.Notes[i]
		if note.Deleted {
			continue
		}
		if len(note.Ranges) > 0 {
			if note.ReanchorRequired {
				continue
			}
			verified := true
			ranges := slices.Clone(note.Ranges)
			changed := false
			for i, selected := range note.Ranges {
				ids, exists := index[selected.SentenceID]
				if !exists {
					return State{}, ErrMapping
				}
				if len(ids) != 1 || ids[0] != selected.SentenceID {
					verified = false
					continue
				}
				start, end, ok := onlinebulletins.ReanchorWhitespaceRange(selected.Quote, anchors[ids[0]].Text, selected.Start, selected.End)
				if !ok {
					verified = false
					continue
				}
				ranges[i].Start, ranges[i].End, ranges[i].Quote = start, end, anchors[ids[0]].Text
				changed = changed || ranges[i] != selected
			}
			if verified && changed {
				note.Ranges = ranges
				note.Version++
				note.UpdatedAt = now
			}
			if !verified {
				for _, selected := range note.Ranges {
					note.InactiveAnchors = append(note.InactiveAnchors, Anchor{ID: selected.SentenceID, Text: selected.Quote})
				}
				note.SentenceIDs = []string{}
				note.ReanchorRequired = true
				note.Version++
				note.UpdatedAt = now
			}
			continue
		}
		wanted := map[string]bool{}
		for _, from := range note.SentenceIDs {
			ids, exists := index[from]
			if !exists {
				return State{}, ErrMapping
			}
			if len(ids) == 0 {
				note.InactiveAnchors = append(note.InactiveAnchors, Anchor{ID: from})
			}
			for _, id := range ids {
				wanted[id] = true
			}
		}
		ids := []string{}
		for _, anchor := range target.Sentences {
			if wanted[anchor.ID] {
				ids = append(ids, anchor.ID)
			}
		}
		if len(ids) > MaxAnchors {
			// Preserve the complete private quote/text, but never silently truncate
			// an oversized anchor set or make every other note inaccessible.
			note.ReanchorRequired = true
			for _, id := range note.SentenceIDs {
				if !slices.ContainsFunc(note.InactiveAnchors, func(anchor Anchor) bool { return anchor.ID == id }) {
					note.InactiveAnchors = append(note.InactiveAnchors, Anchor{ID: id})
				}
			}
			ids = []string{}
		}
		if !slices.Equal(note.SentenceIDs, ids) {
			note.Version++
			note.UpdatedAt = now
		}
		note.SentenceIDs = ids
	}
	if progress := next.Progress; progress != nil {
		if progress.SentenceID != "" {
			ids, exists := index[progress.SentenceID]
			if !exists {
				return State{}, ErrMapping
			}
			progress.SentenceID, progress.ComponentID, progress.PageID = "", "", ""
			if len(ids) > 0 {
				anchor := anchors[ids[0]]
				progress.SentenceID, progress.ComponentID, progress.PageID = anchor.ID, anchor.ComponentID, anchor.PageID
			}
		} else {
			if !slices.Contains(target.Pages, progress.PageID) {
				progress.PageID = ""
			}
			if !slices.Contains(target.Components, progress.ComponentID) {
				progress.ComponentID = ""
			}
		}
	}
	next.AppliedRevision, next.CurrentRevision = target.Revision, target.Revision
	return next, nil
}
