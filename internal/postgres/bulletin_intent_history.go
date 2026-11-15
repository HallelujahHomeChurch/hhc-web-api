package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"time"
)

// Canonical issue lock serializes all epochs, including recreation of an edition.
// With controls disabled an untouched legacy edition stays unknown. Once epochs
// exist, compatible writers keep participating even when controls are disabled.
func (r *Repository) changeBulletinIntent(ctx context.Context, tx *sql.Tx, issue, series, locale, status, reason, actor string, now time.Time, receipt string) error {
	if series != "general" || locale != "zh-Hant" {
		return nil
	}
	var id, asset, checksum string
	var epoch, canonical int64
	var enabled, history bool
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(v.online_intent_id::text,''),v.online_intent_epoch,v.pdf_asset_id,COALESCE(v.pdf_sha256,''),i.version,v.auto_publish,EXISTS(SELECT 1 FROM hhc_web.online_bulletin_publication_intent h WHERE h.issue_id=i.id AND h.series=v.series AND h.locale=v.locale) FROM hhc_web.bulletin_version v JOIN hhc_web.bulletin_issue i ON i.id=v.issue_id WHERE v.issue_id=$1 AND v.series=$2 AND v.locale=$3`, issue, series, locale).Scan(&id, &epoch, &asset, &checksum, &canonical, &enabled, &history)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !r.explicitOnlinePublication && !history {
		return nil
	}
	p, ok := auditclient.ProvenanceFromContext(ctx)
	if !ok || p.ActorType != "user" || p.ActorID != actor || !auditclient.ValidActorRequest(actor, p.RequestID) {
		return bulletins.ErrAuditUnavailable
	}
	var oldStatus, oldRequest, oldAsset string
	if id != "" {
		if err = tx.QueryRowContext(ctx, `SELECT t.status,h.request_id,h.source_asset_id FROM hhc_web.online_bulletin_publication_intent h JOIN hhc_web.online_bulletin_intent_transition t ON t.intent_id=h.id WHERE h.id=$1 ORDER BY t.sequence DESC LIMIT 1`, id).Scan(&oldStatus, &oldRequest, &oldAsset); err != nil {
			return err
		}
	}
	active := oldStatus == "authorized" || oldStatus == "running"
	if status == "authorized" {
		if active && oldRequest == p.RequestID && oldAsset == asset {
			return nil
		}
		oldID := id
		if active {
			if err = appendBulletinIntentTransition(ctx, tx, id, "superseded", reason, actor, p.RequestID, canonical, receipt, now); err != nil {
				return err
			}
		}
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(max(epoch),0)+1 FROM hhc_web.online_bulletin_publication_intent WHERE issue_id=$1 AND series=$2 AND locale=$3`, issue, series, locale).Scan(&epoch); err != nil {
			return err
		}
		id = platform.NewID()
		if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_publication_intent(id,issue_id,series,locale,epoch,canonical_version,source_asset_id,source_checksum,actor_id,request_id,supersedes_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,'')::uuid,$12)`, id, issue, series, locale, epoch, canonical, asset, checksum, actor, p.RequestID, oldID, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version SET online_intent_id=$4,online_intent_epoch=$5 WHERE issue_id=$1 AND series=$2 AND locale=$3`, issue, series, locale, id, epoch); err != nil {
			return err
		}
	} else if !active {
		// Record only a known present terminal event for a legacy true flag.
		// Never invent its historical authorization or a false flag's disposition.
		if id != "" || !enabled || (status != "cancelled" && status != "completed" && status != "failed") {
			return nil
		}
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(max(epoch),0)+1 FROM hhc_web.online_bulletin_publication_intent WHERE issue_id=$1 AND series=$2 AND locale=$3`, issue, series, locale).Scan(&epoch); err != nil {
			return err
		}
		id = platform.NewID()
		if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_publication_intent(id,issue_id,series,locale,epoch,canonical_version,source_asset_id,source_checksum,actor_id,request_id,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, issue, series, locale, epoch, canonical, asset, checksum, actor, p.RequestID, now); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version SET online_intent_id=$4,online_intent_epoch=$5 WHERE issue_id=$1 AND series=$2 AND locale=$3`, issue, series, locale, id, epoch); err != nil {
			return err
		}
	}
	if status == "authorized" && reason != "recovery_authorized" {
		reason = "automatic_authorized"
	}
	return appendBulletinIntentTransition(ctx, tx, id, status, reason, actor, p.RequestID, canonical, receipt, now)
}
func appendBulletinIntentTransition(ctx context.Context, tx *sql.Tx, id, status, reason, actor, request string, canonical int64, receipt string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_intent_transition(intent_id,sequence,status,reason_code,actor_id,request_id,canonical_version,document_id,revision,receipt_id,created_at)
 SELECT $1,COALESCE((SELECT max(sequence) FROM hhc_web.online_bulletin_intent_transition WHERE intent_id=$1),0)+1,$2,$3,$4,$5,$6,d.id,d.local_revision,NULLIF($7,'')::uuid,$8 FROM hhc_web.online_bulletin_publication_intent h LEFT JOIN hhc_web.online_bulletin_document d ON d.issue_id=h.issue_id AND d.series=h.series AND d.content_locale=h.locale WHERE h.id=$1`, id, status, reason, actor, request, canonical, receipt, now)
	return err
}

const dispositionJSON = `(SELECT jsonb_strip_nulls(jsonb_build_object('intentId',h.id,'epoch',h.epoch,'status',t.status,'reasonCode',t.reason_code,'changedAt',t.created_at,'canonicalVersion',t.canonical_version,'sourceAssetId',h.source_asset_id,'sourceChecksum',h.source_checksum,'documentId',t.document_id,'revision',t.revision,'receiptId',t.receipt_id)) FROM hhc_web.online_bulletin_publication_intent h JOIN LATERAL(SELECT * FROM hhc_web.online_bulletin_intent_transition WHERE intent_id=h.id ORDER BY sequence DESC LIMIT 1)t ON true WHERE h.issue_id=i.id AND h.series=v.series AND h.locale=v.locale ORDER BY h.epoch DESC LIMIT 1)`

func loadBulletinDisposition(ctx context.Context, tx *sql.Tx, issue, series, locale string) (*onlinebulletins.PublicationDisposition, error) {
	var raw []byte
	err := tx.QueryRowContext(ctx, `SELECT `+dispositionJSON+` FROM hhc_web.bulletin_issue i JOIN hhc_web.bulletin_version v ON v.issue_id=i.id WHERE i.id=$1 AND v.series=$2 AND v.locale=$3`, issue, series, locale).Scan(&raw)
	if err != nil {
		return nil, err
	}
	var value *onlinebulletins.PublicationDisposition
	if len(raw) > 0 {
		err = json.Unmarshal(raw, &value)
	}
	return value, err
}

// Only the current source/epoch's terminal job may close its intent. The caller
// holds the canonical lock and commits the job failure in this same transaction.
func (r *Repository) recordBulletinJobFailure(ctx context.Context, tx *sql.Tx, extractionID string, now time.Time) error {
	var issue, actor, request string
	err := tx.QueryRowContext(ctx, `SELECT e.issue_id::text,e.authorizer_id::text,e.authorizer_request_id FROM hhc_web.online_bulletin_extraction_job e JOIN hhc_web.bulletin_version v ON v.issue_id=e.issue_id AND v.series=e.series AND v.locale=e.content_locale WHERE e.id=$1 AND v.online_intent_epoch>0 AND e.publication_intent_id=v.online_intent_id AND e.publication_intent_epoch=v.online_intent_epoch AND v.auto_publish AND e.auto_publish AND e.source_asset_id=v.pdf_asset_id AND e.source_asset_checksum=v.pdf_sha256`, extractionID).Scan(&issue, &actor, &request)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if !auditclient.ValidActorRequest(actor, request) {
		return bulletins.ErrAuditUnavailable
	}
	return r.changeBulletinIntent(auditclient.WithProvenance(ctx, actor, request), tx, issue, "general", "zh-Hant", "failed", "processing_failed", actor, now, "")
}

// Existing automatic reconciliation also closes epochs whose bounded job failed
// during lease-expiry/attempt-limit maintenance. No new resident worker is added.
func (r *Repository) reconcileFailedBulletinIntent(ctx context.Context, tx *sql.Tx, now time.Time) (bool, error) {
	var extraction string
	err := tx.QueryRowContext(ctx, `SELECT e.id::text FROM hhc_web.bulletin_issue i JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series='general' AND v.locale='zh-Hant' JOIN hhc_web.online_bulletin_extraction_job e ON e.publication_intent_id=v.online_intent_id AND e.publication_intent_epoch=v.online_intent_epoch AND e.issue_id=i.id LEFT JOIN hhc_web.online_bulletin_document d ON d.id=e.document_id
 WHERE v.online_intent_epoch>0 AND v.auto_publish AND e.auto_publish AND NOT e.superseded AND e.source_asset_id=v.pdf_asset_id AND e.source_asset_checksum=v.pdf_sha256
 AND (SELECT status IN('authorized','running') FROM hhc_web.online_bulletin_intent_transition WHERE intent_id=v.online_intent_id ORDER BY sequence DESC LIMIT 1)
 AND (e.status='failed' OR EXISTS(SELECT 1 FROM hhc_web.online_bulletin_update_job u WHERE u.extraction_job_id=e.id AND (u.status='failed' OR EXISTS(SELECT 1 FROM hhc_web.publication_workflow w WHERE w.id=u.pdf_workflow_id AND w.status='failed') OR EXISTS(SELECT 1 FROM hhc_web.online_bulletin_layout_job l WHERE l.document_id=u.document_id AND l.source_revision=u.result_revision AND l.status='failed'))))
 ORDER BY i.id LIMIT 1 FOR UPDATE OF i,v,e SKIP LOCKED`).Scan(&extraction)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, r.recordBulletinJobFailure(ctx, tx, extraction, now)
}
