package bulletinreader

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// Preserve retained pre-history evidence without reviving consumed entries.
func PreserveHighlightHistory(state State) State {
	next := clone(state)
	for _, highlight := range state.Highlights {
		archiveHighlight(&next, highlight)
	}
	for _, conflict := range state.Conflicts {
		for _, source := range conflict.Sources {
			source.Active = false
			archiveHighlight(&next, source)
		}
	}
	slices.SortFunc(next.HighlightHistory, func(a, b HighlightHistoryEntry) int { return strings.Compare(a.ID, b.ID) })
	return next
}

// History is private evidence, not an active selection on the current wording.
func highlightHistoryID(state *State, highlight Highlight) string {
	identity, _ := json.Marshal([]any{state.OwnerID, state.DocumentID, highlight.SentenceID, highlight.Version, highlight.Quote, highlight.Color, highlight.Segments})
	return uuid.NewSHA1(uuid.NameSpaceOID, identity).String()
}

func archiveHighlight(state *State, highlight Highlight) {
	if highlight.Active || highlight.SentenceID == "" {
		return
	}
	id := highlightHistoryID(state, highlight)
	if slices.ContainsFunc(state.HighlightHistory, func(entry HighlightHistoryEntry) bool { return entry.ID == id }) {
		return
	}
	highlight.Segments = slices.Clone(highlight.Segments)
	state.HighlightHistory = append(state.HighlightHistory, HighlightHistoryEntry{ID: id, Highlight: &highlight})
}

func forgetHistoricalHighlight(state *State, id string) {
	// Keep an opaque tombstone so later migration cannot recreate discarded evidence.
	for i := range state.HighlightHistory {
		if state.HighlightHistory[i].ID == id {
			state.HighlightHistory[i].Highlight = nil
		}
	}
	state.Highlights = slices.DeleteFunc(state.Highlights, func(h Highlight) bool { return !h.Active && highlightHistoryID(state, h) == id })
	for i := range state.Conflicts {
		state.Conflicts[i].Sources = slices.DeleteFunc(state.Conflicts[i].Sources, func(h Highlight) bool { return highlightHistoryID(state, h) == id })
	}
	state.Conflicts = slices.DeleteFunc(state.Conflicts, func(conflict MigrationConflict) bool { return len(conflict.Sources) == 0 })
}
