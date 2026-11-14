package postgres

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

func (r *Repository) SaveOnlineBulletinDraft(ctx context.Context, issueID, series, locale string, input onlinebulletins.SaveDraftInput, actor string, now time.Time) (onlinebulletins.SavedDraft, error) {
	if len(input.Components) < 6 || len(input.Components) > 256 {
		return onlinebulletins.SavedDraft{}, bulletins.ErrInvalid
	}
	return r.saveOnlineLocal(ctx, issueID, series, locale, input, 0, nil, nil, actor, now)
}

func (r *Repository) RestoreOnlineBulletin(ctx context.Context, issueID, series, locale string, input onlinebulletins.RestoreInput, actor string, now time.Time) (onlinebulletins.SavedDraft, error) {
	if input.Revision < 1 {
		return onlinebulletins.SavedDraft{}, bulletins.ErrInvalid
	}
	return r.saveOnlineLocal(ctx, issueID, series, locale, onlinebulletins.SaveDraftInput{CanonicalVersion: input.CanonicalVersion, OnlineVersion: input.OnlineVersion}, input.Revision, nil, nil, actor, now)
}

func (r *Repository) ApplyOnlineBulletinComparison(ctx context.Context, issueID, series, locale string, input onlinebulletins.CompareInput, actor string, now time.Time) (onlinebulletins.SavedDraft, error) {
	if input.BaseRevision < 1 || input.LocalRevision < 1 || input.IncomingRevision < 1 || len(input.Choices) > 65536 {
		return onlinebulletins.SavedDraft{}, bulletins.ErrInvalid
	}
	return r.saveOnlineLocal(ctx, issueID, series, locale, onlinebulletins.SaveDraftInput{CanonicalVersion: input.CanonicalVersion, OnlineVersion: input.OnlineVersion}, 0, &input, nil, actor, now)
}

