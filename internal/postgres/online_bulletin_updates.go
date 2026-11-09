package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

// Caller holds canonical, document and extraction locks. Input is immutable CMS
// content; no request body, private notes or inherited worker lease is trusted.
func queueOnlineUpdate(ctx context.Context, tx *sql.Tx, job onlinebulletins.ExtractionJob, incomingRevision int64, candidate onlinebulletins.DocumentCandidate, now time.Time) (bool, error) {
	var enabled bool
	var actor, request sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT auto_publish,authorizer_id::text,authorizer_request_id FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, job.ID).Scan(&enabled, &actor, &request); err != nil {
		return false, err
	}
	if !enabled {
		return false, nil
	}
	if !auditclient.ValidActorRequest(actor.String, request.String) || candidate.Document == nil || !onlinebulletins.ValidRecognitionEvidence(candidate.Recognition, *candidate.Document) {
		return false, onlinebulletins.ErrUpdatePlan
	}
	var canonicalVersion, version int64
	var base, local, published sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT i.version,d.version,d.base_revision,d.local_revision,(SELECT max(p.revision) FROM hhc_web.online_bulletin_publication p WHERE p.document_id=d.id) FROM hhc_web.online_bulletin_document d JOIN hhc_web.bulletin_issue i ON i.id=d.issue_id WHERE d.id=$1`, job.DocumentID).Scan(&canonicalVersion, &version, &base, &local, &published); err != nil {
		return false, err
	}
	input := onlinebulletins.UpdateInput{Canonical: job.Canonical, Incoming: *candidate.Document, Evidence: candidate.Evidence}
	for _, snapshot := range []struct {
		revision sql.NullInt64
		target   **onlinebulletins.Document
	}{{base, &input.Base}, {local, &input.Local}, {published, &input.Published}} {
		if !snapshot.revision.Valid {
			continue
		}
		var raw []byte
		if err := tx.QueryRowContext(ctx, `SELECT document_json FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=$2`, job.DocumentID, snapshot.revision.Int64).Scan(&raw); err != nil {
			return false, err
		}
		if json.Unmarshal(raw, snapshot.target) != nil {
			return false, onlinebulletins.ErrUpdatePlan
		}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return false, err
	}
	id := platform.NewID()
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_update_job SET status='superseded',lease_token=NULL,lease_until=NULL,updated_at=$2 WHERE document_id=$1 AND status IN ('queued','planning')`, job.DocumentID, now); err != nil {
		return false, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_update_job(id,document_id,extraction_job_id,source_revision,canonical_version,online_version,protocol_sha256,input_json,available_at,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$9,$9)`, id, job.DocumentID, job.ID, incomingRevision, canonicalVersion, version, onlinebulletins.UpdateProtocolSHA256(), raw, now); err != nil {
		return false, err
	}
	return true, enqueueOnlineBulletinWake(ctx, tx, id, now)
}

const currentUpdateSource = ` FROM hhc_web.online_bulletin_update_job u
 JOIN hhc_web.online_bulletin_extraction_job e ON e.id=u.extraction_job_id
 JOIN hhc_web.online_bulletin_document d ON d.id=u.document_id
 JOIN hhc_web.bulletin_issue i ON i.id=d.issue_id
 JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series=d.series AND v.locale=d.content_locale`

const validUpdateSource = ` AND e.auto_publish AND NOT e.superseded AND e.status='review_ready'
 AND v.auto_publish AND v.auto_publish_request=e.authorizer_request_id
 AND e.source_asset_id=v.pdf_asset_id AND e.source_asset_checksum=v.pdf_sha256
 AND i.version=u.canonical_version AND d.version=u.online_version
 AND u.input_json->'canonical'->'metadata'=jsonb_build_object('title',v.title,'subtitle',v.subtitle,'issueNumber',i.issue_number,'date',i.issue_date::text)`

func (r *Repository) ClaimOnlineBulletinUpdate(ctx context.Context, now time.Time, lease time.Duration, maxAttempts int) (*onlinebulletins.UpdateJob, error) {
	if now.IsZero() || lease <= 0 || lease > onlinebulletins.UpdateJobTimeout || maxAttempts < 1 || maxAttempts > 10 {
		return nil, onlinebulletins.ErrUpdatePlan
	}
	if !r.llmBulletinExtraction {
		return nil, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_update_job SET status='superseded',lease_token=NULL,lease_until=NULL,updated_at=$1 WHERE status IN ('queued','planning') AND id NOT IN (SELECT u.id`+currentUpdateSource+` WHERE true`+validUpdateSource+` AND u.protocol_sha256=$2)`, now, onlinebulletins.UpdateProtocolSHA256()); err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_update_job SET status='failed',last_error_code='attempt_limit',lease_token=NULL,lease_until=NULL,updated_at=$1 WHERE attempts >= $2 AND (status='queued' OR (status='planning' AND lease_until<=$1))`, now, maxAttempts); err != nil {
		return nil, err
	}
	var job onlinebulletins.UpdateJob
	var raw, attempts []byte
	err = tx.QueryRowContext(ctx, `WITH next AS (SELECT id FROM hhc_web.online_bulletin_update_job WHERE attempts<$4 AND available_at<=$1 AND (status='queued' OR (status='planning' AND lease_until<=$1)) ORDER BY available_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1)
 UPDATE hhc_web.online_bulletin_update_job u SET status='planning',attempts=u.attempts+1,lease_token=$2,lease_until=$3,updated_at=$1 FROM next WHERE u.id=next.id RETURNING u.id::text,u.document_id::text,u.extraction_job_id::text,u.lease_token::text,u.lease_until,u.attempts,u.canonical_version,u.online_version,u.source_revision,u.input_json,u.checkpoint_json`, now, platform.NewID(), now.Add(lease), maxAttempts).Scan(&job.ID, &job.DocumentID, &job.ExtractionJobID, &job.LeaseToken, &job.LeaseUntil, &job.Attempts, &job.CanonicalVersion, &job.OnlineVersion, &job.SourceRevision, &raw, &attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(raw, &job.Input) != nil || json.Unmarshal(attempts, &job.SavedAttempts) != nil {
		return nil, onlinebulletins.ErrUpdatePlan
	}
	return &job, tx.Commit()
}

func (r *Repository) SaveOnlineBulletinUpdateAttempt(ctx context.Context, job onlinebulletins.UpdateJob, attempt onlinebulletins.UpdateAttempt, now time.Time) error {
	if now.IsZero() || attempt.ProtocolSHA256 != onlinebulletins.UpdateProtocolSHA256() {
		return onlinebulletins.ErrUpdatePlan
	}
	raw, err := json.Marshal(attempt)
	if err != nil || len(raw) > 4<<20 {
		return onlinebulletins.ErrUpdatePlan
	}
	input, err := json.Marshal(job.Input)
	if err != nil {
		return onlinebulletins.ErrUpdatePlan
	}
	result, err := r.db.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_update_job SET checkpoint_json=checkpoint_json || jsonb_build_array($3::jsonb),updated_at=$4 WHERE id=$1 AND lease_token=$2 AND status='planning' AND lease_until>$4 AND input_json=$5::jsonb AND jsonb_array_length(checkpoint_json)<2 AND id IN (SELECT u.id`+currentUpdateSource+` WHERE true`+validUpdateSource+`)`, job.ID, job.LeaseToken, raw, now, input)
	return updateLeaseResult(result, err)
}

