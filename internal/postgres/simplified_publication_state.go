package postgres

import (
	"encoding/json"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
)

// Part of OnlineBulletinState's single MVCC snapshot. No worker leases, raw
// errors, authorizers, source content or notification delivery claims are exposed.
const simplifiedPublicationProjectionSQL = `(SELECT jsonb_build_object(
 'operationId',e.id,
 'recovery',` + simplifiedRecoverySummaryProjectionSQL + `,
 'source',jsonb_build_object('documentId',e.aggregate_id,'revision',e.aggregate_version,'contentHash',o.source_content_hash,'metadataHash',o.source_metadata_hash),
 'sourceCurrent',CASE WHEN sd.published_revision IS DISTINCT FROM e.aggregate_version THEN false WHEN o.source_content_hash IS NULL OR o.source_metadata_hash IS NULL THEN NULL ELSE sr.content_hash=o.source_content_hash AND encode(sha256(convert_to(sp.canonical_metadata::text,'UTF8')),'hex')=o.source_metadata_hash END,
 'status',CASE WHEN e.last_error='publication_authority_superseded' THEN 'superseded' WHEN e.last_error='publication_withdrawn' THEN 'cancelled' WHEN e.last_error='source_publication_changed' THEN 'superseded' WHEN e.status='delivered' THEN 'completed' WHEN e.status IN('pending','processing','failed') THEN e.status ELSE 'unknown' END,
 'phase',CASE WHEN e.status='delivered' THEN 'complete' WHEN e.status='failed' THEN COALESCE(o.failures_json->0->>'phase','unknown') WHEN c.id IS NULL THEN 'waiting' WHEN c.status<>'review_ready' THEN 'conversion' WHEN $4 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements(c.assessment_json) a WHERE COALESCE(a->>'errorCode','')='' AND a->'plan'->>'readiness'='ready') THEN 'assessment' WHEN l.status IS DISTINCT FROM 'ready' THEN 'layout' ELSE 'publication' END,
 'conversionJobId',c.id,'resultRevision',l.result_revision,
 'attempts',CASE WHEN o.coverage='complete' THEN e.attempts END,
 'failureCount',CASE WHEN o.operation_id IS NOT NULL THEN o.failure_count END,
 'createdAt',e.created_at,'updatedAt',e.updated_at,
 'nextAttemptAt',CASE WHEN e.status='pending' THEN e.next_attempt_at END,
 'errorCode',CASE WHEN e.last_error IN('simplified_publication_failed','simplified_conversion_failed','simplified_layout_failed','simplified_target_changed','simplified_review_required','simplified_mapping_review_required','source_publication_changed','publication_withdrawn','stale_metadata','publication_authority_superseded') THEN e.last_error WHEN COALESCE(e.last_error,'')<>'' THEN 'simplified_publication_failed' ELSE '' END,
 'lastFailure',o.last_failure_json,
 'history',COALESCE(o.failures_json,'[]'::jsonb),'hasMore',COALESCE(o.history_count>20,false),'historyCoverage',COALESCE(o.coverage,'unknown'))
 FROM hhc_web.outbox_event e
 JOIN hhc_web.online_bulletin_document sd ON sd.id=e.aggregate_id AND sd.issue_id=i.id AND sd.series='general' AND sd.content_locale='zh-Hant'
 LEFT JOIN hhc_web.online_bulletin_revision sr ON sr.document_id=sd.id AND sr.revision=sd.published_revision
 LEFT JOIN hhc_web.online_bulletin_publication sp ON sp.document_id=sd.id AND sp.revision=sd.published_revision
 LEFT JOIN hhc_web.online_bulletin_simplified_operation o ON o.operation_id=e.id
 LEFT JOIN hhc_web.online_bulletin_conversion_job c ON c.id::text=e.payload_json->>'jobId' AND c.publication_event_id=e.id
 LEFT JOIN LATERAL(SELECT j.status,j.result_revision FROM hhc_web.online_bulletin_layout_job j WHERE j.document_id=c.document_id AND j.source_revision=c.result_revision ORDER BY j.created_at DESC,j.id DESC LIMIT 1) l ON true
 WHERE v.series='general' AND v.locale='zh-Hans' AND e.event_type='bulletin.online.publish_simplified' AND (o.source_publication_version IS NOT NULL AND NOT EXISTS(SELECT 1 FROM hhc_web.outbox_event other LEFT JOIN hhc_web.online_bulletin_simplified_operation other_op ON other_op.operation_id=other.id WHERE other.aggregate_id=e.aggregate_id AND other.event_type=e.event_type AND other.id<>e.id AND other_op.source_publication_version IS NULL AND (other.aggregate_version=e.aggregate_version OR e.aggregate_version IS DISTINCT FROM sd.published_revision)) OR o.source_publication_version IS NULL AND NOT EXISTS(SELECT 1 FROM hhc_web.outbox_event other WHERE other.aggregate_id=e.aggregate_id AND other.event_type=e.event_type AND other.id<>e.id AND (other.aggregate_version=e.aggregate_version OR e.aggregate_version IS DISTINCT FROM sd.published_revision))) ORDER BY o.source_publication_version DESC NULLS LAST,e.created_at DESC,e.id DESC LIMIT 1)`