// Both writes create a new Local and invalidate layout in the same transaction.
// A restore selects server-owned immutable history, never client geometry.
func (r *Repository) saveOnlineLocal(ctx context.Context, issueID, series, locale string, input onlinebulletins.SaveDraftInput, restoreRevision int64, comparison *onlinebulletins.CompareInput, repair *onlinebulletins.PreparedSourceRepair, actor string, now time.Time) (onlinebulletins.SavedDraft, error) {
	var result onlinebulletins.SavedDraft
	// Simplified content is regenerated from the reviewed Traditional source.
	// Reject legacy editors too, so they cannot create a divergent Local draft.
	if locale == "zh-Hans" {
		return result, bulletins.ErrConflict
	}
	if input.CanonicalVersion < 1 || input.OnlineVersion < 1 || actor == "" || now.IsZero() {
		return result, bulletins.ErrInvalid
	}
	if err := r.validateBulletinAudit(ctx, actor); err != nil {
		return result, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var canonicalVersion, local int64
	var currentJSON, issuesJSON []byte
	var extractor string
	var canonical onlinebulletins.CanonicalMetadata
	err = tx.QueryRowContext(ctx, `SELECT d.id::text,d.version,d.local_revision,i.version,v.title,v.subtitle,i.issue_number,i.issue_date::text,r.document_json,r.review_issues,r.extractor_version
	 FROM hhc_web.online_bulletin_document d JOIN hhc_web.bulletin_issue i ON i.id=d.issue_id
	 JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series=d.series AND v.locale=d.content_locale
	 JOIN hhc_web.online_bulletin_revision r ON r.document_id=d.id AND r.revision=d.local_revision
	 WHERE d.issue_id=$1 AND d.series=$2 AND d.content_locale=$3 FOR UPDATE OF i,v,d`, issueID, series, locale).Scan(&result.DocumentID, &result.Version, &local, &canonicalVersion, &canonical.Title, &canonical.Subtitle, &canonical.IssueNumber, &canonical.Date, &currentJSON, &issuesJSON, &extractor)
	if errors.Is(err, sql.ErrNoRows) {
		return result, bulletins.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	if result.Version != input.OnlineVersion || canonicalVersion != input.CanonicalVersion {
		return result, bulletins.ErrPrecondition
	}
	if restoreRevision > 0 {
		err = tx.QueryRowContext(ctx, `SELECT document_json,review_issues,extractor_version FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=$2`, result.DocumentID, restoreRevision).Scan(&currentJSON, &issuesJSON, &extractor)
		if errors.Is(err, sql.ErrNoRows) {
			return result, bulletins.ErrNotFound
		}
		if err != nil {
			return result, err
		}
	}
	var current onlinebulletins.Document
	var issues []onlinebulletins.ReviewIssue
	if json.Unmarshal(currentJSON, &current) != nil || json.Unmarshal(issuesJSON, &issues) != nil {
		return result, onlinebulletins.ErrComponentEdit
	}
	draft := current
	if comparison != nil {
		var baseRevision, incomingRevision sql.NullInt64
		if err = tx.QueryRowContext(ctx, `SELECT base_revision,incoming_revision FROM hhc_web.online_bulletin_document WHERE id=$1`, result.DocumentID).Scan(&baseRevision, &incomingRevision); err != nil {
			return result, err
		}
		if !baseRevision.Valid || !incomingRevision.Valid || comparison.BaseRevision != baseRevision.Int64 || comparison.IncomingRevision != incomingRevision.Int64 || comparison.LocalRevision != local {
			return result, bulletins.ErrPrecondition
		}
		// An issue edit can leave Incoming intact; only current extraction
		// evidence may replace hard canonical/source findings.
		var currentEvidence bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.online_bulletin_extraction_job
		 WHERE document_id=$1 AND result_revision=$2 AND snapshot_json->'canonicalMetadata'=$3::jsonb)`, result.DocumentID, incomingRevision.Int64, mustJSON(canonical)).Scan(&currentEvidence); err != nil {
			return result, err
		}
		if !currentEvidence {
			return result, bulletins.ErrPrecondition
		}
		var base, incoming onlinebulletins.Document
		var incomingIssues []onlinebulletins.ReviewIssue
		for _, source := range []struct {
			revision int64
			document *onlinebulletins.Document
		}{{baseRevision.Int64, &base}, {incomingRevision.Int64, &incoming}} {
			var raw, reviews []byte
			if err = tx.QueryRowContext(ctx, `SELECT document_json,review_issues,extractor_version FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=$2`, result.DocumentID, source.revision).Scan(&raw, &reviews, &extractor); err != nil {
				return result, err
			}
			if json.Unmarshal(raw, source.document) != nil {
				return result, bulletins.ErrConflict
			}
			if source.revision == incomingRevision.Int64 {
				if json.Unmarshal(reviews, &incomingIssues) != nil {
					return result, bulletins.ErrConflict
				}
			}
		}
		draft, err = onlinebulletins.MergeDocuments(base, current, incoming, comparison.Choices)
		if err != nil {
			return result, bulletins.ErrInvalid
		}
		issues = onlinebulletins.MergeReviewIssues(incoming, draft, issues, incomingIssues)
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_document SET base_revision=incoming_revision,incoming_revision=NULL WHERE id=$1`, result.DocumentID); err != nil {
			return result, err
		}
	} else if repair != nil {
		if repair.Input.Revision != local {
			return result, bulletins.ErrPrecondition
		}
		draft, err = onlinebulletins.ApplySourceRepair(current, *repair)
		if err != nil {
			return result, bulletins.ErrInvalid
		}
	} else if restoreRevision == 0 {
		draft, err = onlinebulletins.EditComponents(current, input.Components)
		if err != nil {
			return result, bulletins.ErrInvalid
		}
	}
	var publishedJSON []byte
	err = tx.QueryRowContext(ctx, `SELECT r.document_json FROM hhc_web.online_bulletin_revision r WHERE r.document_id=$1 AND r.revision=(SELECT MAX(revision) FROM hhc_web.online_bulletin_publication WHERE document_id=$1)`, result.DocumentID).Scan(&publishedJSON)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if err == nil {
		var published onlinebulletins.Document
		if json.Unmarshal(publishedJSON, &published) != nil {
			return result, onlinebulletins.ErrComponentEdit
		}
		var remap map[string]string
		var extractedJSON []byte
		err = tx.QueryRowContext(ctx, `SELECT r.document_json FROM hhc_web.online_bulletin_document d JOIN hhc_web.online_bulletin_revision r ON r.document_id=d.id AND r.revision=d.base_revision WHERE d.id=$1 AND r.created_by='bulletin-extractor'`, result.DocumentID).Scan(&extractedJSON)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return result, err
		}
		if err == nil {
			var extracted onlinebulletins.Document
			if json.Unmarshal(extractedJSON, &extracted) != nil {
				return result, onlinebulletins.ErrComponentEdit
			}
			if draft, remap, err = onlinebulletins.RestorePublishedSentenceIdentities(published, extracted, draft); err != nil {
				return result, err
			}
		}
		for i := range issues {
			if id := remap[issues[i].SentenceID]; id != "" {
				issues[i].SentenceID = id
			}
		}
	}
	result, err = writeOnlineLocal(ctx, tx, result, draft, issues, extractor, canonical, actor, now)
	if err != nil {
		return result, err
	}
	if err = r.cancelOnlineAutomaticIntent(ctx, tx, result.DocumentID, now); err != nil {
		return result, err
	}
	action := "cms.bulletin.online.update"
	if restoreRevision > 0 {
		action = "cms.bulletin.online.restore"
	} else if comparison != nil {
		action = "cms.bulletin.online.compare"
	}
	if err = r.enqueueBulletinAudit(ctx, tx, action, result.DocumentID, actor, now); err != nil {
		return result, err
	}
	return result, tx.Commit()
}

