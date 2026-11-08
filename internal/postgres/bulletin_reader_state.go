package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinreader"
)

// The caller first acquires the existing subject/DSR fence and publication lock.
// Locking the parent serializes all private changes for this account/document.
func loadReaderState(ctx context.Context, tx *sql.Tx, owner, document string, current int64, now time.Time) (bulletinreader.State, error) {
	state := bulletinreader.NewState(owner, document, current)
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_reader_migration_state(account_id,document_id,applied_revision,updated_at) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, owner, document, current, now); err != nil {
		return state, err
	}
	var conflicts, history []byte
	if err := tx.QueryRowContext(ctx, `SELECT applied_revision,conflicts,highlight_history FROM hhc_web.bulletin_reader_migration_state WHERE account_id=$1 AND document_id=$2 FOR UPDATE`, owner, document).Scan(&state.AppliedRevision, &conflicts, &history); err != nil {
		return state, err
	}
	if err := json.Unmarshal(conflicts, &state.Conflicts); err != nil {
		return state, err
	}
	if err := json.Unmarshal(history, &state.HighlightHistory); err != nil {
		return state, err
	}
	if len(state.HighlightHistory) == 0 {
		state.HighlightHistory = nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT sentence_id,color,quote,active,version,updated_at,segments FROM hhc_web.bulletin_reader_highlight WHERE account_id=$1 AND document_id=$2 ORDER BY sentence_id`, owner, document)
	if err != nil {
		return state, err
	}
	for rows.Next() {
		var h bulletinreader.Highlight
		var segments []byte
		if err = rows.Scan(&h.SentenceID, &h.Color, &h.Quote, &h.Active, &h.Version, &h.UpdatedAt, &segments); err != nil {
			rows.Close()
			return state, err
		}
		if len(segments) > 0 {
			if err = json.Unmarshal(segments, &h.Segments); err != nil {
				rows.Close()
				return state, err
			}
		}
		h.UpdatedAt = h.UpdatedAt.UTC()
		state.Highlights = append(state.Highlights, h)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return state, err
	}
	if err = rows.Close(); err != nil {
		return state, err
	}
	rows, err = tx.QueryContext(ctx, `SELECT id::text,body,sentence_ids,inactive_anchors,quote,version,deleted,reanchor_required,created_at,updated_at,ranges FROM hhc_web.bulletin_reader_note WHERE account_id=$1 AND document_id=$2 ORDER BY created_at,id`, owner, document)
	if err != nil {
		return state, err
	}
	for rows.Next() {
		var n bulletinreader.Note
		var ids, inactive, ranges []byte
		if err = rows.Scan(&n.ID, &n.Text, &ids, &inactive, &n.Quote, &n.Version, &n.Deleted, &n.ReanchorRequired, &n.CreatedAt, &n.UpdatedAt, &ranges); err != nil {
			rows.Close()
			return state, err
		}
		if len(ranges) > 0 {
			if err = json.Unmarshal(ranges, &n.Ranges); err != nil {
				rows.Close()
				return state, err
			}
		}
		if err = json.Unmarshal(ids, &n.SentenceIDs); err != nil {
			rows.Close()
			return state, err
		}
		if err = json.Unmarshal(inactive, &n.InactiveAnchors); err != nil {
			rows.Close()
			return state, err
		}
		n.CreatedAt, n.UpdatedAt = n.CreatedAt.UTC(), n.UpdatedAt.UTC()
		state.Notes = append(state.Notes, n)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return state, err
	}
	if err = rows.Close(); err != nil {
		return state, err
	}
	if err = loadReaderBookmarks(ctx, tx, &state); err != nil {
		return state, err
	}
	var progress bulletinreader.Progress
	err = tx.QueryRowContext(ctx, `SELECT page_id,component_id,sentence_id,recorded_at,updated_at FROM hhc_web.bulletin_reader_progress WHERE account_id=$1 AND document_id=$2`, owner, document).Scan(&progress.PageID, &progress.ComponentID, &progress.SentenceID, &progress.RecordedAt, &progress.UpdatedAt)
	if err == nil {
		progress.RecordedAt, progress.UpdatedAt = progress.RecordedAt.UTC(), progress.UpdatedAt.UTC()
		state.Progress = &progress
	} else if !errors.Is(err, sql.ErrNoRows) {
		return state, err
	}
	return state, nil
}

func saveReaderState(ctx context.Context, tx *sql.Tx, state bulletinreader.State, now time.Time) error {
	conflicts, err := json.Marshal(state.Conflicts)
	if err != nil {
		return err
	}
	history, err := json.Marshal(append([]bulletinreader.HighlightHistoryEntry{}, state.HighlightHistory...))
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_reader_migration_state SET applied_revision=$3,conflicts=$4,updated_at=$5,highlight_history=$6 WHERE account_id=$1 AND document_id=$2 AND applied_revision<=$3`, state.OwnerID, state.DocumentID, state.AppliedRevision, conflicts, now, history)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected != 1 {
		return bulletinreader.ErrMapping
	}
	highlights, err := json.Marshal(state.Highlights)
	if err != nil {
		return err
	}
	// One set-based write, with unchanged rows left untouched. Clear only removes
	// absent highlights; notes use explicit tombstones and are never pruned here.
	if _, err = tx.ExecContext(ctx, `DELETE FROM hhc_web.bulletin_reader_highlight WHERE account_id=$1 AND document_id=$2 AND sentence_id NOT IN (SELECT "sentenceId" FROM jsonb_to_recordset($3::jsonb) AS h("sentenceId" text))`, state.OwnerID, state.DocumentID, highlights); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_reader_highlight(account_id,document_id,sentence_id,color,quote,active,version,updated_at,segments)
 SELECT $1,$2,"sentenceId",color,quote,active,version,"updatedAt",segments FROM jsonb_to_recordset($3::jsonb) AS h("sentenceId" text,color text,quote text,active boolean,version bigint,"updatedAt" timestamptz,segments jsonb)
 ON CONFLICT(account_id,document_id,sentence_id) DO UPDATE SET color=EXCLUDED.color,quote=EXCLUDED.quote,active=EXCLUDED.active,version=EXCLUDED.version,updated_at=EXCLUDED.updated_at,segments=EXCLUDED.segments
 WHERE (bulletin_reader_highlight.color,bulletin_reader_highlight.quote,bulletin_reader_highlight.active,bulletin_reader_highlight.version,bulletin_reader_highlight.updated_at,bulletin_reader_highlight.segments) IS DISTINCT FROM (EXCLUDED.color,EXCLUDED.quote,EXCLUDED.active,EXCLUDED.version,EXCLUDED.updated_at,EXCLUDED.segments)`, state.OwnerID, state.DocumentID, highlights); err != nil {
		return err
	}
	notes, err := json.Marshal(state.Notes)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_reader_note(account_id,document_id,id,body,sentence_ids,inactive_anchors,quote,version,deleted,reanchor_required,created_at,updated_at,ranges)
 SELECT $1,$2,id,text,"sentenceIds","inactiveAnchors",quote,version,deleted,"reanchorRequired","createdAt","updatedAt",ranges FROM jsonb_to_recordset($3::jsonb) AS n(id uuid,text text,"sentenceIds" jsonb,"inactiveAnchors" jsonb,quote text,version bigint,deleted boolean,"reanchorRequired" boolean,"createdAt" timestamptz,"updatedAt" timestamptz,ranges jsonb)
 ON CONFLICT(account_id,document_id,id) DO UPDATE SET body=EXCLUDED.body,sentence_ids=EXCLUDED.sentence_ids,inactive_anchors=EXCLUDED.inactive_anchors,quote=EXCLUDED.quote,version=EXCLUDED.version,deleted=EXCLUDED.deleted,reanchor_required=EXCLUDED.reanchor_required,updated_at=EXCLUDED.updated_at,ranges=EXCLUDED.ranges
 WHERE bulletin_reader_note.version<>EXCLUDED.version`, state.OwnerID, state.DocumentID, notes); err != nil {
		return err
	}
	if err = saveReaderBookmarks(ctx, tx, state); err != nil {
		return err
	}
	if p := state.Progress; p != nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_reader_progress(account_id,document_id,page_id,component_id,sentence_id,recorded_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7)
 ON CONFLICT(account_id,document_id) DO UPDATE SET page_id=EXCLUDED.page_id,component_id=EXCLUDED.component_id,sentence_id=EXCLUDED.sentence_id,recorded_at=EXCLUDED.recorded_at,updated_at=EXCLUDED.updated_at`, state.OwnerID, state.DocumentID, p.PageID, p.ComponentID, p.SentenceID, p.RecordedAt, p.UpdatedAt)
	}
	return err
}
