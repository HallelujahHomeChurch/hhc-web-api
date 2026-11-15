package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

const simplifiedReleaseEvent = "bulletin.online.publish_simplified"

type simplifiedReleasePayload struct {
	IssueID                  string                            `json:"issueId"`
	SourcePublicationVersion int64                             `json:"sourcePublicationVersion"`
	SourceRevision           int64                             `json:"sourceRevision"`
	SourceMetadata           onlinebulletins.CanonicalMetadata `json:"sourceMetadata"`
	ActorID                  string                            `json:"actorId"`
	RequestID                string                            `json:"requestId"`
	JobID                    string                            `json:"jobId,omitempty"`
}

// The human publication command authorizes this exact derived release. Queue
// and publication commit together; conversion failures never undo the source.
func enqueueSimplifiedRelease(ctx context.Context, tx *sql.Tx, source onlinebulletins.ConfirmedRevision, issueID string, metadata onlinebulletins.CanonicalMetadata, actor string, now time.Time) error {
	p, ok := auditclient.ProvenanceFromContext(ctx)
	if !ok || p.ActorType != "user" || p.ActorID != actor || !auditclient.ValidActorRequest(actor, p.RequestID) {
		return bulletins.ErrAuditUnavailable
	}
	raw, err := json.Marshal(simplifiedReleasePayload{IssueID: issueID, SourceRevision: source.Revision, SourcePublicationVersion: source.Version, SourceMetadata: metadata, ActorID: actor, RequestID: p.RequestID})
	if err != nil {
		return err
	}
	key := fmt.Sprintf("online-bulletin:%s:simplified-release:%d", source.DocumentID, source.Version)
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM hhc_web.outbox_event WHERE destination='hhc-web-api' AND idempotency_key=$1 FOR UPDATE`, key).Scan(&status)
	// A source publication replay is a read-only duplicate, even after a
	// failure, withdrawal or distinct human recovery. Only a new source
	// command version creates a new grant; explicit retry resumes valid ones.
	if err == nil {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	// Old pending intents cannot be revived by a later republish of the same revision.
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='failed',claimed_until=NULL,last_error='source_publication_changed',updated_at=$2 WHERE aggregate_id=$1 AND event_type=$3 AND status IN ('pending','processing')`, source.DocumentID, now, simplifiedReleaseEvent); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at) VALUES($1,'hhc-web-api',$2,'online_bulletin',$3,$4,$5,$6,'pending',$7,$7,$7) ON CONFLICT(destination,idempotency_key) DO NOTHING`, platform.NewID(), simplifiedReleaseEvent, source.DocumentID, source.Revision, raw, key, now)
	return err
}

// One bounded queue step. Heavy conversion/layout stay in the existing extractor.
// Issue -> document -> event lock order also fences concurrent unpublish/update.
func (r *Repository) CompleteSimplifiedRelease(ctx context.Context, event publication.Event, now time.Time) error {
	var p simplifiedReleasePayload
	if event.EventType != simplifiedReleaseEvent || json.Unmarshal(event.Payload, &p) != nil || p.SourceRevision != event.AggregateVersion || !auditclient.ValidActorRequest(p.ActorID, p.RequestID) {
		return bulletins.ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var canonicalVersion, sourceVersion int64
	var published sql.NullInt64
	var metadataJSON []byte
	err = tx.QueryRowContext(ctx, `SELECT i.version,d.version,d.published_revision,p.canonical_metadata FROM hhc_web.bulletin_issue i JOIN hhc_web.online_bulletin_document d ON d.issue_id=i.id AND d.series='general' AND d.content_locale='zh-Hant' LEFT JOIN hhc_web.online_bulletin_publication p ON p.document_id=d.id AND p.revision=d.published_revision WHERE i.id=$1 AND d.id=$2 FOR UPDATE OF i,d`, p.IssueID, event.AggregateID).Scan(&canonicalVersion, &sourceVersion, &published, &metadataJSON)
	if err != nil {
		return err
	}
	var raw []byte
	var status, claimToken string
	var until sql.NullTime
	var attempts int
	err = tx.QueryRowContext(ctx, `SELECT payload_json,status,claimed_until,attempts,COALESCE(simplified_claim_token::text,'') FROM hhc_web.outbox_event WHERE id=$1 AND event_type=$2 AND aggregate_id=$3 AND aggregate_version=$4 FOR UPDATE`, event.ID, simplifiedReleaseEvent, event.AggregateID, event.AggregateVersion).Scan(&raw, &status, &until, &attempts, &claimToken)
	if err != nil {
		return err
	}
	if status == "delivered" {
		return nil
	}
	if event.SimplifiedClaimToken == "" || claimToken != event.SimplifiedClaimToken || status != "processing" || !bytes.Equal(raw, event.Payload) || !until.Valid || !until.Time.Equal(event.ClaimedUntil) || !until.Time.After(now) || attempts != event.Attempts {
		return publication.ErrStalePublication
	}
	finish := func(status, code string) error {
		_, e := tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status=$2,claimed_until=NULL,last_error=$3,updated_at=$4 WHERE id=$1`, event.ID, status, code, now)
		if e != nil {
			return e
		}
		return tx.Commit()
	}
	wait := func() error {
		payload, e := json.Marshal(p)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='pending',last_error='',payload_json=$2,attempts=GREATEST(attempts-1,0),claimed_until=NULL,next_attempt_at=$3,updated_at=$4 WHERE id=$1`, event.ID, payload, now.Add(10*time.Second), now)
		if e != nil {
			return e
		}
		return tx.Commit()
	}
	var sourceMetadata onlinebulletins.CanonicalMetadata
	if !published.Valid || published.Int64 != p.SourceRevision || json.Unmarshal(metadataJSON, &sourceMetadata) != nil || sourceMetadata != p.SourceMetadata {
		return finish("failed", "source_publication_changed")
	}
	ctx = auditclient.WithProvenance(ctx, p.ActorID, p.RequestID)
	if p.JobID == "" {
		var targetVersion int64
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT version FROM hhc_web.online_bulletin_document WHERE issue_id=$1 AND series='general' AND content_locale='zh-Hans'),0)`, p.IssueID).Scan(&targetVersion); err != nil {
			return err
		}
		queued, e := r.queueOnlineBulletinConversionTx(ctx, tx, p.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: canonicalVersion, SourceOnlineVersion: sourceVersion, SourceRevision: p.SourceRevision, TargetOnlineVersion: targetVersion}, p.ActorID, now, event.ID)
		if e != nil {
			return e
		}
		p.JobID = queued.Job.ID
		return wait()
	}
	var targetID, conversionStatus, layoutStatus string
	var local, result sql.NullInt64
	var targetVersion int64
	var documentJSON, issuesJSON, layoutMetadata []byte
	if err = tx.QueryRowContext(ctx, `SELECT d.id::text FROM hhc_web.online_bulletin_document d JOIN hhc_web.online_bulletin_conversion_job c ON c.document_id=d.id WHERE c.id=$1 FOR UPDATE OF d`, p.JobID).Scan(&targetID); err != nil {
		return err
	}
	err = tx.QueryRowContext(ctx, `SELECT c.document_id::text,c.status,c.result_revision,d.local_revision,d.version,r.document_json,r.review_issues,COALESCE(l.status,''),l.canonical_metadata FROM hhc_web.online_bulletin_conversion_job c JOIN hhc_web.online_bulletin_document d ON d.id=c.document_id LEFT JOIN hhc_web.online_bulletin_revision r ON r.document_id=d.id AND r.revision=d.local_revision LEFT JOIN LATERAL (SELECT * FROM hhc_web.online_bulletin_layout_job WHERE document_id=d.id AND source_revision=c.result_revision ORDER BY created_at DESC,id DESC LIMIT 1) l ON true WHERE c.id=$1 AND c.publication_event_id=$2 FOR UPDATE OF c`, p.JobID, event.ID).Scan(&targetID, &conversionStatus, &result, &local, &targetVersion, &documentJSON, &issuesJSON, &layoutStatus, &layoutMetadata)
	if errors.Is(err, sql.ErrNoRows) {
		return finish("failed", "source_publication_changed")
	}
	if err != nil {
		return err
	}
	if conversionStatus == "failed" {
		return finish("failed", "simplified_conversion_failed")
	}
	if conversionStatus == "superseded" {
		return finish("failed", "source_publication_changed")
	}
	if layoutStatus == "failed" {
		return finish("failed", "simplified_layout_failed")
	}
	if layoutStatus == "superseded" {
		return finish("failed", "simplified_target_changed")
	}
	if conversionStatus != "review_ready" || layoutStatus != "ready" {
		return wait()
	}
	if r.llmBulletinExtraction {
		verified, e := validConversionAssessmentTx(ctx, tx, p.JobID)
		if e != nil {
			return e
		}
		if !verified {
			p.JobID = ""
			return wait()
		}
	}
	var measuredRevision int64
	if err = tx.QueryRowContext(ctx, `SELECT result_revision FROM hhc_web.online_bulletin_layout_job WHERE document_id=$1 AND source_revision=$2 AND status='ready' ORDER BY created_at DESC,id DESC LIMIT 1`, targetID, result).Scan(&measuredRevision); err != nil {
		return err
	}
	if !local.Valid || local.Int64 != measuredRevision {
		return finish("failed", "simplified_target_changed")
	}
	var document onlinebulletins.Document
	var issues []onlinebulletins.ReviewIssue
	var metadata onlinebulletins.CanonicalMetadata
	if json.Unmarshal(documentJSON, &document) != nil || json.Unmarshal(issuesJSON, &issues) != nil || json.Unmarshal(layoutMetadata, &metadata) != nil {
		return bulletins.ErrConflict
	}
	var currentMetadata onlinebulletins.CanonicalMetadata
	if err = tx.QueryRowContext(ctx, `SELECT v.title,v.subtitle,i.issue_number,i.issue_date::text FROM hhc_web.bulletin_version v JOIN hhc_web.bulletin_issue i ON i.id=v.issue_id WHERE v.issue_id=$1 AND v.series='general' AND v.locale='zh-Hans' FOR UPDATE OF v`, p.IssueID).Scan(&currentMetadata.Title, &currentMetadata.Subtitle, &currentMetadata.IssueNumber, &currentMetadata.Date); err != nil {
		return err
	}
	if currentMetadata != metadata {
		return finish("failed", "stale_metadata")
	}
	if len(issues) != 0 {
		return finish("failed", "simplified_review_required")
	}
	mappings, err := simplifiedReleaseMappings(ctx, tx, targetID, event.AggregateID, p.SourceRevision, document)
	if errors.Is(err, onlinebulletins.ErrMapping) {
		return finish("failed", "simplified_mapping_review_required")
	}
	if err != nil {
		return err
	}
	// Proof and current source are checked under the same lock as both writes.
	var confirmed bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.online_bulletin_confirmation WHERE document_id=$1 AND revision=$2 AND layout_validation_hash=$3 AND canonical_metadata=$4::jsonb)`, targetID, local.Int64, document.LayoutManifest.LayoutValidationHash, layoutMetadata).Scan(&confirmed); err != nil {
		return err
	}
	if !confirmed {
		confirmation, e := r.confirmOnlineBulletinTx(ctx, tx, p.IssueID, "general", "zh-Hans", onlinebulletins.ConfirmInput{CanonicalVersion: canonicalVersion, OnlineVersion: targetVersion, Revision: local.Int64, LayoutValidationHash: document.LayoutManifest.LayoutValidationHash, PageCount: len(document.Pages), AcceptedIssues: []onlinebulletins.ReviewIssue{}}, p.ActorID, now, &metadata)
		if e != nil {
			return e
		}
		targetVersion = confirmation.Version
	}
	// A mapping ambiguity is a real exception; never invent note migrations.
	if _, err = r.publishOnlineBulletinTx(ctx, tx, p.IssueID, "general", "zh-Hans", onlinebulletins.PublishInput{CanonicalVersion: canonicalVersion, OnlineVersion: targetVersion, Revision: local.Int64, Mappings: mappings}, p.ActorID, now, &metadata); err != nil {
		return err
	}
	return finish("delivered", "")
}

// Conversion preserves source sentence identities. Reuse explicit Traditional
// structural decisions only when the exact publication endpoints are proven.
func simplifiedReleaseMappings(ctx context.Context, tx *sql.Tx, targetID, sourceID string, sourceRevision int64, next onlinebulletins.Document) ([]onlinebulletins.SentenceMapping, error) {
	var raw []byte
	var previousSource sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT r.document_json,c.source_revision FROM hhc_web.online_bulletin_publication p JOIN hhc_web.online_bulletin_revision r ON r.document_id=p.document_id AND r.revision=p.revision LEFT JOIN hhc_web.online_bulletin_layout_job l ON l.document_id=p.document_id AND l.result_revision=p.revision AND l.status='ready' LEFT JOIN hhc_web.online_bulletin_conversion_job c ON c.document_id=l.document_id AND c.result_revision=l.source_revision AND c.source_document_id=$2 WHERE p.document_id=$1 ORDER BY p.revision DESC LIMIT 1`, targetID, sourceID).Scan(&raw, &previousSource)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var previous onlinebulletins.Document
	if json.Unmarshal(raw, &previous) != nil {
		return nil, onlinebulletins.ErrMapping
	}
	automatic, err := onlinebulletins.PublicationMappings(previous, next, nil)
	if err != nil {
		return nil, err
	}
	needsReview := false
	eligible := map[string]bool{}
	for _, m := range automatic {
		eligible[m.FromSentenceID] = true
		if m.Status == "requires_review" {
			needsReview = true
		}
	}
	if !needsReview {
		return nil, nil
	}
	if !previousSource.Valid {
		return nil, onlinebulletins.ErrMapping
	}
	rows, err := tx.QueryContext(ctx, `SELECT from_sentence_id,status,to_sentence_ids FROM hhc_web.online_bulletin_mapping WHERE document_id=$1 AND from_revision=$2 AND to_revision=$3 AND status IN ('split','merged','removed') ORDER BY from_sentence_id`, sourceID, previousSource.Int64, sourceRevision)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	decisions := []onlinebulletins.SentenceMapping{}
	for rows.Next() {
		var m onlinebulletins.SentenceMapping
		var targets []byte
		if err = rows.Scan(&m.FromSentenceID, &m.Status, &targets); err != nil {
			return nil, err
		}
		if json.Unmarshal(targets, &m.ToSentenceIDs) != nil {
			return nil, onlinebulletins.ErrMapping
		}
		if eligible[m.FromSentenceID] {
			decisions = append(decisions, m)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	resolved, err := onlinebulletins.PublicationMappings(previous, next, decisions)
	if err != nil {
		return nil, err
	}
	for _, m := range resolved {
		if m.Status == "requires_review" {
			return nil, onlinebulletins.ErrMapping
		}
	}
	return decisions, nil
}

// Compare the original claim before retrying: stale workers cannot reschedule
// a newer owner or revive a source publication that was explicitly cancelled.
func (r *Repository) RescheduleSimplifiedRelease(ctx context.Context, event publication.Event, next, now time.Time) error {
	status, code := "pending", "simplified_publication_failed"
	if next.IsZero() {
		status = "failed"
		next = now
	}
	_, err := r.db.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status=$2,next_attempt_at=$3,claimed_until=NULL,last_error=$4,updated_at=$5 WHERE id=$1 AND event_type=$6 AND status='processing' AND aggregate_id=$7 AND aggregate_version=$8 AND attempts=$9 AND claimed_until=$10 AND claimed_until>$5 AND simplified_claim_token::text=$11`, event.ID, status, next, code, now, simplifiedReleaseEvent, event.AggregateID, event.AggregateVersion, event.Attempts, event.ClaimedUntil, event.SimplifiedClaimToken)
	return err
}

// An explicit withdrawal cancels the derived grant even before Hans has its
// first published revision. The issue/document lock is held by the caller.
func cancelSimplifiedReleaseTx(ctx context.Context, tx *sql.Tx, documentID string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event e SET status='failed',claimed_until=NULL,last_error='publication_withdrawn',updated_at=$2 WHERE e.event_type='bulletin.online.publish_simplified' AND e.status IN ('pending','processing','failed') AND (e.aggregate_id=$1 OR e.payload_json->>'issueId'=(SELECT issue_id::text FROM hhc_web.online_bulletin_document WHERE id=$1 AND content_locale='zh-Hans'))`, documentID, now)
	return err
}