func writeOnlineLocal(ctx context.Context, tx *sql.Tx, result onlinebulletins.SavedDraft, draft onlinebulletins.Document, issues []onlinebulletins.ReviewIssue, extractor string, canonical onlinebulletins.CanonicalMetadata, actor string, now time.Time) (onlinebulletins.SavedDraft, error) {
	draft.LayoutManifest.ContentHash, draft.LayoutManifest.LayoutValidationHash = "", ""
	validation, err := onlinebulletins.ValidateDocument(draft)
	if err != nil {
		return result, bulletins.ErrInvalid
	}
	kept := []onlinebulletins.ReviewIssue{}
	seen := map[onlinebulletins.ReviewIssue]bool{}
	for _, issue := range issues {
		if issue.Code == "unknown_body_kind" || issue.Code == "missing_body_header_contributors" {
			continue
		}
		if !seen[issue] {
			kept = append(kept, issue)
			seen[issue] = true
		}
	}
	for _, issue := range append(validation, onlinebulletins.ReviewIssue{Code: "layout_validation_required", Blocking: true}) {
		if !seen[issue] {
			kept = append(kept, issue)
			seen[issue] = true
		}
	}
	if len(kept) > 4096 {
		return result, bulletins.ErrInvalid
	}
	documentJSON, err := json.Marshal(draft)
	if err != nil {
		return result, err
	}
	issuesJSON, err := json.Marshal(kept)
	if err != nil {
		return result, err
	}
	_, contentHash, err := onlinebulletins.MarshalLayoutSubmission(draft, canonical)
	if err != nil {
		return result, err
	}
	if err = tx.QueryRowContext(ctx, `SELECT coalesce(max(revision),0)+1 FROM hhc_web.online_bulletin_revision WHERE document_id=$1`, result.DocumentID).Scan(&result.Revision); err != nil {
		return result, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_revision(document_id,issue_id,series,content_locale,revision,document_json,source_asset_checksum,template_version,extractor_version,content_hash,renderer_version,renderer_artifact_sha256,review_issues,created_by,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, result.DocumentID, draft.IssueID, draft.Series, draft.ContentLocale, result.Revision, documentJSON, draft.SourceAssetChecksum, draft.TemplateVersion, extractor, contentHash, draft.LayoutManifest.RendererVersion, draft.LayoutManifest.RendererArtifactSha256, issuesJSON, actor, now)
	if err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_document SET local_revision=$2 WHERE id=$1`, result.DocumentID, result.Revision); err != nil {
		return result, err
	}
	if result.LayoutJobID, err = queueOnlineLayout(ctx, tx, result.DocumentID, result.Revision, draft, canonical, now); err != nil {
		return result, err
	}
	result.Version++
	return result, nil
}

func (r *Repository) bulletinExtractorVersion() string {
	if r.llmBulletinExtraction {
		return onlinebulletins.RecognitionExtractorVersion()
	}
	return onlinebulletins.ExtractorVersion
}

func (r *Repository) QueueOnlineBulletinExtraction(ctx context.Context, issueID string, input onlinebulletins.QueueExtractionInput, actor string, now time.Time) (onlinebulletins.QueuedExtraction, error) {
	var result onlinebulletins.QueuedExtraction
	checksum, err := hex.DecodeString(input.SourceChecksum)
	if err != nil || len(checksum) != 32 || strings.ToLower(input.SourceChecksum) != input.SourceChecksum || input.CanonicalVersion < 1 || input.OnlineVersion < 0 || input.SourceAssetID == "" || len(input.SourceAssetID) > 255 || actor == "" || now.IsZero() {
		return result, bulletins.ErrInvalid
	}
	if err = r.validateBulletinAudit(ctx, actor); err != nil {
		return result, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	var failedUpload bool
	if input.Retry {
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='bulletin.online.ensure_extraction' AND status='failed' AND payload_json->>'assetId'=$2) AND NOT EXISTS(SELECT 1 FROM hhc_web.online_bulletin_extraction_job WHERE issue_id=$1 AND source_asset_id=$2 AND source_asset_checksum=$3 AND extractor_version=$4 AND status='failed' AND snapshot_json IS NULL AND NOT superseded)`, issueID, input.SourceAssetID, input.SourceChecksum, r.bulletinExtractorVersion()).Scan(&failedUpload)
		if err != nil {
			return result, err
		}
		if failedUpload {
			input.Retry = false
		}
	}
	result, err = r.queueOnlineBulletinExtractionTx(ctx, tx, issueID, input, actor, now)
	if err != nil {
		return result, err
	}
	if result.Job.Status != "failed" {
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='delivered',last_error='',updated_at=$3 WHERE aggregate_id=$1 AND event_type='bulletin.online.ensure_extraction' AND status='failed' AND payload_json->>'assetId'=$2`, issueID, input.SourceAssetID, now); err != nil {
			return result, err
		}
	}
	return result, tx.Commit()
}

func (r *Repository) queueOnlineBulletinExtractionTx(ctx context.Context, tx *sql.Tx, issueID string, input onlinebulletins.QueueExtractionInput, actor string, now time.Time) (onlinebulletins.QueuedExtraction, error) {
	var result onlinebulletins.QueuedExtraction
	var err error
	var canonicalVersion int64
	var sourceAssetID string
	var canonical onlinebulletins.CanonicalMetadata
	err = tx.QueryRowContext(ctx, `SELECT i.version,v.pdf_asset_id,v.title,v.subtitle,i.issue_number,i.issue_date::text FROM hhc_web.bulletin_issue i JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series='general' AND v.locale='zh-Hant' WHERE i.id=$1 AND i.issue_number>=1739 FOR UPDATE OF i,v`, issueID).Scan(&canonicalVersion, &sourceAssetID, &canonical.Title, &canonical.Subtitle, &canonical.IssueNumber, &canonical.Date)
	if errors.Is(err, sql.ErrNoRows) {
		return result, bulletins.ErrNotFound
	}
	if err != nil {
		return result, err
	}
	if canonicalVersion != input.CanonicalVersion || sourceAssetID != input.SourceAssetID {
		return result, bulletins.ErrPrecondition
	}
	err = tx.QueryRowContext(ctx, `SELECT id::text,version FROM hhc_web.online_bulletin_document WHERE issue_id=$1 AND series='general' AND content_locale='zh-Hant' FOR UPDATE`, issueID).Scan(&result.DocumentID, &result.Version)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return result, err
	}
	if result.Version != input.OnlineVersion {
		return result, bulletins.ErrPrecondition
	}
	if err = r.setAutomaticBulletinIntent(ctx, tx, issueID, "general", "zh-Hant", input.AutoPublish, input.NotifySubscribers, actor, now); err != nil {
		return result, err
	}
	if input.AutoPublish {
		if err = cancelPendingSimplifiedRelease(ctx, tx, issueID, now); err != nil {
			return result, err
		}
	}
	created := result.DocumentID == ""
	if created {
		if input.Retry {
			return result, bulletins.ErrConflict
		}
		result.DocumentID, result.Version = platform.NewID(), 1
		if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_document(id,issue_id,series,content_locale,created_at,updated_at) VALUES($1,$2,'general','zh-Hant',$3,$3)`, result.DocumentID, issueID, now); err != nil {
			return result, err
		}
	}
	var snapshot, noResult, metadataChanged bool
	var templateSnapshot, learningRules []byte
	err = tx.QueryRowContext(ctx, `SELECT id::text,status,attempts,last_error_code,snapshot_json IS NOT NULL,result_revision IS NULL,result_revision,COALESCE(snapshot_json->'canonicalMetadata',requested_metadata) IS DISTINCT FROM $5::jsonb FROM hhc_web.online_bulletin_extraction_job WHERE document_id=$1 AND source_asset_checksum=$2 AND template_version='v1' AND extractor_version=$3 AND source_asset_id=$4 AND NOT superseded FOR UPDATE`, result.DocumentID, input.SourceChecksum, r.bulletinExtractorVersion(), input.SourceAssetID, mustJSON(canonical)).Scan(&result.Job.ID, &result.Job.Status, &result.Job.Attempts, &result.Job.ErrorCode, &snapshot, &noResult, &result.Job.ResultRevision, &metadataChanged)
	if err == nil {
		var epochChanged bool
		if err = tx.QueryRowContext(ctx, `SELECT v.online_intent_epoch>0 AND (e.publication_intent_id IS DISTINCT FROM v.online_intent_id OR e.publication_intent_epoch IS DISTINCT FROM v.online_intent_epoch) FROM hhc_web.bulletin_version v JOIN hhc_web.online_bulletin_extraction_job e ON e.id=$1 WHERE v.issue_id=e.issue_id AND v.series=e.series AND v.locale=e.content_locale`, result.Job.ID).Scan(&epochChanged); err != nil {
			return result, err
		}
		if input.AutoPublish && epochChanged {
			// A new authorization gets a new job identity. Never rewrite the original
			// author's request/evidence or reuse its in-flight lease as fresh authority.
			result.Job, input.Retry = onlinebulletins.AdminJob{}, false
		} else if metadataChanged && (input.AutoPublish || result.Job.Status == "review_ready" && snapshot) {
			// Recheck corrected metadata without modifying the old immutable evidence.
			result.Job, input.Retry = onlinebulletins.AdminJob{}, false
		} else if input.Retry && result.Job.Status == "failed" && r.llmBulletinExtraction {
			var changed bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.online_bulletin_recognition_checkpoint WHERE job_id=$1 AND phase='verified' AND checkpoint_json->'canonical' IS DISTINCT FROM $2::jsonb)`, result.Job.ID, mustJSON(canonical)).Scan(&changed); err != nil {
				return result, err
			}
			if changed {
				result.Job.ID, input.Retry = "", false
			}
		} else if input.AutoPublish && result.Job.Status == "review_ready" && snapshot {
			// A fresh job reuses immutable source proof under the new request.
			result.Job, input.Retry = onlinebulletins.AdminJob{}, false
		} else if !input.Retry {
			if input.AutoPublish {
				p, _ := auditclient.ProvenanceFromContext(ctx)
				if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_extraction_job SET auto_publish=true,authorizer_id=$2,authorizer_request_id=$3 WHERE id=$1`, result.Job.ID, actor, p.RequestID); err != nil {
					return result, err
				}
				if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_extraction_job e SET publication_intent_id=v.online_intent_id,publication_intent_epoch=NULLIF(v.online_intent_epoch,0) FROM hhc_web.bulletin_version v WHERE e.id=$1 AND v.issue_id=e.issue_id AND v.series=e.series AND v.locale=e.content_locale`, result.Job.ID); err != nil {
					return result, err
				}
				if err = r.enqueueBulletinAudit(ctx, tx, "cms.bulletin.online.extract", result.DocumentID, actor, now); err != nil {
					return result, err
				}
			}
			return result, nil
		} else if result.Job.Status == "review_ready" && snapshot && noResult {
			// A blocked snapshot stays immutable; an explicit retry creates fresh evidence.
			result.Job.ID, input.Retry = "", false
		} else if result.Job.Status != "failed" || snapshot {
			return result, bulletins.ErrConflict
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return result, err
	} else if input.Retry {
		// A failed previous parser may be retried as a new current-version job.
		var previous, previousStatus string
		err = tx.QueryRowContext(ctx, `SELECT id::text,template_snapshot_json,learning_rules_json,status FROM hhc_web.online_bulletin_extraction_job WHERE document_id=$1 AND source_asset_checksum=$2 AND template_version='v1' AND extractor_version<>$3 AND source_asset_id=$4 AND ((status='failed' AND snapshot_json IS NULL) OR (status='review_ready' AND result_revision IS NULL)) AND NOT superseded ORDER BY created_at DESC,id DESC LIMIT 1 FOR UPDATE`, result.DocumentID, input.SourceChecksum, r.bulletinExtractorVersion(), input.SourceAssetID).Scan(&previous, &templateSnapshot, &learningRules, &previousStatus)
		if errors.Is(err, sql.ErrNoRows) {
			return result, bulletins.ErrConflict
		}
		if err != nil {
			return result, err
		}
		if previousStatus == "review_ready" || r.llmBulletinExtraction {
			input.Retry = false
		}
	}
	// A parser upgrade must not cancel a worker still processing this source.
	var active bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.online_bulletin_extraction_job WHERE document_id=$1 AND source_asset_id=$2 AND source_asset_checksum=$3 AND NOT superseded AND status IN ('queued','parsing'))`, result.DocumentID, input.SourceAssetID, input.SourceChecksum).Scan(&active); err != nil {
		return result, err
	}
	if active && !input.AutoPublish {
		return result, bulletins.ErrConflict
	}
	// Fence every previous lease. Re-uploads never change Local, Base, Incoming,
	// or the published pointer; only successful extraction produces Incoming.
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_extraction_job SET superseded=true,lease_token=NULL,lease_until=NULL,updated_at=$2 WHERE document_id=$1 AND NOT superseded`, result.DocumentID, now); err != nil {
		return result, err
	}
	if result.Job.ID == "" {
		if !input.Retry {
			templateSnapshot = mustJSON(onlinebulletins.ConversionTemplateSettings())
			learningRules = []byte("[]")
			if !r.llmBulletinExtraction {
				learningRules, err = loadActiveLearningRules(ctx, tx)
				if err != nil {
					return result, err
				}
			}
		}
		result.Job.ID = platform.NewID()
		var authorizer, request any
		if p, ok := auditclient.ProvenanceFromContext(ctx); ok && p.ActorID == actor && auditclient.ValidActorRequest(p.ActorID, p.RequestID) {
			authorizer, request = p.ActorID, p.RequestID
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.online_bulletin_extraction_job(id,document_id,issue_id,series,content_locale,source_asset_id,source_asset_checksum,template_version,extractor_version,available_at,created_at,updated_at,template_snapshot_json,learning_rules_json,authorizer_id,authorizer_request_id,auto_publish,requested_metadata) VALUES($1,$2,$3,'general','zh-Hant',$4,$5,'v1',$6,$7,$7,$7,$8,$9,$10,$11,$12,$13)`, result.Job.ID, result.DocumentID, issueID, input.SourceAssetID, input.SourceChecksum, r.bulletinExtractorVersion(), now, templateSnapshot, learningRules, authorizer, request, input.AutoPublish, mustJSON(canonical))
	} else {
		p, _ := auditclient.ProvenanceFromContext(ctx)
		var authorizer any
		if input.AutoPublish {
			authorizer = actor
		}
		_, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_extraction_job SET status='queued',attempts=0,last_error_code='',superseded=false,source_asset_id=$2,available_at=$3,updated_at=$3,auto_publish=$4,authorizer_id=CASE WHEN $4 THEN $5::text ELSE authorizer_id END,authorizer_request_id=CASE WHEN $4 THEN $6 ELSE authorizer_request_id END WHERE id=$1`, result.Job.ID, input.SourceAssetID, now, input.AutoPublish, authorizer, p.RequestID)
	}
	if err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_extraction_job e SET publication_intent_id=v.online_intent_id,publication_intent_epoch=NULLIF(v.online_intent_epoch,0) FROM hhc_web.bulletin_version v WHERE e.id=$1 AND v.issue_id=e.issue_id AND v.series=e.series AND v.locale=e.content_locale`, result.Job.ID); err != nil {
		return result, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.bulletin_version SET pdf_sha256=$2 WHERE issue_id=$1 AND series='general' AND locale='zh-Hant'`, issueID, input.SourceChecksum); err != nil {
		return result, err
	}
	if !created {
		if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_document SET version=version+1,draft_status=CASE WHEN local_revision IS NULL THEN 'extracting' ELSE draft_status END,updated_at=$2 WHERE id=$1`, result.DocumentID, now); err != nil {
			return result, err
		}
		result.Version++
	}
	if err = r.enqueueBulletinAudit(ctx, tx, "cms.bulletin.online.extract", result.DocumentID, actor, now); err != nil {
		return result, err
	}
	if err = enqueueOnlineBulletinWake(ctx, tx, result.Job.ID, now); err != nil {
		return result, err
	}
	result.Job.Status, result.Job.Attempts, result.Job.ErrorCode = "queued", 0, ""
	return result, nil
}

