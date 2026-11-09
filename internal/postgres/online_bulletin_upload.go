package postgres

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

func enqueueUploadExtraction(ctx context.Context, tx *sql.Tx, issue bulletins.Issue, assetID string, autoPublish, notify bool, actor string, now time.Time) error {
	p, ok := auditclient.ProvenanceFromContext(ctx)
	if !ok || p.ActorType != "user" || p.ActorID != actor || !auditclient.ValidActorRequest(p.ActorID, p.RequestID) {
		return bulletins.ErrAuditUnavailable
	}
	payload, err := json.Marshal(publication.BulletinExtractionPayload{IssueID: issue.ID, AssetID: assetID, ActorID: p.ActorID, RequestID: p.RequestID, AutoPublish: autoPublish, NotifySubscribers: notify})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) VALUES($1,'hhc-web-api','bulletin.online.ensure_extraction','bulletin',$2,$3,$4,$5,'pending',$6,$6,$6)`, platform.NewID(), issue.ID, issue.Version, payload, fmt.Sprintf("bulletin:%s:online:upload:v%d", issue.ID, issue.Version), now)
	return err
}

func (r *Repository) CompleteOnlineExtractionRequest(ctx context.Context, event publication.Event, checksum string, now time.Time) error {
	digest, err := hex.DecodeString(checksum)
	if err != nil || len(digest) != 32 || checksum != strings.ToLower(checksum) || now.IsZero() {
		return bulletins.ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var stored []byte
	var status string
	var lease sql.NullTime
	var attempts int
	var version int64
	err = tx.QueryRowContext(ctx, `SELECT payload_json,status,claimed_until,attempts,aggregate_version FROM hhc_web.outbox_event WHERE id=$1 AND destination='hhc-web-api' AND event_type='bulletin.online.ensure_extraction' AND aggregate_id=$2 FOR UPDATE`, event.ID, event.AggregateID).Scan(&stored, &status, &lease, &attempts, &version)
	if err != nil {
		return err
	}
	var p publication.BulletinExtractionPayload
	if event.EventType != "bulletin.online.ensure_extraction" || json.Unmarshal(stored, &p) != nil || p.IssueID != event.AggregateID || !auditclient.ValidActorRequest(p.ActorID, p.RequestID) {
		return bulletins.ErrInvalid
	}
	// Match the persisted command, not a caller-supplied replacement payload.
	var received publication.BulletinExtractionPayload
	if json.Unmarshal(event.Payload, &received) != nil || received != p {
		return bulletins.ErrConflict
	}
	if status == "delivered" {
		return nil
	}
	if status != "processing" || !lease.Valid || !lease.Time.After(now) || !lease.Time.Equal(event.ClaimedUntil) || event.Attempts != attempts || event.AggregateVersion != version {
		return publication.ErrStalePublication
	}
	var number int
	var currentAsset string
	var canonicalVersion int64
	var automatic bool
	var request string
	err = tx.QueryRowContext(ctx, `SELECT i.issue_number,i.version,v.pdf_asset_id,v.auto_publish,v.auto_publish_request FROM hhc_web.bulletin_issue i JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series='general' AND v.locale='zh-Hant' WHERE i.id=$1 FOR UPDATE OF i,v`, p.IssueID).Scan(&number, &canonicalVersion, &currentAsset, &automatic, &request)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && number >= 1739 && currentAsset == p.AssetID && automatic == p.AutoPublish && request == p.RequestID && canonicalVersion >= event.AggregateVersion {
		var onlineVersion int64
		err = tx.QueryRowContext(ctx, `SELECT version FROM hhc_web.online_bulletin_document WHERE issue_id=$1 AND series='general' AND content_locale='zh-Hant' FOR UPDATE`, p.IssueID).Scan(&onlineVersion)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		ctx = auditclient.WithProvenance(ctx, p.ActorID, p.RequestID)
		if err = r.validateBulletinAudit(ctx, p.ActorID); err != nil {
			return err
		}
		_, err = r.queueOnlineBulletinExtractionTx(ctx, tx, p.IssueID, onlinebulletins.QueueExtractionInput{CanonicalVersion: canonicalVersion, OnlineVersion: onlineVersion, SourceAssetID: p.AssetID, SourceChecksum: checksum, AutoPublish: p.AutoPublish, NotifySubscribers: p.NotifySubscribers}, p.ActorID, now)
		if err != nil {
			return err
		}
	}
	_, err = tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='delivered',claimed_until=NULL,last_error='',updated_at=$2 WHERE id=$1`, event.ID, now)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// A stale failure must not revoke a newer claim, including Defer's attempts ABA.
func (r *Repository) RescheduleOnlineExtractionRequest(ctx context.Context, event publication.Event, code string, next, now time.Time, waiting bool) error {
	if event.EventType != "bulletin.online.ensure_extraction" || !extractionErrorCode.MatchString(code) || now.IsZero() || !next.IsZero() && !next.After(now) {
		return bulletins.ErrInvalid
	}
	status := "pending"
	if next.IsZero() {
		status, next = "failed", now
	}
	_, err := r.db.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status=$2,next_attempt_at=$3,claimed_until=NULL,last_error=$4,attempts=CASE WHEN $5 THEN GREATEST(attempts-1,0) ELSE attempts END,updated_at=$6 WHERE id=$1 AND event_type='bulletin.online.ensure_extraction' AND status='processing' AND aggregate_id=$7 AND aggregate_version=$8 AND attempts=$9 AND claimed_until=$10 AND claimed_until>$6`, event.ID, status, next, code, waiting, now, event.AggregateID, event.AggregateVersion, event.Attempts, event.ClaimedUntil)
	return err
}
