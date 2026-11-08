package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"time"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinreader"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func readerPublication(ctx context.Context, tx *sql.Tx, document string, revision int64) (onlinebulletins.Document, bulletinreader.Snapshot, error) {
	var doc onlinebulletins.Document
	var metadata onlinebulletins.CanonicalMetadata
	var raw, canonical []byte
	snapshot := bulletinreader.Snapshot{DocumentID: document, Revision: revision}
	err := tx.QueryRowContext(ctx, `SELECT r.document_json,p.canonical_metadata FROM hhc_web.online_bulletin_publication p JOIN hhc_web.online_bulletin_revision r ON r.document_id=p.document_id AND r.revision=p.revision WHERE p.document_id=$1 AND p.revision=$2`, document, revision).Scan(&raw, &canonical)
	if err == sql.ErrNoRows {
		return doc, snapshot, bulletinreader.ErrMapping
	}
	if err != nil {
		return doc, snapshot, err
	}
	if json.Unmarshal(raw, &doc) != nil || json.Unmarshal(canonical, &metadata) != nil {
		return doc, snapshot, bulletinreader.ErrMapping
	}
	if _, err = onlinebulletins.ValidateDocument(doc); err != nil {
		return doc, snapshot, bulletinreader.ErrMapping
	}
	for _, page := range doc.Pages {
		snapshot.Pages = append(snapshot.Pages, page.ID)
	}
	snapshot.Components = []string{"canonical"}
	for _, component := range doc.Components {
		snapshot.Components = append(snapshot.Components, component.ID)
	}
	for _, a := range onlinebulletins.ReaderAnchors(doc, metadata) {
		snapshot.Sentences = append(snapshot.Sentences, bulletinreader.Anchor{ID: a.ID, Text: a.Text, ComponentID: a.ComponentID, PageID: a.PageID})
	}
	snapshot.SentencePages = map[string][]string{}
	snapshot.SentenceFragments = map[string][]bulletinreader.SourceFragment{}
	for _, page := range doc.LayoutManifest.Pages {
		for _, slot := range page.Slots {
			for _, fragment := range slot.Fragments {
				snapshot.SentencePages[fragment.SentenceID] = append(snapshot.SentencePages[fragment.SentenceID], page.PageID)
				snapshot.SentenceFragments[fragment.SentenceID] = append(snapshot.SentenceFragments[fragment.SentenceID], bulletinreader.SourceFragment{PageID: page.PageID, Start: fragment.Start, End: fragment.End})
			}
		}
		for _, slot := range page.FixedSlots {
			id := "canonical-" + slot.Element
			snapshot.SentencePages[id] = append(snapshot.SentencePages[id], page.PageID)
			for _, a := range snapshot.Sentences {
				if a.ID == id {
					snapshot.SentenceFragments[id] = append(snapshot.SentenceFragments[id], bulletinreader.SourceFragment{PageID: page.PageID, End: utf8.RuneCountInString(a.Text)})
					break
				}
			}
		}
	}
	return doc, snapshot, nil
}

