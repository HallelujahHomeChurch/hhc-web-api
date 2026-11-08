package postgres

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinreader"
)

func loadReaderBookmarks(ctx context.Context, tx *sql.Tx, state *bulletinreader.State) error {
	rows, err := tx.QueryContext(ctx, `SELECT id::text,name,sentence_id,scalar_offset,page_id,document_revision,version,deleted,reanchor_required,created_at,updated_at FROM hhc_web.bulletin_reader_bookmark WHERE account_id=$1 AND document_id=$2 ORDER BY created_at,id`, state.OwnerID, state.DocumentID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var b bulletinreader.Bookmark
		if err = rows.Scan(&b.ID, &b.Name, &b.SentenceID, &b.Offset, &b.PageID, &b.DocumentRevision, &b.Version, &b.Deleted, &b.ReanchorRequired, &b.CreatedAt, &b.UpdatedAt); err != nil {
			return err
		}
		b.CreatedAt, b.UpdatedAt = b.CreatedAt.UTC(), b.UpdatedAt.UTC()
		state.Bookmarks = append(state.Bookmarks, b)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	return rows.Close()
}

func saveReaderBookmarks(ctx context.Context, tx *sql.Tx, state bulletinreader.State) error {
	raw, err := json.Marshal(state.Bookmarks)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_reader_bookmark(account_id,document_id,id,name,sentence_id,scalar_offset,page_id,document_revision,version,deleted,reanchor_required,created_at,updated_at)
 SELECT $1,$2,id,name,"sentenceId","offset","pageId","documentRevision",version,deleted,"reanchorRequired","createdAt","updatedAt" FROM jsonb_to_recordset($3::jsonb) AS b(id uuid,name text,"sentenceId" text,"offset" integer,"pageId" text,"documentRevision" bigint,version bigint,deleted boolean,"reanchorRequired" boolean,"createdAt" timestamptz,"updatedAt" timestamptz)
 ON CONFLICT(account_id,document_id,id) DO UPDATE SET name=EXCLUDED.name,sentence_id=EXCLUDED.sentence_id,scalar_offset=EXCLUDED.scalar_offset,page_id=EXCLUDED.page_id,document_revision=EXCLUDED.document_revision,version=EXCLUDED.version,deleted=EXCLUDED.deleted,reanchor_required=EXCLUDED.reanchor_required,updated_at=EXCLUDED.updated_at
 WHERE bulletin_reader_bookmark.version<>EXCLUDED.version`, state.OwnerID, state.DocumentID, raw)
	return err
}
