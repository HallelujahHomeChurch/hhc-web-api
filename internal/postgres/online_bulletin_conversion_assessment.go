package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func (r *Repository) SaveOnlineBulletinConversionAssessment(ctx context.Context, job onlinebulletins.ConversionJob, input onlinebulletins.UpdateInput, attempt onlinebulletins.UpdateAttempt, now time.Time) error {
	hash, err := onlinebulletins.UpdateInputSHA256(input)
	if err != nil || now.IsZero() || job.PublicationEventID == "" || input.DerivedSource == nil || input.DerivedMetadata == nil || input.Canonical.IssueID != job.IssueID || input.Local != nil || input.Base != nil || input.Published != nil || attempt.InputSHA256 != hash || attempt.ProtocolSHA256 != onlinebulletins.UpdateProtocolSHA256() || (attempt.ErrorCode != "" && attempt.ErrorCode != "update_plan_failed") {
		return onlinebulletins.ErrUpdatePlan
	}
	raw, err := json.Marshal(attempt)
	if err != nil || len(raw) > 4<<20 {
		return onlinebulletins.ErrUpdatePlan
	}
	var localMetadata any
	if input.LocalMetadata != nil {
		localMetadata = mustJSON(input.LocalMetadata)
	}
	result, err := r.db.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_conversion_job j SET assessment_json=assessment_json || jsonb_build_array($3::jsonb),updated_at=$4
 WHERE id=$1 AND lease_token=$2 AND status='converting' AND lease_until>$4 AND publication_event_id=$5 AND jsonb_array_length(assessment_json)<6
 AND source_metadata=$7::jsonb AND (initializes_metadata AND $8::jsonb IS NULL OR NOT initializes_metadata AND target_metadata=$8::jsonb)
 AND EXISTS(SELECT 1 FROM hhc_web.online_bulletin_document sd
 JOIN hhc_web.online_bulletin_revision sr ON sr.document_id=sd.id AND sr.revision=sd.published_revision
 JOIN hhc_web.online_bulletin_publication sp ON sp.document_id=sd.id AND sp.revision=sd.published_revision
 JOIN hhc_web.online_bulletin_document td ON td.id=j.document_id
 JOIN hhc_web.bulletin_version tv ON tv.issue_id=j.issue_id AND tv.series='general' AND tv.locale='zh-Hans'
 JOIN hhc_web.outbox_event event ON event.id=j.publication_event_id
 WHERE sd.id=j.source_document_id AND sd.published_revision=j.source_revision AND sr.content_hash=j.source_content_hash AND sr.document_json=$6::jsonb
 AND sp.canonical_metadata=j.source_metadata AND td.version=j.target_online_version AND tv.title=j.target_metadata->>'title' AND tv.subtitle=j.target_metadata->>'subtitle'
 AND event.status IN ('pending','processing') AND event.event_type='bulletin.online.publish_simplified')`, job.ID, job.LeaseToken, raw, now, job.PublicationEventID, mustJSON(input.DerivedSource), mustJSON(input.DerivedMetadata), localMetadata)
	return updateLeaseResult(result, err)
}

func successfulAssessmentAttempts(attempts []onlinebulletins.UpdateAttempt) []onlinebulletins.UpdateAttempt {
	var result []onlinebulletins.UpdateAttempt
	for _, attempt := range attempts {
		if attempt.ErrorCode == "" {
			result = append(result, attempt)
		}
	}
	return result
}
