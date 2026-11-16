package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

type bulletinProductState struct {
	outcome                                                         bulletins.PublicationOutcome
	number                                                          int
	automatic, withdrawn, publishable, publishing, sourceDraft      bool
	asset, extraction, update, layout, derived                      string
	correction, correctionStatus                                    string
	correctionBlocked                                               bool
	uploadStatus, uploadError, extractionStatus, extractionError    string
	updateStatus, updateError, layoutStatus, layoutError, pdfStatus string
	derivedBlocked                                                  bool
	derivedStatus, derivedError, sourceAssessment                   string
	onlineVersion                                                   int64
	traditionalRevision                                             sql.NullInt64
	metadataMismatch, extractionBlocked, layoutBlocked              bool
	stage                                                           string
}

func (r *Repository) BulletinPublicationOutcome(ctx context.Context, issue string) (*bulletins.PublicationOutcome, error) {
	state, err := r.loadBulletinProductState(ctx, r.db, issue)
	if err != nil || state == nil {
		return nil, err
	}
	return &state.outcome, nil
}

func (r *Repository) loadBulletinProductState(ctx context.Context, query bulletinQueryer, issue string) (*bulletinProductState, error) {
	states, err := r.loadBulletinProductStates(ctx, query, []string{issue})
	return states[issue], err
}

