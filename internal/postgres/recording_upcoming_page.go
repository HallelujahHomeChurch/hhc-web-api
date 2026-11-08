package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"strings"
	"time"
)

func (r *RecordingRepository) ListUpcomingBroadcastPage(ctx context.Context, now time.Time, at *time.Time, id string, limit int, query string) ([]recordings.Broadcast, error) {
	var after any
	if id != "" {
		after = id
	}
	when := `(b.payload_json->'View'->>'scheduledAt')::timestamptz`
	where := ` WHERE r.deleted_at IS NULL AND NOT r.hidden AND NOT r.media_terminal AND (r.expires_at IS NULL OR r.expires_at>$1) AND b.payload_json->'View'->>'announced'='true' AND COALESCE((b.payload_json->>'Revoked')::boolean,false)=false AND b.payload_json->'View'->>'phase' IN ('scheduled','preview','start_pending') AND ` + when + ` IS NOT NULL AND ($2::timestamptz IS NULL OR (` + when + `,r.id)>($2::timestamptz,$3::uuid))`
	args := []any{now, at, after, limit}
	escape := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	for _, term := range strings.Fields(query) {
		args = append(args, "%"+escape.Replace(term)+"%")
		where += fmt.Sprintf(` AND (r.title_override ILIKE $%d ESCAPE '\' OR r.description ILIKE $%d ESCAPE '\')`, len(args), len(args))
	}
	rows, e := r.db.QueryContext(ctx, `SELECT b.payload_json,r.title_override,r.description FROM hhc_web.member_recording_broadcast b JOIN hhc_web.member_recording r ON r.id=b.recording_id`+where+` ORDER BY `+when+`,r.id LIMIT $4`, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []recordings.Broadcast{}
	for rows.Next() {
		var b recordings.Broadcast
		var raw []byte
		var title, desc string
		if e = rows.Scan(&raw, &title, &desc); e != nil {
			return nil, e
		}
		if e = json.Unmarshal(raw, &b); e != nil {
			return nil, e
		}
		b.View.Title = title
		b.View.Description = desc
		out = append(out, b)
	}
	return out, rows.Err()
}
