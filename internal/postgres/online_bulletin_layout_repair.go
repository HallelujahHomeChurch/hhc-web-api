package postgres

import (
	"context"
	"encoding/json"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

const automaticLayoutSource = ` FROM hhc_web.online_bulletin_update_job u
 JOIN hhc_web.online_bulletin_extraction_job e ON e.id=u.extraction_job_id
 JOIN hhc_web.online_bulletin_document d ON d.id=u.document_id
 JOIN hhc_web.bulletin_issue i ON i.id=d.issue_id
 JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series=d.series AND v.locale=d.content_locale
 WHERE u.document_id=j.document_id AND u.result_revision=j.source_revision AND u.status='ready'
 AND d.local_revision=j.source_revision AND e.auto_publish AND v.auto_publish AND NOT e.superseded
 AND (v.online_intent_epoch=0 OR e.publication_intent_id=v.online_intent_id AND e.publication_intent_epoch=v.online_intent_epoch AND (SELECT t.status IN ('authorized','running') FROM hhc_web.online_bulletin_intent_transition t WHERE t.intent_id=v.online_intent_id ORDER BY t.sequence DESC LIMIT 1))
 AND e.authorizer_request_id=v.auto_publish_request AND e.source_asset_id=v.pdf_asset_id AND e.source_asset_checksum=v.pdf_sha256
 AND j.canonical_metadata=jsonb_build_object('title',v.title,'subtitle',v.subtitle,'issueNumber',i.issue_number,'date',i.issue_date::text)`

func (r *Repository) SaveOnlineBulletinLayoutRepair(ctx context.Context, job onlinebulletins.LayoutJob, evidence onlinebulletins.LayoutRepairEvidence, now time.Time) error {
	if now.IsZero() || !onlinebulletins.ValidLayoutRepairAttempt(job, evidence) {
		return onlinebulletins.ErrLayoutJob
	}
	raw, err := json.Marshal(evidence)
	if err != nil || len(raw) > 256<<10 {
		return onlinebulletins.ErrLayoutJob
	}
	result, err := r.db.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_layout_job j SET repair_json=$3,updated_at=$4 WHERE id=$1 AND lease_token=$2 AND status='validating' AND lease_until>$4 AND content_hash=$5 AND source_revision=$6 AND (repair_json IS NULL OR repair_json=$3::jsonb) AND EXISTS(SELECT 1`+automaticLayoutSource+`)`, job.ID, job.LeaseToken, raw, now, job.ContentHash, job.Revision)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return onlinebulletins.ErrLayoutLease
	}
	return nil
}