func (r *Repository) loadBulletinProductStates(ctx context.Context, query bulletinQueryer, issues []string) (map[string]*bulletinProductState, error) {
	rows, err := query.QueryContext(ctx, `
 SELECT i.id::text,COALESCE(i.issue_number,0),v.auto_publish,v.pdf_asset_id,
 v.status IN('draft','unpublished','published') AND i.status NOT IN('publishing','unpublishing'), v.status='publishing',v.status='draft',
 encode(sha256(convert_to(jsonb_build_array(i.id,i.version,v.id,v.version,v.pdf_asset_id,v.status,v.online_intent_epoch,d.id,d.version,d.published_revision,sd.id,sd.version,sd.published_revision,mc.layout_job_id,ml.status)::text,'UTF8')),'hex'),
 (v.status IN('unpublished','unpublishing','unpublish_failed') OR d.published_revision IS NULL AND EXISTS(SELECT 1 FROM hhc_web.online_bulletin_publication old WHERE old.document_id=d.id) OR sd.published_revision IS NULL AND EXISTS(SELECT 1 FROM hhc_web.online_bulletin_publication old WHERE old.document_id=sd.id)),
 COALESCE(d.version,0),d.published_revision,
 pdf.issue_id IS NOT NULL,hp.document_id IS NOT NULL,sp.document_id IS NOT NULL,
 COALESCE(pdf.asset_id=v.pdf_asset_id AND pdf.title=v.title AND pdf.subtitle=v.subtitle AND COALESCE(pdf.issue_number,i.issue_number)=i.issue_number AND COALESCE(pdf.issue_date,i.issue_date)=i.issue_date,false),
 COALESCE(hr.source_asset_checksum=v.pdf_sha256 AND v.pdf_sha256<>'' AND hp.canonical_metadata=m.value,false),
 COALESCE(sr.source_asset_checksum=hr.source_asset_checksum AND sp.canonical_metadata->'issueNumber'=hp.canonical_metadata->'issueNumber' AND sp.canonical_metadata->'date'=hp.canonical_metadata->'date' AND COALESCE(derived.status='delivered' OR EXISTS(SELECT 1 FROM hhc_web.online_bulletin_simplified_authorization sa JOIN hhc_web.online_bulletin_recovery_receipt rr ON rr.id=sa.receipt_id WHERE sa.operation_id=derived.id AND rr.result_json#>>'{publication,revision}'=sp.revision::text),true),false),
 COALESCE(upload.status,''),COALESCE(upload.last_error,''),
 COALESCE(e.id::text,''),COALESCE(e.status,''),COALESCE(e.last_error_code,''),
 COALESCE(u.id::text,''),COALESCE(u.status,''),COALESCE(u.last_error_code,''),
 COALESCE(CASE WHEN u.last_error_code='source_not_ready' AND
  u.checkpoint_json#>>'{0,plan,readiness}'=u.checkpoint_json#>>'{1,plan,readiness}'
  THEN u.checkpoint_json#>>'{0,plan,readiness}' END,''),
 COALESCE(l.id::text,''),COALESCE(l.status,''),COALESCE(l.last_error_code,''),
 COALESCE(w.status,''),COALESCE(derived.id::text,''),COALESCE(derived.status,''),COALESCE(derived.last_error,''),
 COALESCE(e.snapshot_json->'issues' @> '[{"code":"canonical_source_mismatch"}]',false),
 COALESCE(e.status='review_ready' AND e.result_revision IS NULL,false),
 COALESCE(l.review_issues<>'[]'::jsonb OR l.status='ready' AND lr.review_issues<>'[]'::jsonb,false),
 mc.metadata_json,COALESCE(ml.id::text,''),COALESCE(ml.status,''),COALESCE(ml.review_issues<>'[]'::jsonb,false),
 COALESCE(dc.last_error_code IN('derived_content_not_ready','conversion_source_changed') OR dl.review_issues<>'[]'::jsonb,false)
 FROM hhc_web.bulletin_issue i JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series='general' AND v.locale='zh-Hant'
 CROSS JOIN LATERAL(SELECT jsonb_build_object('title',v.title,'subtitle',v.subtitle,'issueNumber',i.issue_number,'date',i.issue_date::text)) m(value)
 LEFT JOIN hhc_web.bulletin_published_version pdf ON pdf.issue_id=i.id AND pdf.series=v.series AND pdf.locale=v.locale
 LEFT JOIN hhc_web.online_bulletin_document d ON d.issue_id=i.id AND d.series=v.series AND d.content_locale=v.locale
 LEFT JOIN hhc_web.online_bulletin_publication hp ON hp.document_id=d.id AND hp.revision=d.published_revision AND i.issue_number>=1739
 LEFT JOIN hhc_web.online_bulletin_revision hr ON hr.document_id=hp.document_id AND hr.revision=hp.revision
 LEFT JOIN hhc_web.online_bulletin_document sd ON sd.issue_id=i.id AND sd.series=v.series AND sd.content_locale='zh-Hans'
 LEFT JOIN hhc_web.online_bulletin_publication sp ON sp.document_id=sd.id AND sp.revision=sd.published_revision AND i.issue_number>=1739
 LEFT JOIN hhc_web.online_bulletin_revision sr ON sr.document_id=sp.document_id AND sr.revision=sp.revision
 LEFT JOIN LATERAL(SELECT id,status,last_error,payload_json FROM hhc_web.outbox_event WHERE aggregate_id=d.id AND aggregate_version=d.published_revision AND event_type='bulletin.online.publish_simplified' ORDER BY created_at DESC,id DESC LIMIT 1) derived ON true
 LEFT JOIN hhc_web.online_bulletin_conversion_job dc ON dc.publication_event_id=derived.id AND dc.id::text=derived.payload_json->>'jobId'
 LEFT JOIN LATERAL(SELECT review_issues FROM hhc_web.online_bulletin_layout_job WHERE document_id=dc.document_id AND source_revision=dc.result_revision ORDER BY created_at DESC,id DESC LIMIT 1) dl ON true
 LEFT JOIN LATERAL(SELECT status,last_error FROM hhc_web.outbox_event WHERE aggregate_id=i.id AND event_type='bulletin.online.ensure_extraction' AND payload_json->>'assetId'=v.pdf_asset_id AND payload_json->>'requestId'=v.auto_publish_request ORDER BY created_at DESC,id DESC LIMIT 1) upload ON v.auto_publish
 LEFT JOIN LATERAL(SELECT * FROM hhc_web.online_bulletin_extraction_job WHERE document_id=d.id AND source_asset_id=v.pdf_asset_id AND source_asset_checksum=v.pdf_sha256 AND extractor_version=$2 AND NOT superseded AND auto_publish AND authorizer_request_id=v.auto_publish_request AND COALESCE(snapshot_json->'canonicalMetadata',requested_metadata)=m.value ORDER BY created_at DESC,id DESC LIMIT 1) e ON v.auto_publish
 LEFT JOIN hhc_web.online_bulletin_update_job u ON u.extraction_job_id=e.id AND u.protocol_sha256=$3
 LEFT JOIN LATERAL(SELECT id,status,result_revision,last_error_code,review_issues FROM hhc_web.online_bulletin_layout_job WHERE document_id=d.id AND source_revision=u.result_revision ORDER BY created_at DESC,id DESC LIMIT 1) l ON true
 LEFT JOIN hhc_web.online_bulletin_revision lr ON lr.document_id=d.id AND lr.revision=l.result_revision
 LEFT JOIN hhc_web.publication_workflow w ON w.id=u.pdf_workflow_id
 LEFT JOIN hhc_web.bulletin_metadata_update mc ON mc.issue_id=i.id AND mc.issue_version=i.version AND mc.online_version=d.version
 LEFT JOIN hhc_web.online_bulletin_layout_job ml ON ml.id=mc.layout_job_id
 WHERE i.id=ANY($1::uuid[])`, issues, onlinebulletins.RecognitionExtractorVersion(), onlinebulletins.UpdateProtocolSHA256())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := make(map[string]*bulletinProductState, len(issues))
	for rows.Next() {
		var p bulletinProductState
		var id string
		var pending []byte
		a, c := &p.outcome.Availability, &p.outcome.Current
		if err = rows.Scan(&id, &p.number, &p.automatic, &p.asset, &p.publishable, &p.publishing, &p.sourceDraft, &p.outcome.CommandToken, &p.withdrawn, &p.onlineVersion, &p.traditionalRevision,
			&a.PDF, &a.Traditional, &a.Simplified, &c.PDF, &c.Traditional, &c.Simplified,
			&p.uploadStatus, &p.uploadError, &p.extraction, &p.extractionStatus, &p.extractionError,
			&p.update, &p.updateStatus, &p.updateError, &p.sourceAssessment, &p.layout, &p.layoutStatus, &p.layoutError,
			&p.pdfStatus, &p.derived, &p.derivedStatus, &p.derivedError, &p.metadataMismatch, &p.extractionBlocked, &p.layoutBlocked, &pending, &p.correction, &p.correctionStatus, &p.correctionBlocked, &p.derivedBlocked); err != nil {
			return nil, err
		}
		if len(pending) > 0 && json.Unmarshal(pending, &p.outcome.PendingMetadata) != nil {
			return nil, bulletins.ErrConflict
		}
		p.classify(r.llmBulletinExtraction && r.automaticBulletinExtraction)
		states[id] = &p
	}
	return states, rows.Err()
}

