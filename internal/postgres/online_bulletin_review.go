package postgres

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func (r *Repository) ConfirmOnlineBulletin(ctx context.Context, issueID, series, locale string, input onlinebulletins.ConfirmInput, actor string, now time.Time) (onlinebulletins.ConfirmedRevision, error) {
	if err := r.validateConfirmOnlineBulletinInput(ctx, input, actor, now); err != nil {
		return onlinebulletins.ConfirmedRevision{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return onlinebulletins.ConfirmedRevision{}, err
	}
	defer tx.Rollback()
	result, err := r.confirmOnlineBulletinTx(ctx, tx, issueID, series, locale, input, actor, now, nil)
	if err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func (r *Repository) confirmOnlineBulletinTx(ctx context.Context, tx *sql.Tx, issueID, series, locale string, input onlinebulletins.ConfirmInput, actor string, now time.Time, metadataOverride *onlinebulletins.CanonicalMetadata) (onlinebulletins.ConfirmedRevision, error) {
	var result onlinebulletins.ConfirmedRevision
	if err := r.validateConfirmOnlineBulletinInput(ctx, input, actor, now); err != nil {
		return result, err
	}

	var canonical onlinebulletins.CanonicalMetadata
	var canonicalVersion int64
	var proof string
	var documentJSON, issuesJSON []byte
	err := tx.QueryRowContext(ctx, `SELECT d.id::text,d.version,d.local_revision,i.version,v.title,v.subtitle,i.issue_number,i.issue_date::text,r.document_json,r.review_issues,COALESCE(r.layout_validation_hash,'')
	 FROM hhc_web.online_bulletin_document d JOIN hhc_web.bulletin_issue i ON i.id=d.issue_id
	 JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series=d.series AND v.locale=d.content_locale
	 JOIN hhc_web.online_bulletin_revision r ON r.document_id=d.id AND r.revision=d.local_revision
	 WHERE d.issue_id=$1 AND d.series=$2 AND d.content_locale=$3 FOR UPDATE OF i,v,d`, issueID, series, locale).Scan(&result.DocumentID, &result.Version, &result.Revision, &canonicalVersion, &canonical.Title, &canonical.Subtitle, &canonical.IssueNumber, &canonical.Date, &documentJSON, &issuesJSON, &proof)
	if errors.Is(err, sql.ErrNoRows) {
		return result, bulletins.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	if canonicalVersion != input.CanonicalVersion || result.Version != input.OnlineVersion || result.Revision != input.Revision {
		return result, bulletins.ErrPrecondition
	}
	var document onlinebulletins.Document
	var issues []onlinebulletins.ReviewIssue
	if json.Unmarshal(documentJSON, &document) != nil || json.Unmarshal(issuesJSON, &issues) != nil {
		return result, bulletins.ErrConflict
	}
	validation, err := onlinebulletins.ValidateDocument(document)
	if err != nil || len(validation) != 0 || proof == "" || proof != input.LayoutValidationHash || proof != document.LayoutManifest.LayoutValidationHash || input.PageCount != len(document.Pages) || onlinebulletins.ValidateReview(issues, input.AcceptedIssues) != nil {
		return result, bulletins.ErrConflict
	}
	if metadataOverride != nil {
		canonical = *metadataOverride
	}
	canonicalJSON, err := json.Marshal(canonical)
	if err != nil {
		return result, err
	}
	// Evidence must originate from our immutable worker result for this exact
	// Local and metadata. A browser-provided hash alone has no authority.
	var measured bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.online_bulletin_layout_job WHERE document_id=$1 AND result_revision=$2 AND status='ready' AND canonical_metadata=$3::jsonb AND measurement_json->>'layoutValidationHash'=$4 AND renderer_artifact_sha256=$5)`, result.DocumentID, result.Revision, canonicalJSON, proof, document.LayoutManifest.RendererArtifactSha256).Scan(&measured); err != nil {
		return result, err
	}
	if !measured {
		return result, bulletins.ErrConflict
	}
	accepted, err := json.Marshal(input.AcceptedIssues)
	if err != nil {
		return result, err
	}
	insert, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_confirmation(document_id,revision,canonical_metadata,layout_validation_hash,page_count,accepted_issues,created_by,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(document_id,revision) DO NOTHING`, result.DocumentID, result.Revision, canonicalJSON, proof, input.PageCount, accepted, actor, now)
	if err != nil {
		return result, err
	}
	count, err := insert.RowsAffected()
	if err != nil {
		return result, err
	}
	if count != 1 {
		return result, bulletins.ErrConflict
	}
	var extractor string
	if err = tx.QueryRowContext(ctx, `SELECT extractor_version FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=$2`, result.DocumentID, result.Revision).Scan(&extractor); err != nil {
		return result, err
	}
	if series == "general" && locale == "zh-Hant" && canonical.IssueNumber >= 1739 && extractor != onlinebulletins.RecognitionExtractorVersion() {
		if err = enqueueConfirmedLearning(ctx, tx, result, now); err != nil {
			return result, err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_document SET version=version+1,draft_status='ready',updated_at=$2 WHERE id=$1`, result.DocumentID, now); err != nil {
		return result, err
	}
	if err = r.enqueueBulletinAudit(ctx, tx, "cms.bulletin.online.review", result.DocumentID, actor, now); err != nil {
		return result, err
	}
	result.Version++
	return result, nil
}

func (r *Repository) validateConfirmOnlineBulletinInput(ctx context.Context, input onlinebulletins.ConfirmInput, actor string, now time.Time) error {
	hash, err := hex.DecodeString(input.LayoutValidationHash)
	if err != nil || len(hash) != 32 || strings.ToLower(input.LayoutValidationHash) != input.LayoutValidationHash || input.CanonicalVersion < 1 || input.OnlineVersion < 1 || input.Revision < 1 || input.PageCount < 1 || input.PageCount > 80 || input.AcceptedIssues == nil || len(input.AcceptedIssues) > 4096 || actor == "" || now.IsZero() {
		return bulletins.ErrInvalid
	}
	if err = r.validateBulletinAudit(ctx, actor); err != nil {
		return err
	}

	return nil
}
