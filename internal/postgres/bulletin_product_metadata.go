package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func (r *Repository) stageBulletinMetadata(ctx context.Context, tx *sql.Tx, issue string, version int64, input bulletins.PublicationMetadata, actor string, now time.Time) error {
	var previous onlinebulletins.CanonicalMetadata
	var documentID, status, asset string
	var published, local sql.NullInt64
	var documentVersion int64
	var automatic, busy, pdfCurrent, hasOnlinePublication bool
	err := tx.QueryRowContext(ctx, `
 SELECT COALESCE(i.issue_number,0),i.issue_date::text,v.title,v.subtitle,v.auto_publish,v.pdf_asset_id,v.status,
 COALESCE(d.id::text,''),COALESCE(d.version,0),d.published_revision,d.local_revision,
 EXISTS(SELECT 1 FROM hhc_web.bulletin_version v2 WHERE v2.issue_id=i.id AND v2.status IN('publishing','unpublishing','unpublish_failed')),
 EXISTS(SELECT 1 FROM hhc_web.bulletin_published_version p WHERE p.issue_id=i.id AND p.series=v.series AND p.locale=v.locale AND p.asset_id=v.pdf_asset_id),
 EXISTS(SELECT 1 FROM hhc_web.online_bulletin_document active WHERE active.issue_id=i.id AND active.published_revision IS NOT NULL)
 FROM hhc_web.bulletin_issue i JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series='general' AND v.locale='zh-Hant'
 LEFT JOIN hhc_web.online_bulletin_document d ON d.issue_id=i.id AND d.series=v.series AND d.content_locale=v.locale
 WHERE i.id=$1 FOR UPDATE OF i,v`, issue).Scan(&previous.IssueNumber, &previous.Date, &previous.Title, &previous.Subtitle, &automatic, &asset, &status, &documentID, &documentVersion, &published, &local, &busy, &pdfCurrent, &hasOnlinePublication)
	if err != nil {
		return err
	}
	if busy || (input.IssueNumber < 1739 && (hasOnlinePublication || automatic)) {
		return bulletins.ErrConflict
	}
	metadata := onlinebulletins.CanonicalMetadata{IssueNumber: input.IssueNumber, Date: input.IssueDate, Title: input.Title, Subtitle: input.Subtitle}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(1172101,$1::integer)`, metadata.IssueNumber); err != nil {
		return err
	}
	var collision bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.bulletin_issue i WHERE i.issue_number=$1 AND i.id<>$2) OR EXISTS(SELECT 1 FROM hhc_web.bulletin_issue_number_alias a WHERE a.issue_number=$1 AND a.issue_id<>$2)
 OR EXISTS(SELECT 1 FROM hhc_web.bulletin_metadata_update c
  JOIN hhc_web.bulletin_issue i ON i.id=c.issue_id AND i.version=c.issue_version
  JOIN hhc_web.online_bulletin_layout_job l ON l.id=c.layout_job_id AND l.status IN('queued','validating','failed','ready')
  JOIN hhc_web.online_bulletin_document d ON d.id=l.document_id AND d.version=c.online_version
  WHERE c.issue_number=$1 AND c.issue_id<>$2)`, metadata.IssueNumber, issue).Scan(&collision); err != nil {
		return err
	}
	if collision {
		return bulletins.ErrConflict
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_layout_job l SET status='superseded',lease_token=NULL,lease_until=NULL,updated_at=$2 FROM hhc_web.bulletin_metadata_update c WHERE c.issue_id=$1 AND c.layout_job_id=l.id AND l.status IN('queued','validating')`, issue, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM hhc_web.bulletin_metadata_update WHERE issue_id=$1`, issue); err != nil {
		return err
	}

	if metadata == previous {
		return nil
	}

	if published.Valid && pdfCurrent && status == "published" {
		if !r.automaticBulletinExtraction {
			return bulletins.ErrOnlinePublicationUnavailable
		}
		if local != published || automatic {
			return bulletins.ErrConflict
		}
		var raw []byte
		err = tx.QueryRowContext(ctx, `SELECT r.document_json FROM hhc_web.online_bulletin_revision r
 JOIN hhc_web.online_bulletin_document d ON d.id=r.document_id
 WHERE r.document_id=$1 AND r.revision=$2 AND d.version=$3 AND d.incoming_revision IS NULL FOR UPDATE OF d`, documentID, published.Int64, documentVersion).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			return bulletins.ErrConflict
		}
		if err != nil {
			return err
		}
		var document onlinebulletins.Document
		if json.Unmarshal(raw, &document) != nil {
			return bulletins.ErrConflict
		}
		layoutID, err := queueOnlineLayout(ctx, tx, documentID, published.Int64, document, metadata, now)
		if err != nil {
			return err
		}
		if err = productAffected(tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET version=version+1,updated_by=$3,updated_at=$4 WHERE id=$1 AND version=$2`, issue, version, actor, now)); err != nil {
			return err
		}
		provenance, _ := auditclient.ProvenanceFromContext(ctx)
		_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_metadata_update(issue_id,issue_version,online_version,issue_number,layout_job_id,metadata_json,actor_id,request_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, issue, version+1, documentVersion+1, metadata.IssueNumber, layoutID, mustJSON(input), actor, provenance.RequestID)
		return err
	}

	// An unverified replacement has its own draft metadata. Keep an existing
	// published PDF/online snapshot intact until its normal publication succeeds.
	if err = r.applyBulletinMetadata(ctx, tx, issue, version, previous.IssueNumber, metadata, actor, now); err != nil {
		return err
	}
	if !automatic {
		return nil
	}
	if err = r.setAutomaticBulletinIntentReason(ctx, tx, issue, "general", "zh-Hant", true, false, actor, now, "canonical_edited"); err != nil {
		return err
	}
	next, err := loadIssue(ctx, tx, issue)
	if err != nil {
		return err
	}
	return enqueueUploadExtraction(ctx, tx, next, asset, true, false, actor, now)
}

func (r *Repository) applyBulletinMetadata(ctx context.Context, tx *sql.Tx, issue string, version int64, oldNumber int, metadata onlinebulletins.CanonicalMetadata, actor string, now time.Time) error {
	if oldNumber != metadata.IssueNumber && oldNumber > 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_issue_number_alias(issue_number,issue_id)
 SELECT $2,i.id FROM hhc_web.bulletin_issue i
 WHERE i.id=$1 AND (i.published_at IS NOT NULL OR EXISTS(SELECT 1 FROM hhc_web.bulletin_published_version WHERE issue_id=i.id) OR EXISTS(SELECT 1 FROM hhc_web.online_bulletin_publication p JOIN hhc_web.online_bulletin_document d ON d.id=p.document_id WHERE d.issue_id=i.id))
 ON CONFLICT(issue_number) DO NOTHING`, issue, oldNumber); err != nil {
			return mapConflict(err)
		}
	}
	if err := productAffected(tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET issue_number=$3,issue_date=$4,version=version+1,updated_by=$5,updated_at=$6 WHERE id=$1 AND version=$2`, issue, version, metadata.IssueNumber, metadata.Date, actor, now)); err != nil {
		return mapConflict(err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version SET title=$2,subtitle=$3,version=version+1,updated_by=$4,updated_at=$5 WHERE issue_id=$1 AND series='general' AND locale='zh-Hant'`, issue, metadata.Title, metadata.Subtitle, actor, now); err != nil {
		return err
	}
	// Refresh only projections backed by the same live asset. An old PDF
	// fallback for a replacement keeps its frozen metadata and download name.
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_published_version p SET issue_number=$2,issue_date=$3,title=v.title,subtitle=v.subtitle
 FROM hhc_web.bulletin_version v WHERE p.issue_id=$1 AND (p.issue_id,p.series,p.locale)=(v.issue_id,v.series,v.locale) AND p.asset_id=v.pdf_asset_id AND v.status='published'`, issue, metadata.IssueNumber, metadata.Date); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_document d SET metadata_sync_pending=(p.canonical_metadata->>'issueNumber')::int<>$2 OR p.canonical_metadata->>'date'<>$3 OR (d.series='general' AND d.content_locale='zh-Hant' AND (p.canonical_metadata->>'title'<>$4 OR p.canonical_metadata->>'subtitle'<>$5))
 FROM hhc_web.online_bulletin_publication p WHERE d.issue_id=$1 AND p.document_id=d.id AND p.revision=d.published_revision`, issue, metadata.IssueNumber, metadata.Date, metadata.Title, metadata.Subtitle); err != nil {
		return err
	}
	updated, err := loadIssue(ctx, tx, issue)
	if err != nil {
		return err
	}
	return insertBulletinRevision(ctx, tx, updated, actor, now)
}