func (r *Repository) GetIssueWithPublicationOutcome(ctx context.Context, id string) (bulletins.Issue, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return bulletins.Issue{}, err
	}
	defer tx.Rollback()
	issue, err := loadIssue(ctx, tx, id)
	if err != nil {
		return issue, err
	}
	items := []bulletins.Issue{issue}
	if err = r.enrichBulletinProduct(ctx, tx, items, []string{id}); err != nil {
		return issue, err
	}
	return items[0], tx.Commit()
}

func (r *Repository) enrichBulletinProduct(ctx context.Context, query bulletinQueryer, items []bulletins.Issue, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	states, err := r.loadBulletinProductStates(ctx, query, ids)
	if err != nil {
		return err
	}
	deletion, err := bulletinDeletionReasons(ctx, query, ids)
	if err != nil {
		return err
	}
	for i := range items {
		issue := &items[i]
		if state := states[issue.ID]; state != nil {
			issue.PublicationOutcome = &state.outcome
		}
		issue.Deletion = deletion.eligibility(issue.ID, "", "")
		for vi := range issue.Versions {
			version := &issue.Versions[vi]
			version.Deletion = deletion.eligibility(issue.ID, version.Series, version.Locale)
		}
	}
	return nil
}

func (p *bulletinProductState) classify(productEnabled bool) {
	outcome := &p.outcome
	outcome.Current.Simplified = outcome.Current.Simplified && outcome.Current.Traditional
	attention := func(reason, action, stage string) {
		outcome.Status, outcome.Reason, outcome.NextAction, p.stage = "needs_attention", reason, action, stage
	}
	if outcome.PendingMetadata != nil {
		outcome.Current = bulletins.PublicationChannels{}
		switch {
		case p.correctionBlocked && p.correctionStatus != "queued" && p.correctionStatus != "validating":
			attention("review_required", "update_metadata", "")
		case p.correctionStatus == "failed":
			attention("processing_failed", "resume", "metadata")
		case p.correctionStatus == "superseded":
			attention("review_required", "update_metadata", "")
		default:
			outcome.Status = "preparing"
		}
		return
	}
	switch {
	case p.automatic && p.number < 1739:
		attention("unsupported_source", "replace_source", "")
	case p.automatic && p.metadataMismatch:
		attention("metadata_mismatch", "update_metadata", "")
	case p.automatic && p.extractionBlocked:
		attention("unsupported_source", "replace_source", "")
	case p.automatic && p.extractionStatus == "failed":
		switch p.extractionError {
		case "unsupported_source", "source_limit":
			attention("unsupported_source", "replace_source", "")
		default:
			attention("processing_failed", "resume", "extraction")
		}
	case p.automatic && p.updateStatus == "failed":
		switch p.sourceAssessment {
		case "wrong_issue":
			attention("metadata_mismatch", "update_metadata", "")
			return
		case "wrong_edition", "unsupported":
			attention("unsupported_source", "replace_source", "")
			return
		case "incomplete":
			attention("source_invalid", "replace_source", "")
			return
		}
		if p.updateError == "provider_unavailable" || p.updateError == "attempt_limit" {
			attention("processing_failed", "resume", "update")
		} else {
			attention("review_required", "review", "")
		}
	case p.automatic && p.layoutBlocked && p.layoutStatus != "queued" && p.layoutStatus != "validating":
		attention("review_required", "review", "")
	case p.automatic && p.layoutStatus == "failed":
		attention("processing_failed", "resume", "layout")
	case p.automatic && p.pdfStatus == "failed":
		attention("processing_failed", "resume", "pdf")
	case p.automatic && p.extraction == "" && p.uploadStatus == "failed":
		switch p.uploadError {
		case "upload_source_invalid":
			attention("source_invalid", "replace_source", "")
		default:
			attention("processing_failed", "resume", "upload")
		}
	case p.automatic && (p.updateStatus == "superseded" || p.layoutStatus == "superseded" || p.extraction == "" && p.uploadStatus == "delivered"):
		attention("review_required", "review", "")
	case p.publishing || p.automatic && !outcome.Current.Traditional:
		outcome.Status = "preparing"
	case outcome.Current.Traditional && (p.derivedStatus == "pending" || p.derivedStatus == "processing"):
		outcome.Status = "preparing"
	case outcome.Current.PDF && outcome.Current.Traditional && outcome.Current.Simplified:
		outcome.Status = "published"
	case outcome.Current.Traditional && p.derivedStatus == "failed" && p.derivedBlocked:
		attention("review_required", "review", "")
	case outcome.Current.Traditional && p.derivedStatus == "failed":
		switch p.derivedError {
		case "publication_withdrawn":
			outcome.Status = "withdrawn"
		case "source_publication_changed", "publication_authority_superseded":
			outcome.Status = "draft"
		case "simplified_review_required", "simplified_mapping_review_required", "simplified_target_changed", "stale_metadata":
			attention("review_required", "review", "")
		default:
			attention("processing_failed", "resume", "simplified")
		}
	case !p.automatic && p.withdrawn:
		outcome.Status = "withdrawn"
	default:
		outcome.Status = "draft"
	}
	// Source freshness and old automation history must not revoke a public
	// edition or authorize a new write. Only an actual saved PDF draft may
	// update an existing publication through this command.
	available := outcome.Availability.PDF || outcome.Availability.Traditional || outcome.Availability.Simplified
	if outcome.Status == "draft" && available && !p.sourceDraft {
		outcome.Status = "published"
	}
	if productEnabled && (!available || p.sourceDraft) && p.publishable && !p.automatic && p.number >= 1739 && p.asset != "" && (outcome.Status == "draft" || outcome.Status == "withdrawn") {
		outcome.NextAction = "publish"
	}
}