func updateLeaseResult(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return onlinebulletins.ErrExtractionLease
	}
	return nil
}

func (r *Repository) FailOnlineBulletinUpdate(ctx context.Context, job onlinebulletins.UpdateJob, code string, now time.Time, maxAttempts int) error {
	if now.IsZero() || !extractionErrorCode.MatchString(code) || maxAttempts < 1 || maxAttempts > 10 {
		return onlinebulletins.ErrUpdatePlan
	}
	// Semantic/source failures are terminal. A crash resumes persisted passes under
	// the lease/attempt budget rather than blindly repeating paid requests.
	result, err := r.db.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_update_job SET status='failed',last_error_code=$3,lease_token=NULL,lease_until=NULL,updated_at=$4 WHERE id=$1 AND lease_token=$2 AND status='planning' AND lease_until>$4`, job.ID, job.LeaseToken, code, now)
	return updateLeaseResult(result, err)
}

func (r *Repository) CompleteOnlineBulletinUpdate(ctx context.Context, job onlinebulletins.UpdateJob, evidence onlinebulletins.UpdateEvidence, now time.Time) error {
	if now.IsZero() || !onlinebulletins.ValidUpdateEvidence(job.Input, evidence) {
		return onlinebulletins.ErrUpdatePlan
	}
	document, mappings, err := onlinebulletins.ApplyUpdatePlan(job.Input, evidence.Plan)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(document)
	if err != nil || len(raw) > onlinebulletins.MaxDocumentBytes {
		return onlinebulletins.ErrUpdatePlan
	}
	proof, err := json.Marshal(evidence)
	if err != nil {
		return err
	}
	input, err := json.Marshal(job.Input)
	if err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Match the established canonical -> document -> job lock order.
	var ignored string
	if err = tx.QueryRowContext(ctx, `SELECT i.id::text FROM hhc_web.bulletin_issue i JOIN hhc_web.bulletin_version v ON v.issue_id=i.id WHERE i.id=$1 AND v.series=$2 AND v.locale=$3 FOR UPDATE OF i,v`, job.Input.Canonical.IssueID, job.Input.Canonical.Series, job.Input.Canonical.ContentLocale).Scan(&ignored); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT id::text FROM hhc_web.online_bulletin_document WHERE id=$1 FOR UPDATE`, job.DocumentID).Scan(&ignored); err != nil {
		return err
	}
	var status string
	var saved, savedInput, checkpoints []byte
	var sourceRevision, canonicalVersion, onlineVersion int64
	err = tx.QueryRowContext(ctx, `SELECT status,evidence_json,input_json,checkpoint_json,source_revision,canonical_version,online_version FROM hhc_web.online_bulletin_update_job WHERE id=$1 AND document_id=$2 AND extraction_job_id=$3 FOR UPDATE`, job.ID, job.DocumentID, job.ExtractionJobID).Scan(&status, &saved, &savedInput, &checkpoints, &sourceRevision, &canonicalVersion, &onlineVersion)
	if err != nil {
		return err
	}
	jsonEqual := func(a, b []byte) bool {
		var x, y any
		if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
			return false
		}
		aa, _ := json.Marshal(x)
		bb, _ := json.Marshal(y)
		return bytes.Equal(aa, bb)
	}
	if !jsonEqual(savedInput, input) || sourceRevision != job.SourceRevision || canonicalVersion != job.CanonicalVersion || onlineVersion != job.OnlineVersion {
		return onlinebulletins.ErrUpdatePlan
	}
	if status == "ready" && jsonEqual(saved, proof) {
		return tx.Commit()
	}
	if !jsonEqual(checkpoints, mustJSON(evidence.Attempts)) {
		return onlinebulletins.ErrUpdatePlan
	}
	var valid bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1`+currentUpdateSource+` WHERE u.id=$1 AND u.lease_token=$2 AND u.status='planning' AND u.lease_until>$3`+validUpdateSource+`)`, job.ID, job.LeaseToken, now).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return onlinebulletins.ErrExtractionLease
	}
	var revision int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(max(revision),0)+1 FROM hhc_web.online_bulletin_revision WHERE document_id=$1`, job.DocumentID).Scan(&revision); err != nil {
		return err
	}
	_, contentHash, err := onlinebulletins.MarshalLayoutSubmission(document, job.Input.Canonical.Metadata)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_revision(document_id,issue_id,series,content_locale,revision,document_json,source_asset_checksum,template_version,extractor_version,content_hash,renderer_version,renderer_artifact_sha256,review_issues,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'[{"code":"layout_validation_required","blocking":true}]','bulletin-update')`, job.DocumentID, job.Input.Canonical.IssueID, job.Input.Canonical.Series, job.Input.Canonical.ContentLocale, revision, raw, document.SourceAssetChecksum, document.TemplateVersion, onlinebulletins.RecognitionExtractorVersion(), contentHash, document.LayoutManifest.RendererVersion, document.LayoutManifest.RendererArtifactSha256); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_document SET base_revision=$2,local_revision=$3,incoming_revision=NULL,updated_at=$4 WHERE id=$1`, job.DocumentID, job.SourceRevision, revision, now); err != nil {
		return err
	}
	if _, err = queueOnlineLayout(ctx, tx, job.DocumentID, revision, document, job.Input.Canonical.Metadata, now); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_update_job SET status='ready',evidence_json=$2,mappings_json=$3,result_revision=$4,lease_token=NULL,lease_until=NULL,updated_at=$5 WHERE id=$1`, job.ID, proof, mustJSON(mappings), revision, now); err != nil {
		return err
	}
	return tx.Commit()
}