// aggregate_version is deliberately never used: automatic and manual commands
// overload it differently. Generation zero is the initial command for a series.
const initialNotificationProjectionSQL = `(SELECT jsonb_build_object('id',n.id,'generation',0,
 '_actorId',COALESCE(n.payload_json->>'actorId',''),'_requestId',COALESCE(n.payload_json->>'requestId',''),
 '_idempotencyKey',COALESCE(n.payload_json->>'idempotencyKey',''),'_sourceIssueId',n.payload_json#>>'{source,issueId}','_sourceSeries',COALESCE(n.payload_json#>>'{source,series}',''),
 '_shapeKnown',jsonb_typeof(n.payload_json->'actorId')='string' AND jsonb_typeof(n.payload_json->'requestId')='string' AND jsonb_typeof(n.payload_json->'idempotencyKey')='string'
 AND jsonb_typeof(n.payload_json->'name')='string' AND n.payload_json->>'name'<>''
 AND CASE WHEN jsonb_typeof(n.payload_json->'notifications')='array' THEN jsonb_array_length(n.payload_json->'notifications') BETWEEN 1 AND 2 ELSE false END,
 'status',CASE WHEN n.status='delivered' THEN 'accepted' WHEN n.status IN('pending','processing','failed') THEN n.status ELSE 'unknown' END,
 'attempts',n.attempts,'errorCode',CASE WHEN COALESCE(n.last_error,'')<>'' OR n.status='failed' THEN 'notification_submission_failed' ELSE '' END,
 'canonicalVersion',NULL,'createdAt',n.created_at,'updatedAt',n.updated_at,'nextAttemptAt',CASE WHEN n.status='pending' THEN n.next_attempt_at END)
 FROM hhc_web.outbox_event n WHERE n.destination='engagement-api' AND n.event_type='bulletin.notification.submit' AND n.aggregate_id=i.id AND n.payload_json#>>'{source,kind}'='bulletin' AND lower(n.payload_json#>>'{source,issueId}')=i.id::text AND COALESCE(NULLIF(n.payload_json#>>'{source,series}',''),'general')=v.series AND n.payload_json#>>'{source,notificationGeneration}'='0' ORDER BY n.created_at,n.id LIMIT 1)`