func (r *Repository) UpdateBulletinPublication(ctx context.Context, issue string, expected int64, input bulletins.PublicationUpdateInput, key, actor string, now time.Time) (bulletins.PublicationUpdateResult, error) {
	var receipt bulletins.PublicationUpdateResult
	p, ok := auditclient.ProvenanceFromContext(ctx)
	if !ok || p.ActorType != "user" || p.ActorID != actor || !auditclient.ValidActorRequest(actor, p.RequestID) {
		return receipt, bulletins.ErrAuditUnavailable
	}
	hash := sha256.Sum256(mustJSON(struct {
		Version int64
		Input   bulletins.PublicationUpdateInput
	}{expected, input}))
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return receipt, err
	}
	defer tx.Rollback()
	var current int64
	err = tx.QueryRowContext(ctx, `SELECT version FROM hhc_web.bulletin_issue WHERE id=$1 FOR UPDATE`, issue).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return receipt, err
	}
	replay, err := bulletinProductReceipt(ctx, tx, issue, actor, "publication_update", key, hash[:], &receipt)
	if err != nil || replay {
		return receipt, err
	}
	if current == 0 {
		return receipt, bulletins.ErrNotFound
	}
	if current != expected {
		return receipt, bulletins.ErrPrecondition
	}
	state, err := r.loadBulletinProductState(ctx, tx, issue)
	if err != nil {
		return receipt, err
	}
	if state == nil {
		return receipt, bulletins.ErrConflict
	}
	if (input.Metadata != nil || input.Action == "publish" || input.CommandToken != "") && input.CommandToken != state.outcome.CommandToken {
		return receipt, bulletins.ErrPrecondition
	}
	switch {
	case input.Metadata != nil:
		err = r.stageBulletinMetadata(ctx, tx, issue, current, *input.Metadata, actor, now)
	case input.Action == "publish":
		err = r.publishExistingBulletin(ctx, tx, issue, current, *state, actor, now)
	default:
		err = r.resumeBulletinPublication(ctx, tx, issue, current, *state, actor, p.RequestID, now)
	}
	if err != nil {
		return receipt, err
	}
	receipt.Issue, err = loadIssue(ctx, tx, issue)
	if err != nil {
		return receipt, err
	}
	state, err = r.loadBulletinProductState(ctx, tx, issue)
	if err != nil {
		return receipt, err
	}
	receipt.Outcome = state.outcome
	receipt.Issue.PublicationOutcome = &receipt.Outcome
	clearBulletinWorkflowDiagnostics(&receipt.Issue)
	if err = r.enqueueBulletinAudit(ctx, tx, "cms.bulletin.update", issue, actor, now); err != nil {
		return receipt, err
	}
	if err = writeBulletinProductReceipt(ctx, tx, issue, actor, "publication_update", key, hash[:], receipt, now); err != nil {
		return receipt, err
	}
	// Return JSON-normalized values just as an exact replay would.
	if err = json.Unmarshal(mustJSON(receipt), &receipt); err != nil {
		return receipt, err
	}
	return receipt, tx.Commit()
}

