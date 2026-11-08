package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func (r *Repository) OnlineBulletinComparison(ctx context.Context, issueID, series, locale string) (onlinebulletins.Comparison, error) {
	var result onlinebulletins.Comparison
	var base, local, incoming []byte
	// One MVCC snapshot binds every side to the version shown to the editor.
	err := r.db.QueryRowContext(ctx, `SELECT d.id::text,d.version,i.version,d.base_revision,d.local_revision,d.incoming_revision,b.document_json,l.document_json,n.document_json
	 FROM hhc_web.online_bulletin_document d JOIN hhc_web.bulletin_issue i ON i.id=d.issue_id
	 JOIN hhc_web.online_bulletin_revision b ON b.document_id=d.id AND b.revision=d.base_revision
	 JOIN hhc_web.online_bulletin_revision l ON l.document_id=d.id AND l.revision=d.local_revision
	 JOIN hhc_web.online_bulletin_revision n ON n.document_id=d.id AND n.revision=d.incoming_revision
	 WHERE d.issue_id=$1 AND d.series=$2 AND d.content_locale=$3`, issueID, series, locale).Scan(&result.DocumentID, &result.Version, &result.CanonicalVersion, &result.BaseRevision, &result.LocalRevision, &result.IncomingRevision, &base, &local, &incoming)
	if errors.Is(err, sql.ErrNoRows) {
		return result, bulletins.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	var b, l, n onlinebulletins.Document
	if json.Unmarshal(base, &b) != nil || json.Unmarshal(local, &l) != nil || json.Unmarshal(incoming, &n) != nil {
		return result, bulletins.ErrConflict
	}
	result.Units, err = onlinebulletins.CompareDocuments(b, l, n)
	return result, err
}
