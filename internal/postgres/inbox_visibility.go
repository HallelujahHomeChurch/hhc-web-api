package postgres

import (
	"context"
	"encoding/json"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/inboxcontent"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func (r *Repository) InboxBulletinEntitlements(ctx context.Context) ([]inboxcontent.Entitlement, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT series,locale,entitlement_code FROM hhc_web.bulletin_entitlement_policy ORDER BY series,locale`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []inboxcontent.Entitlement{}
	for rows.Next() {
		var v inboxcontent.Entitlement
		if err = rows.Scan(&v.Series, &v.Locale, &v.Code); err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}
func (r *Repository) InboxBulletins(ctx context.Context, refs []inboxcontent.Resource) ([]inboxcontent.Bulletin, error) {
	raw, err := json.Marshal(refs)
	if err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT i.id::text,i.issue_number,i.issue_date::text,v.series,v.locale,COALESCE(o.canonical_metadata->>'title',p.title),COALESCE(o.canonical_metadata->>'subtitle',p.subtitle,''),COALESCE(p.asset_id,''),COALESCE(o.created_at,p.published_at),COALESCE(o.revision,p.version),o.document_id IS NOT NULL
 FROM jsonb_to_recordset($1::jsonb) AS x(type text,id text,series text)
 JOIN hhc_web.bulletin_issue i ON i.id::text=x.id
 JOIN hhc_web.bulletin_version v ON v.issue_id=i.id AND v.series=x.series
 LEFT JOIN hhc_web.bulletin_published_version p ON p.issue_id=v.issue_id AND p.series=v.series AND p.locale=v.locale
 LEFT JOIN hhc_web.online_bulletin_document d ON d.issue_id=v.issue_id AND d.series=v.series AND d.content_locale=v.locale
 LEFT JOIN hhc_web.online_bulletin_publication o ON o.document_id=d.id AND o.revision=d.published_revision AND i.issue_number>=1739
 WHERE x.type='bulletin' AND (p.issue_id IS NOT NULL OR o.document_id IS NOT NULL)
 AND i.issue_number>0
 ORDER BY i.id::text,v.series,v.locale`, raw)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []inboxcontent.Bulletin{}
	for rows.Next() {
		var v inboxcontent.Bulletin
		if err = rows.Scan(&v.IssueID, &v.IssueNumber, &v.IssueDate, &v.Series, &v.Locale, &v.Title, &v.Subtitle, &v.ProtectedAssetID, &v.PublishedAt, &v.Version, &v.Online); err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}
func (r *RecordingRepository) InboxRecordings(ctx context.Context, ids []string) ([]recordings.InboxSnapshot, error) {
	values, err := r.queryRecordings(ctx, `SELECT `+recordingColumns+` FROM hhc_web.member_recording WHERE id::text=ANY($1) AND deleted_at IS NULL`, ids)
	if err != nil {
		return nil, err
	}
	snapshots := make([]recordings.InboxSnapshot, len(values))
	byID := map[string]int{}
	for i, v := range values {
		snapshots[i].Recording = v
		byID[v.ID] = i
	}
	rows, err := r.db.QueryContext(ctx, `SELECT r.id::text,b.payload_json,c.payload_json FROM hhc_web.member_recording r LEFT JOIN hhc_web.member_recording_broadcast b ON b.recording_id=r.id LEFT JOIN LATERAL (SELECT payload_json FROM hhc_web.member_recording_capture WHERE recording_id=r.id ORDER BY (payload_json->'Status'->>'createdAt')::timestamptz DESC,id DESC LIMIT 1) c ON true WHERE r.id::text=ANY($1) AND r.deleted_at IS NULL`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var b, c []byte
		if err = rows.Scan(&id, &b, &c); err != nil {
			return nil, err
		}
		i, ok := byID[id]
		if !ok {
			continue
		}
		if len(b) > 0 {
			var v recordings.Broadcast
			if err = json.Unmarshal(b, &v); err != nil {
				return nil, err
			}
			snapshots[i].Broadcast = &v
		}
		if len(c) > 0 {
			var v recordings.Capture
			if err = json.Unmarshal(c, &v); err != nil {
				return nil, err
			}
			snapshots[i].Capture = &v
		}
	}
	return snapshots, rows.Err()
}

func (r *Repository) InboxSubjectAllowed(ctx context.Context, actor string) (bool, error) {
	var blocked bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.bulletin_watermark_subject WHERE subject_hash=$1)`, watermarkSubjectHash(actor)).Scan(&blocked)
	return !blocked, err
}