func bulletinProductReceipt(ctx context.Context, tx bulletinQueryer, issue, actor, operation, key string, hash []byte, result any) (bool, error) {
	var prior, raw []byte
	err := tx.QueryRowContext(ctx, `SELECT input_hash,result_json FROM hhc_web.bulletin_product_command WHERE issue_id=$1 AND actor_id=$2 AND operation=$3 AND operation_key=$4`, issue, actor, operation, key).Scan(&prior, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !bytes.Equal(hash, prior) {
		return false, bulletins.ErrConflict
	}
	return true, json.Unmarshal(raw, result)
}

func writeBulletinProductReceipt(ctx context.Context, tx *sql.Tx, issue, actor, operation, key string, hash []byte, result any, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_product_command(issue_id,actor_id,operation,operation_key,input_hash,result_json,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, issue, actor, operation, key, hash, mustJSON(result), now)
	return err
}

func (r *Repository) resumeBulletinPublication(ctx context.Context, tx *sql.Tx, issue string, current int64, state bulletinProductState, actor, request string, now time.Time) error {
	if state.stage == "metadata" {
		if err := productAffected(tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_layout_job SET status='queued',attempts=0,available_at=$2,last_error_code='',updated_at=$2 WHERE id=$1 AND status='failed' AND review_issues='[]'`, state.correction, now)); err != nil {
			return err
		}
		if err := productAffected(tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_metadata_update SET actor_id=$3,request_id=$4 WHERE issue_id=$1 AND layout_job_id=$2`, issue, state.correction, actor, request)); err != nil {
			return err
		}
		return enqueueOnlineBulletinWake(ctx, tx, state.correction, now)
	}
	if !r.llmBulletinExtraction || !r.automaticBulletinExtraction {
		return bulletins.ErrOnlinePublicationUnavailable
	}
	if state.stage == "simplified" {
		var targetVersion int64
		if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT version FROM hhc_web.online_bulletin_document WHERE issue_id=$1 AND series='general' AND content_locale='zh-Hans'),0)`, issue).Scan(&targetVersion); err != nil {
			return err
		}
		queued, err := r.retrySimplifiedReleaseTx(ctx, tx, issue, onlinebulletins.QueueSimplifiedInput{
			CanonicalVersion: current, SourceOnlineVersion: state.onlineVersion, SourceRevision: state.traditionalRevision.Int64, TargetOnlineVersion: targetVersion,
		}, actor, now)
		if err != nil {
			return err
		}
		if queued == nil {
			return bulletins.ErrConflict
		}
		_, err = tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET payload_json=payload_json||jsonb_build_object('actorId',$2::text,'requestId',$3::text) WHERE id=$1`, state.derived, actor, request)
		return err
	}
	if !state.automatic || state.stage == "" {
		return bulletins.ErrConflict
	}
	// A publisher renews authority for the same current source, keeping completed
	// recognition, semantic passes and measured layout proofs in their original rows.
	withHistory := *r
	withHistory.explicitOnlinePublication = true
	if err := withHistory.changeBulletinIntent(ctx, tx, issue, "general", "zh-Hant", "authorized", "recovery_authorized", actor, now, ""); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version SET auto_notify=false,auto_publish_request=$2 WHERE issue_id=$1 AND series='general' AND locale='zh-Hant' AND auto_publish`, issue, request); err != nil {
		return err
	}
	if state.stage == "upload" {
		return r.queueExistingBulletinSource(ctx, tx, issue, current, state.asset, actor, now)
	}
	if err := productAffected(tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_extraction_job e SET authorizer_id=$2,authorizer_request_id=$3,publication_intent_id=v.online_intent_id,publication_intent_epoch=v.online_intent_epoch FROM hhc_web.bulletin_version v WHERE e.id=$1 AND NOT e.superseded AND e.auto_publish AND (e.issue_id,e.series,e.content_locale)=(v.issue_id,v.series,v.locale) AND v.pdf_asset_id=e.source_asset_id AND v.pdf_sha256=e.source_asset_checksum`, state.extraction, actor, request)); err != nil {
		return err
	}
	switch state.stage {
	case "extraction":
		if err := productAffected(tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_extraction_job SET status='queued',attempts=0,available_at=$2,last_error_code='',updated_at=$2 WHERE id=$1 AND status='failed' AND snapshot_json IS NULL`, state.extraction, now)); err != nil {
			return err
		}
		return enqueueOnlineBulletinWake(ctx, tx, state.extraction, now)
	case "update":
		if err := productAffected(tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_update_job u SET status='queued',attempts=0,attempt_history_json=checkpoint_json,available_at=$2,last_error_code='',updated_at=$2 WHERE u.id=$1 AND u.status='failed' AND EXISTS(SELECT 1`+currentUpdateSource+` WHERE u.id=$1`+validUpdateSource+`)`, state.update, now)); err != nil {
			return err
		}
		return enqueueOnlineBulletinWake(ctx, tx, state.update, now)
	case "layout":
		if err := productAffected(tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_layout_job l SET status='queued',attempts=0,available_at=$2,last_error_code='',updated_at=$2 FROM hhc_web.online_bulletin_document d WHERE l.id=$1 AND l.status='failed' AND d.id=l.document_id AND d.local_revision=l.source_revision AND d.incoming_revision IS NULL AND l.review_issues='[]'`, state.layout, now)); err != nil {
			return err
		}
		return enqueueOnlineBulletinWake(ctx, tx, state.layout, now)
	case "pdf":
		if err := productAffected(tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_update_job u SET pdf_workflow_id=NULL,updated_at=$2 FROM hhc_web.publication_workflow w WHERE u.id=$1 AND u.pdf_workflow_id=w.id AND w.status='failed'`, state.update, now)); err != nil {
			return err
		}
		return enqueueOnlineBulletinWake(ctx, tx, state.update, now)
	default:
		return bulletins.ErrConflict
	}
}