// Walk all retained published checkpoints, regardless of intervening draft count.
// Documents are loaded one at a time; missing or partial checkpoints fail closed.
func migrateReaderState(ctx context.Context, tx *sql.Tx, state bulletinreader.State, requested *int64, now time.Time) (bulletinreader.State, bulletinreader.Snapshot, []bulletinreader.DirectMapping, error) {
	fail := func(err error) (bulletinreader.State, bulletinreader.Snapshot, []bulletinreader.DirectMapping, error) {
		return state, bulletinreader.Snapshot{}, nil, err
	}
	start := state.AppliedRevision
	if requested != nil {
		start = min(start, *requested)
	}
	if state.AppliedRevision > state.CurrentRevision || requested != nil && *requested > state.CurrentRevision {
		return fail(bulletinreader.ErrMapping)
	}
	previous, snapshot, err := readerPublication(ctx, tx, state.DocumentID, start)
	if err != nil {
		return fail(err)
	}
	var direct []bulletinreader.DirectMapping
	initDirect := func() {
		direct = []bulletinreader.DirectMapping{}
		for _, a := range snapshot.Sentences {
			direct = append(direct, bulletinreader.DirectMapping{FromSentenceID: a.ID, ToSentenceIDs: []string{a.ID}})
		}
	}
	if requested != nil && *requested == start {
		initDirect()
	}
	rows, err := tx.QueryContext(ctx, `SELECT revision FROM hhc_web.online_bulletin_publication WHERE document_id=$1 AND revision>$2 AND revision<=$3 ORDER BY revision`, state.DocumentID, start, state.CurrentRevision)
	if err != nil {
		return fail(err)
	}
	revisions := []int64{}
	for rows.Next() {
		var revision int64
		if err = rows.Scan(&revision); err != nil {
			rows.Close()
			return fail(err)
		}
		revisions = append(revisions, revision)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return fail(err)
	}
	if err = rows.Close(); err != nil {
		return fail(err)
	}
	for _, revision := range revisions {
		next, target, err := readerPublication(ctx, tx, state.DocumentID, revision)
		if err != nil {
			return fail(err)
		}
		// Match PublicationMappings' Go string ordering, regardless of database locale.
		rows, err = tx.QueryContext(ctx, `SELECT from_sentence_id,status,to_sentence_ids FROM hhc_web.online_bulletin_mapping WHERE document_id=$1 AND from_revision=$2 AND to_revision=$3 ORDER BY from_sentence_id COLLATE "C"`, state.DocumentID, snapshot.Revision, revision)
		if err != nil {
			return fail(err)
		}
		mappings := []onlinebulletins.SentenceMapping{}
		for rows.Next() {
			var mapping onlinebulletins.SentenceMapping
			var ids []byte
			if err = rows.Scan(&mapping.FromSentenceID, &mapping.Status, &ids); err != nil {
				rows.Close()
				return fail(err)
			}
			if json.Unmarshal(ids, &mapping.ToSentenceIDs) != nil {
				rows.Close()
				return fail(bulletinreader.ErrMapping)
			}
			mappings = append(mappings, mapping)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			return fail(err)
		}
		if err = rows.Close(); err != nil {
			return fail(err)
		}
		// Recompute with the stored explicit decisions, then require the checkpoint
		// to cover every old sentence (including canonical metadata anchors).
		decisions := []onlinebulletins.SentenceMapping{}
		for _, m := range mappings {
			if m.Status == "split" || m.Status == "merged" || m.Status == "removed" && !isCanonicalAnchor(m.FromSentenceID) {
				decisions = append(decisions, m)
			}
		}
		expected, err := onlinebulletins.PublicationMappings(previous, next, decisions)
		if err != nil || len(expected) != len(mappings) {
			return fail(bulletinreader.ErrMapping)
		}
		for i := range expected {
			if expected[i].FromSentenceID != mappings[i].FromSentenceID || expected[i].Status != mappings[i].Status || !slices.Equal(expected[i].ToSentenceIDs, mappings[i].ToSentenceIDs) {
				return fail(bulletinreader.ErrMapping)
			}
		}
		if state.AppliedRevision == snapshot.Revision {
			state, err = bulletinreader.MigrateFromSnapshot(state.OwnerID, state, snapshot, target, mappings, now)
			if err != nil {
				return fail(err)
			}
		}
		if direct != nil {
			index := map[string][]string{}
			for _, m := range mappings {
				index[m.FromSentenceID] = m.ToSentenceIDs
			}
			for i := range direct {
				wanted := map[string]bool{}
				for _, id := range direct[i].ToSentenceIDs {
					targets, ok := index[id]
					if !ok {
						return fail(bulletinreader.ErrMapping)
					}
					for _, target := range targets {
						wanted[target] = true
					}
				}
				ids := []string{}
				for _, a := range target.Sentences {
					if wanted[a.ID] {
						ids = append(ids, a.ID)
					}
				}
				direct[i].ToSentenceIDs = ids
			}
		}
		previous, snapshot = next, target
		if requested != nil && *requested == revision {
			initDirect()
		}
	}
	if snapshot.Revision != state.CurrentRevision || state.AppliedRevision != state.CurrentRevision || requested != nil && direct == nil {
		return fail(bulletinreader.ErrMapping)
	}
	// Bound output by bytes, never by an arbitrary number of publication steps.
	if raw, err := json.Marshal(direct); err != nil || len(raw) > onlinebulletins.MaxDocumentBytes {
		return fail(bulletinreader.ErrMapping)
	}
	return bulletinreader.PreserveHighlightHistory(state), snapshot, direct, nil
}

func isCanonicalAnchor(id string) bool { return len(id) >= 10 && id[:10] == "canonical-" }