type bulletinMetadataJob struct {
	issue          string
	version        int64
	actor, request string
}

func loadBulletinMetadataJob(ctx context.Context, tx *sql.Tx, job onlinebulletins.LayoutJob) (*bulletinMetadataJob, bool, error) {
	var correction bulletinMetadataJob
	var valid bool
	err := tx.QueryRowContext(ctx, `SELECT i.id::text,c.issue_version,c.actor_id,c.request_id,
 i.version=c.issue_version AND d.version=c.online_version AND d.local_revision=$3 AND d.published_revision=$3 AND d.incoming_revision IS NULL
 AND v.status='published' AND EXISTS(SELECT 1 FROM hhc_web.bulletin_published_version p WHERE (p.issue_id,p.series,p.locale,p.asset_id)=(v.issue_id,v.series,v.locale,v.pdf_asset_id))
 FROM hhc_web.bulletin_metadata_update c JOIN hhc_web.bulletin_issue i ON i.id=c.issue_id
 JOIN hhc_web.online_bulletin_document d ON d.issue_id=c.issue_id AND d.id=$2 AND d.series='general' AND d.content_locale='zh-Hant'
 JOIN hhc_web.bulletin_version v ON (v.issue_id,v.series,v.locale)=(d.issue_id,d.series,d.content_locale)
 WHERE c.layout_job_id=$1`, job.ID, job.DocumentID, job.Revision).Scan(&correction.issue, &correction.version, &correction.actor, &correction.request, &valid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	return &correction, valid, err
}

func (r *Repository) finishBulletinMetadata(ctx context.Context, tx *sql.Tx, correction bulletinMetadataJob, job onlinebulletins.LayoutJob, oldNumber int, metadata onlinebulletins.CanonicalMetadata, revision int64, result onlinebulletins.LayoutResult, now time.Time) error {
	var accepted []onlinebulletins.ReviewIssue
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT accepted_issues FROM hhc_web.online_bulletin_confirmation WHERE document_id=$1 AND revision=$2`, job.DocumentID, job.Revision).Scan(&raw)
	if err != nil {
		return err
	}
	if json.Unmarshal(raw, &accepted) != nil {
		return bulletins.ErrConflict
	}
	var conflict bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.bulletin_issue WHERE issue_number=$1 AND id<>$2) OR EXISTS(SELECT 1 FROM hhc_web.bulletin_issue_number_alias WHERE issue_number=$1 AND issue_id<>$2)`, metadata.IssueNumber, correction.issue).Scan(&conflict); err != nil {
		return err
	}
	if conflict {
		return bulletins.ErrConflict
	}
	ctx = auditclient.WithProvenance(ctx, correction.actor, correction.request)
	if err = r.applyBulletinMetadata(ctx, tx, correction.issue, correction.version, oldNumber, metadata, correction.actor, now); err != nil {
		return err
	}
	var onlineVersion int64
	if err = tx.QueryRowContext(ctx, `SELECT version FROM hhc_web.online_bulletin_document WHERE id=$1`, job.DocumentID).Scan(&onlineVersion); err != nil {
		return err
	}
	confirmed, err := r.confirmOnlineBulletinTx(ctx, tx, correction.issue, "general", "zh-Hant", onlinebulletins.ConfirmInput{CanonicalVersion: correction.version + 1, OnlineVersion: onlineVersion, Revision: revision, LayoutValidationHash: result.LayoutValidationHash, PageCount: len(result.Document.Pages), AcceptedIssues: accepted}, correction.actor, now, nil)
	if err != nil {
		return err
	}
	var targetWithdrawn bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.online_bulletin_document d JOIN hhc_web.online_bulletin_publication p ON p.document_id=d.id WHERE d.issue_id=$1 AND d.series='general' AND d.content_locale='zh-Hans' AND d.published_revision IS NULL)`, correction.issue).Scan(&targetWithdrawn); err != nil {
		return err
	}
	if _, err = r.publishOnlineBulletinTx(ctx, tx, correction.issue, "general", "zh-Hant", onlinebulletins.PublishInput{CanonicalVersion: correction.version + 1, OnlineVersion: confirmed.Version, Revision: revision}, correction.actor, now, nil, !targetWithdrawn); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `DELETE FROM hhc_web.bulletin_metadata_update WHERE issue_id=$1 AND layout_job_id=$2`, correction.issue, job.ID)
	return err
}
