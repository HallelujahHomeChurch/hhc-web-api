package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"time"
)

func (r *RecordingRepository) RecordPlaybackHeartbeat(ctx context.Context, id, user string, input recordings.PlaybackHeartbeat, duration float64, now time.Time) error {
	if e := input.Validate(); e != nil {
		return e
	}
	tx, finishTx, e := r.interactionTransaction(ctx, id, user, now)
	if e != nil {
		return e
	}
	defer finishTx()
	var raw []byte
	live := false
	e = tx.QueryRowContext(ctx, `SELECT payload_json FROM hhc_web.member_recording_broadcast WHERE recording_id=$1`, id).Scan(&raw)
	if e == nil {
		var b recordings.Broadcast
		if e = json.Unmarshal(raw, &b); e != nil {
			return e
		}
		live = b.View.Phase == "live" || b.View.Phase == "end_pending"
		if !live && b.View.Phase != "processing" && b.View.Phase != "archived" {
			return recordings.ErrNotFound
		}
	} else if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	var sequence int64
	var lastSeen time.Time
	e = tx.QueryRowContext(ctx, `SELECT sequence,last_seen FROM hhc_web.member_recording_playback_session WHERE recording_id=$1 AND account_id=$2 AND session_id=$3`, id, user, input.PlaybackSessionID).Scan(&sequence, &lastSeen)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if input.Sequence <= sequence {
		return nil
	}
	if sequence == 0 {
		if _, e = tx.ExecContext(ctx, `DELETE FROM hhc_web.member_recording_playback_session WHERE recording_id=$1 AND account_id=$2 AND last_seen<$3`, id, user, now.Add(-60*time.Second)); e != nil {
			return e
		}
		var count int
		if e = tx.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.member_recording_playback_session WHERE recording_id=$1 AND account_id=$2`, id, user).Scan(&count); e != nil {
			return e
		}
		if count >= 16 {
			return recordings.ErrConflict
		}
	}

	var visit string
	var active, credited time.Time
	var liveMS, replayMS int64
	var liveQ, replayQ, totalQ bool
	e = tx.QueryRowContext(ctx, `SELECT visit_id::text,last_active,credited_until,live_ms,replay_ms,live_qualified,replay_qualified,total_qualified FROM hhc_web.member_recording_playback_visit WHERE recording_id=$1 AND account_id=$2`, id, user).Scan(&visit, &active, &credited, &liveMS, &replayMS, &liveQ, &replayQ, &totalQ)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if visit == "" || now.Sub(active) >= 30*time.Minute {
		visit = platform.NewID()
		active = now
		credited = now
		liveMS, replayMS = 0, 0
		liveQ, replayQ, totalQ = false, false, false
	}
	credit := recordings.PlaybackCredit(now, lastSeen, credited, input.DeltaPlayingMS)
	if credit > 0 {
		credited = now
		active = now
	}
	if input.State == "playing" {
		active = now
	}
	if live {
		liveMS += credit
	} else {
		replayMS += credit
	}
	shortEnded := input.State == "ended" && duration > 0 && duration < 10 && input.Position >= duration-0.25 && liveMS+replayMS > 0
	newLiveQ := liveQ || liveMS >= 10000
	newReplayQ := replayQ || replayMS >= 10000 || shortEnded && !live
	newTotalQ := totalQ || liveMS+replayMS >= 10000 || shortEnded
	incLive, incReplay, incTotal := 0, 0, 0
	if !liveQ && newLiveQ {
		incLive = 1
	}
	if !replayQ && newReplayQ {
		incReplay = 1
	}
	if !totalQ && newTotalQ {
		incTotal = 1
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_playback_session(recording_id,account_id,session_id,sequence,state,position,last_seen) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(recording_id,account_id,session_id) DO UPDATE SET sequence=EXCLUDED.sequence,state=EXCLUDED.state,position=EXCLUDED.position,last_seen=EXCLUDED.last_seen`, id, user, input.PlaybackSessionID, input.Sequence, input.State, input.Position, now)
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_playback_visit(recording_id,account_id,visit_id,last_active,credited_until,live_ms,replay_ms,live_qualified,replay_qualified,total_qualified) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT(recording_id,account_id) DO UPDATE SET visit_id=EXCLUDED.visit_id,last_active=EXCLUDED.last_active,credited_until=EXCLUDED.credited_until,live_ms=EXCLUDED.live_ms,replay_ms=EXCLUDED.replay_ms,live_qualified=EXCLUDED.live_qualified,replay_qualified=EXCLUDED.replay_qualified,total_qualified=EXCLUDED.total_qualified`, id, user, visit, active, credited, liveMS, replayMS, newLiveQ, newReplayQ, newTotalQ)
	if e != nil {
		return e
	}
	if newTotalQ {
		if _, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_viewer(recording_id,account_id,first_viewed_at) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, id, user, now); e != nil {
			return e
		}
	}
	_, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_metrics(recording_id,live_views,replay_views,total_views,watch_ms,tracking_since) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(recording_id) DO UPDATE SET live_views=member_recording_metrics.live_views+EXCLUDED.live_views,replay_views=member_recording_metrics.replay_views+EXCLUDED.replay_views,total_views=member_recording_metrics.total_views+EXCLUDED.total_views,watch_ms=member_recording_metrics.watch_ms+EXCLUDED.watch_ms`, id, incLive, incReplay, incTotal, credit, now)
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (r *RecordingRepository) GetPlaybackMetrics(ctx context.Context, id, user string, admin bool, now time.Time) (recordings.PlaybackMetrics, error) {
	out := recordings.PlaybackMetrics{MetricVersion: recordings.MetricVersion, Status: "available", MeasuredAt: now, TrackingSince: now}
	var tx *sql.Tx
	var finishTx func()
	defer func() {
		if finishTx != nil {
			finishTx()
		}
	}()
	var e error
	if !admin {
		tx, finishTx, e = r.interactionTransaction(ctx, id, user, now)
	} else {
		tx, finishTx, e = r.beginRecordingTransaction(ctx)
		if e == nil {
			var exists bool
			e = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.member_recording WHERE id=$1 AND deleted_at IS NULL)`, id).Scan(&exists)
			if e == nil && !exists {
				e = recordings.ErrNotFound
			}
		}
	}
	if e != nil {
		return out, e
	}
	var milliseconds int64
	e = tx.QueryRowContext(ctx, `SELECT live_views,replay_views,total_views,watch_ms,peak_concurrent,tracking_since FROM hhc_web.member_recording_metrics WHERE recording_id=$1`, id).Scan(&out.LiveViews, &out.ReplayViews, &out.TotalViews, &milliseconds, &out.PeakConcurrent, &out.TrackingSince)
	if errors.Is(e, sql.ErrNoRows) {
		out.Status = "not_started"
	} else if e != nil {
		return out, e
	}
	out.WatchSeconds = float64(milliseconds) / 1000
	e = tx.QueryRowContext(ctx, `SELECT count(DISTINCT account_id) FROM hhc_web.member_recording_playback_session WHERE recording_id=$1 AND state='playing' AND last_seen>$2`, id, now.Add(-60*time.Second)).Scan(&out.CurrentViewers)
	if e != nil {
		return out, e
	}
	e = tx.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.member_recording_viewer WHERE recording_id=$1`, id).Scan(&out.UniqueViewers)
	return out, e
}
func (r *RecordingRepository) RollupPlaybackMetrics(ctx context.Context, now time.Time) error {
	tx, finishTx, e := r.beginRecordingTransaction(ctx)
	if e != nil {
		return e
	}
	defer finishTx()
	_, e = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_metric_minute(recording_id,minute,peak_concurrent,total_views,watch_ms) SELECT m.recording_id,$1,COALESCE(s.viewers,0),m.total_views,m.watch_ms FROM hhc_web.member_recording_metrics m LEFT JOIN (SELECT recording_id,count(DISTINCT account_id) AS viewers FROM hhc_web.member_recording_playback_session WHERE state='playing' AND last_seen>$2 GROUP BY recording_id) s ON s.recording_id=m.recording_id WHERE EXISTS(SELECT 1 FROM hhc_web.member_recording_playback_session recent WHERE recent.recording_id=m.recording_id AND recent.last_seen>$3) ON CONFLICT(recording_id,minute) DO UPDATE SET peak_concurrent=GREATEST(member_recording_metric_minute.peak_concurrent,EXCLUDED.peak_concurrent),total_views=GREATEST(member_recording_metric_minute.total_views,EXCLUDED.total_views),watch_ms=GREATEST(member_recording_metric_minute.watch_ms,EXCLUDED.watch_ms)`, now.Truncate(time.Minute), now.Add(-60*time.Second), now.Add(-2*time.Minute))
	if e != nil {
		return e
	}
	_, e = tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_metrics m SET peak_concurrent=GREATEST(m.peak_concurrent,s.peak_concurrent) FROM hhc_web.member_recording_metric_minute s WHERE m.recording_id=s.recording_id AND s.minute=$1`, now.Truncate(time.Minute))
	if e != nil {
		return e
	}
	for _, q := range []string{`DELETE FROM hhc_web.member_recording_playback_session WHERE last_seen<$1`, `DELETE FROM hhc_web.member_recording_playback_visit WHERE last_active<$1`} {
		if _, e = tx.ExecContext(ctx, q, now.Add(-7*24*time.Hour)); e != nil {
			return e
		}
	}
	if _, e = tx.ExecContext(ctx, `DELETE FROM hhc_web.member_recording_metric_minute WHERE minute<$1`, now.Add(-365*24*time.Hour)); e != nil {
		return e
	}
	return tx.Commit()
}
