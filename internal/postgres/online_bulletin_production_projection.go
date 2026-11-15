package postgres

import (
	"fmt"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

// References only the existing AdminState aliases i and v. Every value in this
// expression is metadata, a count, an allowlisted lifecycle value or a timestamp.
// The parameters below are fixed, repository-owned protocol identifiers.
var onlineBulletinProductionProjectionSQL = fmt.Sprintf(`(
 SELECT jsonb_build_object(
 'sourceIdentity',encode(sha256(convert_to(jsonb_build_array(i.id,v.series,v.locale,j.source_asset_checksum,jsonb_build_object('title',v.title,'subtitle',v.subtitle,'issueNumber',i.issue_number,'date',i.issue_date::text))::text,'UTF8')),'hex'),
 'jobId',j.id,'attemptId',COALESCE(j.production_attempt_id::text,j.id::text||':queued'),'progressVersion',j.production_revision,
 'phase',CASE WHEN bound.ok THEN j.production_phase ELSE 'scan' END,
 'status',CASE WHEN j.superseded THEN 'superseded' WHEN j.status='failed' THEN 'failed' WHEN j.status='review_ready' THEN 'completed' WHEN j.status='queued' AND j.attempts=0 THEN 'queued' WHEN j.status='queued' OR j.lease_until<=statement_timestamp() THEN 'waiting' ELSE 'running' END,
 'totalPages',CASE WHEN bound.ok THEN j.production_total_pages END,
 'currentPage',CASE WHEN bound.ok AND j.status='parsing' AND NOT j.superseded AND j.lease_until>statement_timestamp() THEN j.production_current_page END,
 'verifiedPages',CASE WHEN NOT bound.ok THEN 0 WHEN reused.ok THEN j.production_total_pages ELSE prefix.verified_pages END,
 'verifiedReuseCount',CASE WHEN NOT bound.ok THEN 0 WHEN reused.ok THEN j.production_total_pages ELSE prefix.reused_pages END,
 'subphase',CASE WHEN bound.ok THEN j.production_subphase ELSE '' END,
 'startedAt',j.production_started_at,'lastProgressAt',CASE WHEN bound.ok THEN j.production_last_progress_at END,
 'heartbeatAt',j.production_heartbeat_at,'observedAt',statement_timestamp())
 FROM (SELECT current_job.* FROM hhc_web.online_bulletin_extraction_job current_job
  WHERE current_job.issue_id=i.id AND current_job.series=v.series AND current_job.content_locale=v.locale
   AND current_job.source_asset_id=v.pdf_asset_id AND current_job.source_asset_checksum=v.pdf_sha256 AND current_job.extractor_version='%[4]s'
  ORDER BY current_job.superseded,current_job.created_at DESC,current_job.id DESC LIMIT 1) j
 LEFT JOIN LATERAL (SELECT COALESCE(j.production_protocol_sha256='%[1]s' AND j.production_canonical=jsonb_build_object('title',v.title,'subtitle',v.subtitle,'issueNumber',i.issue_number,'date',i.issue_date::text),false) AS ok) bound ON true
 LEFT JOIN LATERAL hhc_web.online_bulletin_production_prefix(j.id,'%[1]s','%[2]s','%[3]s',jsonb_build_object('title',v.title,'subtitle',v.subtitle,'issueNumber',i.issue_number,'date',i.issue_date::text)) prefix ON true
 LEFT JOIN hhc_web.online_bulletin_extraction_job prior ON prior.id=j.recognition_reused_from
 LEFT JOIN LATERAL (SELECT COALESCE(bound.ok AND j.production_reused_snapshot_sha256=prior.snapshot_sha256
  AND prior.source_asset_checksum=j.source_asset_checksum AND prior.extractor_version=j.extractor_version
  AND prior.template_snapshot_json=j.template_snapshot_json
  AND prior.snapshot_json->'canonicalMetadata'=j.production_canonical
  AND prior.snapshot_json#>>'{candidate,recognition,protocolSha256}'='%[1]s'
  AND prior.snapshot_json#>>'{candidate,recognition,deployment}'='%[2]s'
  AND prior.snapshot_json#>>'{candidate,recognition,modelVersion}'='%[3]s',false) AS ok) reused ON true
)`, onlinebulletins.RecognitionProtocolSHA256(), onlinebulletins.RecognitionDeployment, onlinebulletins.RecognitionModelVersion, onlinebulletins.RecognitionExtractorVersion())
