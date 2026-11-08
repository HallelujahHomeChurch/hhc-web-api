package postgres

import (
	"context"
	"database/sql"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/google/uuid"
)

func (r *Repository) GetStatementDismissal(ctx context.Context, owner string, ref content.StatementRef) (content.StatementDismissal, error) {
	return r.statementDismissal(ctx, owner, ref, false)
}
func (r *Repository) DismissStatement(ctx context.Context, owner string, ref content.StatementRef) (content.StatementDismissal, error) {
	return r.statementDismissal(ctx, owner, ref, true)
}
func (r *Repository) statementDismissal(ctx context.Context, owner string, ref content.StatementRef, dismiss bool) (content.StatementDismissal, error) {
	result := content.StatementDismissal{StatementID: ref.StatementID, PublishedVersion: ref.PublishedVersion}
	if _, err := uuid.Parse(owner); err != nil || !ref.Valid() {
		return result, content.ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if err = lockWatermarkSubject(ctx, tx, owner); err != nil {
		return result, err
	}
	var restricted bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.bulletin_watermark_subject WHERE subject_hash=$1) OR EXISTS(SELECT 1 FROM hhc_web.account_cleanup_operation WHERE subject_ref=$2)`, watermarkSubjectHash(owner), watermarkCleanupSubjectRef(owner)).Scan(&restricted); err != nil {
		return result, err
	}
	if restricted {
		return result, content.ErrStatementDismissalRestricted
	}
	// Publication and deletion lock this same entry before replacing its projections.
	var version int64
	var entryID string
	err = tx.QueryRowContext(ctx, `SELECT id::text FROM hhc_web.content_entry WHERE id=$1 AND module='news' FOR SHARE`, ref.StatementID).Scan(&entryID)
	if errors.Is(err, sql.ErrNoRows) {
		return result, content.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	err = tx.QueryRowContext(ctx, `SELECT version FROM hhc_web.public_projection WHERE resource_id=$1 AND resource_type='news' AND locale='zh-Hant' AND payload_json->>'kind'='statement'`, ref.StatementID).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		return result, content.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	if version != ref.PublishedVersion {
		return result, content.ErrStatementVersionChanged
	}
	if dismiss {
		if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.statement_dismissal(account_id,statement_id,published_version) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, owner, ref.StatementID, ref.PublishedVersion); err != nil {
			return result, err
		}
		result.Dismissed = true
	} else {
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.statement_dismissal WHERE account_id=$1 AND statement_id=$2 AND published_version=$3)`, owner, ref.StatementID, ref.PublishedVersion).Scan(&result.Dismissed)
		if err != nil {
			return result, err
		}
	}
	return result, tx.Commit()
}