func (r *Repository) OnlineBulletinState(ctx context.Context, issueID, series, locale string) (onlinebulletins.AdminState, error) {
	state := onlinebulletins.AdminState{IssueID: issueID, Series: series, ContentLocale: locale}
	var local, publication, extraction, layout, conversion, derivation, automation, production, derivedPublication, notification []byte
	// One statement gives a consistent MVCC snapshot, including in-flight worker
	// changes, without locking the editor's read against the worker or publisher.
	err := r.db.QueryRowContext(ctx, `SELECT i.version,v.title,v.subtitle,i.issue_number,i.issue_date::text,v.status,
	 COALESCE(d.id::text,''),COALESCE(d.version,0),COALESCE(d.draft_status,'notStarted'),d.base_revision,d.incoming_revision,d.published_revision,COALESCE(d.metadata_sync_pending,false),
	 CASE WHEN rev.revision IS NOT NULL THEN jsonb_build_object('revision',rev.revision,'document',rev.document_json,'reviewIssues',rev.review_issues) END,
	 pub.canonical_metadata,
	 EXISTS(SELECT 1 FROM hhc_web.online_bulletin_confirmation c WHERE c.document_id=d.id AND c.revision=d.local_revision AND c.layout_validation_hash=rev.layout_validation_hash AND c.canonical_metadata=jsonb_build_object('title',v.title,'subtitle',v.subtitle,'issueNumber',i.issue_number,'date',i.issue_date::text)),
	 COALESCE(
	  (SELECT jsonb_build_object('id',e.id,'status',CASE WHEN e.status='failed' THEN 'failed' ELSE 'queued' END,'attempts',e.attempts,'errorCode',CASE WHEN e.last_error ~ '^[a-z0-9_]{1,64}$' THEN e.last_error ELSE CASE WHEN e.status='failed' THEN 'upload_request_failed' ELSE 'upload_source_pending' END END,'reviewIssues','[]'::jsonb)
	   FROM (SELECT * FROM hhc_web.outbox_event WHERE aggregate_id=i.id AND event_type='bulletin.online.ensure_extraction' AND payload_json->>'assetId'=v.pdf_asset_id ORDER BY aggregate_version DESC LIMIT 1) e
	   WHERE e.status IN ('pending','processing','failed') AND v.series='general' AND v.locale='zh-Hant' AND i.issue_number>=1739),
	  (SELECT jsonb_build_object('id',j.id,'status',j.status,'attempts',j.attempts,'errorCode',j.last_error_code,'resultRevision',j.result_revision,'reviewIssues',COALESCE(j.snapshot_json->'issues','[]'::jsonb))
	   FROM hhc_web.online_bulletin_extraction_job j WHERE j.document_id=d.id AND j.source_asset_id=v.pdf_asset_id AND NOT j.superseded ORDER BY j.created_at DESC,j.id DESC LIMIT 1)),
	 (SELECT jsonb_build_object('id',j.id,'status',j.status,'attempts',j.attempts,'errorCode',j.last_error_code,'reviewIssues',CASE WHEN j.status='ready' THEN '[]'::jsonb ELSE j.review_issues END)
	  FROM hhc_web.online_bulletin_layout_job j WHERE j.document_id=d.id AND j.status<>'superseded'
	  AND (j.source_revision=d.local_revision OR j.result_revision=d.local_revision) ORDER BY j.created_at DESC,j.id DESC LIMIT 1),
    cj.job,cj.derivation,
     CASE WHEN v.series='general' AND v.locale='zh-Hant' THEN jsonb_build_object('autoPublish',v.auto_publish,'update',uj.job,'summary',COALESCE(uj.summary,''),'changeKind',COALESCE(uj.change_kind,''),'disposition',`+dispositionJSON+`) END, `+onlineBulletinProductionProjectionSQL+`, `+simplifiedPublicationProjectionSQL+`, `+initialNotificationProjectionSQL+`
	 FROM hhc_web.bulletin_issue i
	 JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series=$2 AND v.locale=$3
	 LEFT JOIN hhc_web.online_bulletin_document d ON d.issue_id=i.id AND d.series=v.series AND d.content_locale=v.locale
	 LEFT JOIN hhc_web.online_bulletin_revision rev ON rev.document_id=d.id AND rev.revision=d.local_revision
	 LEFT JOIN hhc_web.online_bulletin_publication pub ON pub.document_id=d.id AND pub.revision=d.published_revision
	 LEFT JOIN LATERAL (
	  SELECT jsonb_build_object('id',u.id,'status',u.status,'attempts',u.attempts,'errorCode',u.last_error_code) AS job,u.evidence_json#>>'{plan,summary}' AS summary,u.evidence_json#>>'{plan,changeKind}' AS change_kind
	  FROM hhc_web.online_bulletin_update_job u JOIN hhc_web.online_bulletin_extraction_job e ON e.id=u.extraction_job_id
	  WHERE u.document_id=d.id AND e.source_asset_id=v.pdf_asset_id AND e.source_asset_checksum=v.pdf_sha256 AND NOT e.superseded ORDER BY u.created_at DESC,u.id DESC LIMIT 1
	 ) uj ON true
	 LEFT JOIN LATERAL (
	  SELECT jsonb_build_object('id',j.id,'status',j.status,'attempts',j.attempts,'errorCode',j.last_error_code,'reviewIssues',CASE WHEN j.status='review_ready' THEN '[]'::jsonb ELSE j.review_issues END) AS job,
	   jsonb_build_object('sourceDocumentId',j.source_document_id,'sourceRevision',j.source_revision,'sourceContentHash',j.source_content_hash,
	    'sourceMetadataVersion',j.requested_canonical_version,'converterVersion',j.converter_version,'resultRevision',j.result_revision,'convertedMetadata',j.converted_metadata,
	    'stale',CASE WHEN j.publication_event_id IS NOT NULL THEN sd.published_revision IS DISTINCT FROM j.source_revision OR sp.canonical_metadata IS DISTINCT FROM j.source_metadata ELSE sd.local_revision IS DISTINCT FROM j.source_revision OR j.source_metadata IS DISTINCT FROM jsonb_build_object('title',sv.title,'subtitle',sv.subtitle,'issueNumber',i.issue_number,'date',i.issue_date::text) END OR sr.content_hash IS DISTINCT FROM j.source_content_hash) AS derivation
	  FROM hhc_web.online_bulletin_conversion_job j
	  LEFT JOIN hhc_web.online_bulletin_document sd ON sd.id=j.source_document_id
	  LEFT JOIN hhc_web.online_bulletin_revision sr ON sr.document_id=sd.id AND sr.revision=CASE WHEN j.publication_event_id IS NULL THEN sd.local_revision ELSE sd.published_revision END
	  LEFT JOIN hhc_web.online_bulletin_publication sp ON sp.document_id=sd.id AND sp.revision=sd.published_revision
	  LEFT JOIN hhc_web.bulletin_version sv ON sv.issue_id=i.id AND sv.series='general' AND sv.locale='zh-Hant'
	  WHERE j.document_id=d.id AND j.status<>'superseded' ORDER BY j.created_at DESC,j.id DESC LIMIT 1
	 ) cj ON true
	 WHERE i.id=$1`, issueID, series, locale, r.llmBulletinExtraction).Scan(&state.CanonicalVersion, &state.CanonicalMetadata.Title, &state.CanonicalMetadata.Subtitle, &state.CanonicalMetadata.IssueNumber, &state.CanonicalMetadata.Date, &state.PDFStatus,
		&state.DocumentID, &state.Version, &state.DraftStatus, &state.BaseRevision, &state.IncomingRevision, &state.PublishedRevision, &state.MetadataSyncPending, &local, &publication, &state.Confirmed, &extraction, &layout, &conversion, &derivation, &automation, &production, &derivedPublication, &notification)
	if errors.Is(err, sql.ErrNoRows) {
		return state, bulletins.ErrNotFound
	}
	if err != nil {
		return state, err
	}
	for _, value := range []struct {
		raw []byte
		to  any
	}{{local, &state.Local}, {publication, &state.PublishedMetadata}, {extraction, &state.Extraction}, {layout, &state.Layout}, {conversion, &state.Conversion}, {derivation, &state.Derivation}, {automation, &state.Automation}, {production, &state.Production}, {derivedPublication, &state.DerivedPublication}, {notification, &state.Notification}} {
		if len(value.raw) > 0 {
			if err = json.Unmarshal(value.raw, value.to); err != nil {
				return state, err
			}
		}
	}
	if state.Notification != nil && !initialNotificationEvidenceKnown(notification) {
		state.Notification.Status = "unknown"
	}
	state.Capabilities = &onlinebulletins.AdminCapabilities{ExplicitOnlinePublication: r.explicitOnlinePublication, QualifiedDraftRecovery: r.explicitOnlinePublication, SimplifiedPublicationRecovery: r.simplifiedPublicationRecovery}
	// The last attempt can fail without a revision ever having been produced.
	if state.Local == nil && state.Extraction != nil && (state.Extraction.Status == "failed" || state.Extraction.Status == "review_ready") {
		state.DraftStatus = "extractionFailed"
	} else if state.Local == nil && state.Extraction != nil && state.Extraction.Status == "queued" {
		state.DraftStatus = "extracting"
	}
	return state, nil
}
