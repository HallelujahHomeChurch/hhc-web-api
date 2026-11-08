package postgres

import (
	"context"
	"database/sql"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
)

func lookupReaderTrace(ctx context.Context, tx *sql.Tx, code, subject string, now time.Time) (bulletinwatermark.Receipt, error) {
	v := bulletinwatermark.Receipt{Channel: "online_reader"}
	err := tx.QueryRowContext(ctx, `SELECT r.id::text,r.trace_code,r.account_id::text,d.issue_id::text,d.series,d.content_locale,r.revision,v.renderer_version,r.privacy_notice_version,r.issued_at,r.expires_at FROM hhc_web.online_bulletin_reader_receipt r JOIN hhc_web.online_bulletin_document d ON d.id=r.document_id JOIN hhc_web.online_bulletin_revision v ON v.document_id=r.document_id AND v.revision=r.revision WHERE r.trace_code=$1 AND r.account_id=NULLIF($2,'')::uuid AND r.expires_at>$3 FOR SHARE OF r`, code, subject, now).Scan(&v.ID, &v.Code, &v.UserID, &v.IssueID, &v.Series, &v.Locale, &v.Revision, &v.RendererVersion, &v.PrivacyVersion, &v.IssuedAt, &v.ExpiresAt)
	return v, err
}