func (r *Repository) queueExistingBulletinSource(ctx context.Context, tx *sql.Tx, issue string, current int64, asset, actor string, now time.Time) error {
	if err := productAffected(tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_issue SET status=CASE WHEN status='unpublished' THEN 'draft' ELSE status END,version=version+1,updated_by=$3,updated_at=$4 WHERE id=$1 AND version=$2`, issue, current, actor, now)); err != nil {
		return err
	}
	next, err := loadIssue(ctx, tx, issue)
	if err != nil {
		return err
	}
	if err = insertBulletinRevision(ctx, tx, next, actor, now); err != nil {
		return err
	}
	return enqueueUploadExtraction(ctx, tx, next, asset, true, false, actor, now)
}

func (r *Repository) publishExistingBulletin(ctx context.Context, tx *sql.Tx, issue string, current int64, state bulletinProductState, actor string, now time.Time) error {
	if !r.llmBulletinExtraction || !r.automaticBulletinExtraction {
		return bulletins.ErrOnlinePublicationUnavailable
	}
	if state.outcome.NextAction != "publish" {
		return bulletins.ErrConflict
	}
	if state.outcome.Current.PDF && state.outcome.Current.Traditional {
		// A fresh user request may authorize only the missing derived edition
		// without reopening the current PDF or Traditional publication.
		var source onlinebulletins.ConfirmedRevision
		var metadata onlinebulletins.CanonicalMetadata
		var raw []byte
		err := tx.QueryRowContext(ctx, `UPDATE hhc_web.online_bulletin_document d SET version=d.version+1,updated_at=$3 FROM hhc_web.online_bulletin_publication p
 WHERE d.issue_id=$1 AND d.series='general' AND d.content_locale='zh-Hant' AND d.published_revision=$2 AND p.document_id=d.id AND p.revision=d.published_revision
 RETURNING d.id::text,d.version,d.published_revision,p.canonical_metadata`, issue, state.traditionalRevision.Int64, now).Scan(&source.DocumentID, &source.Version, &source.Revision, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			return bulletins.ErrPrecondition
		}
		if err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &metadata); err != nil {
			return err
		}
		return enqueueSimplifiedRelease(ctx, tx, source, issue, metadata, actor, now)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version SET status='draft',version=version+1,updated_by=$2,updated_at=$3 WHERE issue_id=$1 AND series='general' AND locale='zh-Hant' AND status='unpublished'`, issue, actor, now); err != nil {
		return err
	}
	if err := r.setAutomaticBulletinIntentReason(ctx, tx, issue, "general", "zh-Hant", true, false, actor, now, "automatic_authorized"); err != nil {
		return err
	}
	return r.queueExistingBulletinSource(ctx, tx, issue, current, state.asset, actor, now)
}

func productAffected(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return bulletins.ErrConflict
	}
	return nil
}

func (r *Repository) ReplayBulletinUploadCompletion(ctx context.Context, issue, actor string, command bulletins.UploadCompletionCommand) (bulletins.Issue, bool, error) {
	var result bulletins.Issue
	if err := r.validateBulletinAudit(ctx, actor); err != nil {
		return result, false, err
	}
	replayed, err := bulletinProductReceipt(ctx, r.db, issue, actor, "complete_upload", command.Key, command.InputHash[:], &result)
	return result, replayed, err
}

func clearBulletinWorkflowDiagnostics(issue *bulletins.Issue) {
	for index := range issue.Versions {
		issue.Versions[index].WorkflowError = ""
	}
}
