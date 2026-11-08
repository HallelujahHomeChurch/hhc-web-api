package bulletinreader

import (
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

// MigrateFromSnapshot preserves a bookmark automatically only when the retained
// publication proves its text offset and exactly one destination page fragment.
func MigrateFromSnapshot(owner string, state State, previous, target Snapshot, mappings []onlinebulletins.SentenceMapping, now time.Time) (State, error) {
	if previous.DocumentID != state.DocumentID || previous.Revision != state.AppliedRevision {
		return State{}, ErrMapping
	}
	next, err := Migrate(owner, state, target, mappings, now)
	if err != nil || target.Revision == state.AppliedRevision {
		return next, err
	}
	before, after := map[string]Anchor{}, map[string]Anchor{}
	for _, anchor := range previous.Sentences {
		before[anchor.ID] = anchor
	}
	for _, anchor := range target.Sentences {
		after[anchor.ID] = anchor
	}
	index := map[string][]string{}
	for _, mapping := range mappings {
		index[mapping.FromSentenceID] = mapping.ToSentenceIDs
	}
	for i, original := range state.Bookmarks {
		if original.Deleted || original.ReanchorRequired || original.DocumentRevision != previous.Revision {
			continue
		}
		ids, exists := index[original.SentenceID]
		if !exists {
			return State{}, ErrMapping
		}
		source, exists := before[original.SentenceID]
		if !exists || len(ids) != 1 || !validBookmarkTarget(previous, original.SentenceID, original.PageID, &original.Offset) {
			continue
		}
		destination := after[ids[0]]
		start, _, proven := onlinebulletins.ReanchorWhitespaceRange(source.Text, destination.Text, original.Offset, original.Offset+1)
		if !proven {
			continue
		}
		page, matches := "", 0
		for _, fragment := range target.SentenceFragments[destination.ID] {
			if fragment.Start <= start && start < fragment.End {
				page, matches = fragment.PageID, matches+1
			}
		}
		if matches != 1 || !validBookmarkTarget(target, destination.ID, page, &start) {
			continue
		}
		bookmark := &next.Bookmarks[i]
		bookmark.SentenceID, bookmark.Offset, bookmark.PageID = destination.ID, start, page
		bookmark.DocumentRevision, bookmark.ReanchorRequired = target.Revision, false
	}
	return next, nil
}
