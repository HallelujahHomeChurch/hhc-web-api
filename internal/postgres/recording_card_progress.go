package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"time"
)

func (r *RecordingRepository) GetCardProgress(ctx context.Context, user string, ids []string, now time.Time) (map[string]recordings.CardPlayback, error) {
	out := make(map[string]recordings.CardPlayback, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	if len(ids) > 100 {
		return nil, recordings.ErrInvalid
	}
	err := r.withRecordingSubject(ctx, user, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT r.id::text,r.package_id,r.is_broadcast,b.payload_json,p.media_version,p.position_seconds,p.completed
   FROM hhc_web.member_recording r
   LEFT JOIN hhc_web.member_recording_progress p ON p.recording_id=r.id AND p.account_id=$1 AND p.mode='vod'
     AND p.expires_at>GREATEST($3,statement_timestamp()) AND (p.position_seconds>0 OR p.completed)
   LEFT JOIN hhc_web.member_recording_broadcast b ON b.recording_id=r.id
   WHERE r.id=ANY($2::uuid[])
   AND r.deleted_at IS NULL AND NOT r.hidden AND NOT r.media_terminal AND r.status='published'
   AND r.package_id IS NOT NULL AND r.ready_at IS NOT NULL AND r.uploaded_at IS NOT NULL AND r.expires_at IS NOT NULL
   AND r.expires_at>GREATEST($3,statement_timestamp())`, user, ids, now)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, pkg string
			var savedVersion sql.NullString
			var position sql.NullFloat64
			var completed sql.NullBool
			var broadcast bool
			var raw []byte
			if err := rows.Scan(&id, &pkg, &broadcast, &raw, &savedVersion, &position, &completed); err != nil {
				return err
			}
			version := "vod:" + pkg
			if broadcast {
				var b recordings.Broadcast
				if len(raw) == 0 {
					continue
				}
				if err := json.Unmarshal(raw, &b); err != nil {
					return err
				}
				if !b.MemberVODReady() || (b.View.Phase != "processing" && b.View.Phase != "archived") {
					continue
				}
				version = b.VODProgressVersion(pkg)
			}
			item := recordings.CardPlayback{MediaVersion: version}
			if savedVersion.Valid && savedVersion.String == version && position.Valid && completed.Valid {
				item.Progress = &recordings.CardProgress{MediaVersion: version, PositionSeconds: position.Float64, Completed: completed.Bool}
			}
			out[id] = item
		}
		return rows.Err()
	})
	return out, err
}