// Private projection evidence never belongs to the wire type. A legacy row with
// incomplete request identity cannot establish downstream command acceptance.
func initialNotificationEvidenceKnown(raw []byte) bool {
	var proof struct {
		ActorID    string `json:"_actorId"`
		RequestID  string `json:"_requestId"`
		Key        string `json:"_idempotencyKey"`
		IssueID    string `json:"_sourceIssueId"`
		Series     string `json:"_sourceSeries"`
		ShapeKnown bool   `json:"_shapeKnown"`
	}
	if json.Unmarshal(raw, &proof) != nil || !proof.ShapeKnown || !auditclient.ValidActorRequest(proof.ActorID, proof.RequestID) || len(proof.Key) > 200 {
		return false
	}
	expected := "notification:v2:bulletin:" + proof.IssueID
	// Engagement's recognized legacy General contract omits series and :g0.
	if proof.Series != "" {
		if proof.Series != "general" && proof.Series != "children" {
			return false
		}
		expected += ":" + proof.Series + ":g0"
	}
	return proof.Key == expected
}

// Target publication versions are causal under the issue lock; wall clocks and
// random receipt IDs are not. Guard every legacy JSON numeric conversion.
const simplifiedRecoverySummaryProjectionSQL = `(SELECT jsonb_build_object('authorizationId',a.id,'receiptId',a.receipt_id,'revision',proof.revision,'createdAt',a.created_at)
 FROM hhc_web.online_bulletin_simplified_authorization a
 JOIN hhc_web.online_bulletin_recovery_receipt rc ON rc.id=a.receipt_id AND rc.actor_id=a.actor_id AND rc.request_id=a.request_id AND rc.issue_id=i.id
 JOIN hhc_web.online_bulletin_revision receipt_source ON receipt_source.document_id=e.aggregate_id AND receipt_source.revision=e.aggregate_version
 CROSS JOIN LATERAL(SELECT
 CASE WHEN jsonb_typeof(rc.result_json#>'{publication,version}')='number' AND rc.result_json#>>'{publication,version}' ~ '^[1-9][0-9]{0,18}$' THEN (rc.result_json#>>'{publication,version}')::numeric END AS version,
 CASE WHEN jsonb_typeof(rc.result_json#>'{receipt,revision}')='number' AND rc.result_json#>>'{receipt,revision}' ~ '^[1-9][0-9]{0,18}$' THEN (rc.result_json#>>'{receipt,revision}')::numeric END AS revision) proof
 WHERE a.operation_id=e.id AND rc.input_json->>'Kind'='simplified_publication_recovery' AND rc.input_json->>'Locale'='zh-Hans' AND rc.input_json->>'Series'='general'
 AND rc.input_json->>'TargetDocumentID'=d.id::text AND rc.result_json#>>'{receipt,documentId}'=d.id::text AND rc.result_json#>>'{publication,documentId}'=d.id::text
 AND rc.result_json#>>'{receipt,authorizationId}'=a.id::text AND rc.result_json#>>'{receipt,id}'=a.receipt_id::text AND rc.result_json#>>'{receipt,operationKey}'=rc.operation_key
 AND rc.input_json->>'PublicationOperationID'=e.id::text AND rc.result_json#>>'{receipt,publicationOperationId}'=e.id::text AND rc.result_json#>>'{receipt,outcome}'='published'
 AND rc.result_json#>>'{receipt,source,documentId}'=e.aggregate_id::text AND rc.result_json#>>'{receipt,source,revision}'=e.aggregate_version::text
 AND rc.result_json#>>'{receipt,source,contentHash}'=receipt_source.content_hash AND rc.input_json->'Source'=rc.result_json#>'{receipt,source}'
 AND rc.input_json->'Revision'=rc.result_json#>'{receipt,revision}' AND rc.result_json#>'{publication,revision}'=rc.result_json#>'{receipt,revision}'
 AND rc.input_json->'CanonicalVersion'=rc.result_json#>'{receipt,canonicalVersion}' AND rc.input_json->'OnlineVersion'=rc.result_json#>'{receipt,onlineVersion}'
 AND rc.input_json->'LayoutValidationHash'=rc.result_json#>'{receipt,layoutValidationHash}'
 AND proof.version BETWEEN 1 AND 9223372036854775807 AND proof.revision BETWEEN 1 AND 9223372036854775807
 ORDER BY proof.version DESC,a.created_at DESC,a.id DESC LIMIT 1)`
