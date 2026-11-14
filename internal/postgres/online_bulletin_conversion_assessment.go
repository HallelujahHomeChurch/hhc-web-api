package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

// A completed legacy/manual conversion is not semantic publication evidence.
func validConversionAssessmentTx(ctx context.Context, tx *sql.Tx, jobID string) (bool, error) {
	metadata, err := conversionAssessmentMetadataTx(ctx, tx, jobID)
	return metadata != nil, err
}

// The verified assessment selects either generated or preserved local metadata.
// Recovery must publish the exact choice that was subsequently measured.
func conversionAssessmentMetadataTx(ctx context.Context, tx *sql.Tx, jobID string) (*onlinebulletins.CanonicalMetadata, error) {
	var sourceJSON, sourceMetadataJSON, targetJSON, convertedJSON, localMetadataJSON, attemptsJSON []byte
	var initializes bool
	err := tx.QueryRowContext(ctx, `SELECT sr.document_json,j.source_metadata,tr.document_json,j.converted_metadata,j.target_metadata,j.initializes_metadata,j.assessment_json
 FROM hhc_web.online_bulletin_conversion_job j
 JOIN hhc_web.online_bulletin_revision sr ON sr.document_id=j.source_document_id AND sr.revision=j.source_revision
 JOIN hhc_web.online_bulletin_revision tr ON tr.document_id=j.document_id AND tr.revision=j.result_revision
 WHERE j.id=$1`, jobID).Scan(&sourceJSON, &sourceMetadataJSON, &targetJSON, &convertedJSON, &localMetadataJSON, &initializes, &attemptsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var source, target onlinebulletins.Document
	var metadata, converted, local onlinebulletins.CanonicalMetadata
	var attempts []onlinebulletins.UpdateAttempt
	if json.Unmarshal(sourceJSON, &source) != nil || json.Unmarshal(targetJSON, &target) != nil || json.Unmarshal(sourceMetadataJSON, &metadata) != nil || json.Unmarshal(convertedJSON, &converted) != nil || json.Unmarshal(localMetadataJSON, &local) != nil || json.Unmarshal(attemptsJSON, &attempts) != nil {
		return nil, nil
	}
	input, err := onlinebulletins.SimplifiedAssessmentInput(source, metadata, target, converted)
	if err != nil {
		return nil, nil
	}
	if !initializes {
		input.LocalMetadata = &local
	}
	proof, err := onlinebulletins.CompletedUpdateEvidence(input, successfulAssessmentAttempts(attempts))
	if err != nil {
		return nil, nil
	}
	if proof.Plan.MetadataChoice == "local" {
		return &local, nil
	}
	return &converted, nil
}
