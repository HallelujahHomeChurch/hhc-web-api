package postgres

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

type progressMedia struct {
	version, mode, captureID string
	expiresAt                *time.Time
}

// The member write transaction already holds the recording row lock and
// serializes writes for this account, including the first claim.
func currentProgressMedia(ctx context.Context, tx *sql.Tx, recording recordings.Recording, now time.Time, lock bool) (progressMedia, error) {
	var bindingCaptureID string
	var broadcastLock, captureLock string
	if lock {
		broadcastLock, captureLock = " FOR SHARE OF b", " FOR SHARE"
	}
	if recording.Hidden || recording.Expired(now) {
		return progressMedia{}, recordings.ErrNotFound
	}
	if recording.Broadcast {
		b, err := scanBroadcast(tx.QueryRowContext(ctx, `SELECT b.payload_json,r.title_override,r.description,b.source_managed
			FROM hhc_web.member_recording_broadcast b JOIN hhc_web.member_recording r ON r.id=b.recording_id
			WHERE b.recording_id=$1`+broadcastLock, recording.ID))
		if err != nil {
			return progressMedia{}, err
		}
		if !b.View.Announced || b.Revoked {
			return progressMedia{}, recordings.ErrNotFound
		}
		switch b.View.Phase {
		case "live", "end_pending":
			if b.View.Binding == nil || (b.MemberState != "live" && b.MemberState != "vod") {
				return progressMedia{}, recordings.ErrNotFound
			}
			bindingCaptureID = b.View.Binding.CaptureID
		case "archived", "processing":
			if recording.Visible(now) && b.MemberVODReady() {
				return progressMedia{version: b.VODProgressVersion(recording.PackageID), mode: "vod", expiresAt: recording.ExpiresAt}, nil
			}
			return progressMedia{}, recordings.ErrNotFound
		default:
			return progressMedia{}, recordings.ErrNotFound
		}
	} else if recording.Visible(now) {
		return progressMedia{version: "vod:" + recording.PackageID, mode: "vod", expiresAt: recording.ExpiresAt}, nil
	}
	capture, err := scanCapture(tx.QueryRowContext(ctx, `SELECT `+captureObservationJSON+`
		FROM hhc_web.member_recording_capture WHERE recording_id=$1
		ORDER BY (payload_json->'Status'->>'createdAt')::timestamptz DESC,id DESC LIMIT 1`+captureLock, recording.ID))
	if err != nil {
		return progressMedia{}, err
	}
	if !capture.LiveAvailable(now) || bindingCaptureID != "" && bindingCaptureID != capture.Status.CaptureID {
		return progressMedia{}, recordings.ErrNotFound
	}
	expiry := capture.Status.ExpiresAt
	for _, candidate := range []*time.Time{recording.ExpiresAt, capture.MediaExpiresAt, capture.Status.ReplayUntil} {
		if candidate != nil && candidate.Before(expiry) {
			expiry = *candidate
		}
	}
	return progressMedia{
		version: "live:" + capture.Status.CaptureID, mode: "live", captureID: capture.Status.CaptureID, expiresAt: &expiry,
	}, nil
}

func (r *RecordingRepository) GetPlaybackProgress(ctx context.Context, id, user string, now time.Time) (recordings.ProgressReadResult, error) {
	tx, finish, recording, err := r.recordingMemberWrite(ctx, id, user)
	if err != nil {
		return recordings.ProgressReadResult{}, err
	}
	defer finish()
	media, err := currentProgressMedia(ctx, tx, recording, now, true)
	if err != nil {
		return recordings.ProgressReadResult{}, err
	}
	out := recordings.ProgressReadResult{MediaVersion: media.version}
	var progress recordings.ProgressSnapshot
	err = tx.QueryRowContext(ctx, `SELECT owner_generation::text, media_version, position_seconds, completed, mode, updated_at
		FROM hhc_web.member_recording_progress
		WHERE account_id=$1 AND recording_id=$2 AND media_version=$3 AND expires_at>GREATEST($4,statement_timestamp())`, user, id, media.version, now).
		Scan(&progress.Generation, &progress.MediaVersion, &progress.PositionSeconds, &progress.Completed, &progress.Mode, &progress.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return recordings.ProgressReadResult{}, err
	}
	out.Progress = &progress
	return out, nil
}

type progressRow struct {
	recordings.ProgressOwner
	sessionID, claimID string
	duration           *float64
}

func readProgressRow(ctx context.Context, tx *sql.Tx, id, user, version string, now time.Time) (progressRow, error) {
	var row progressRow
	err := tx.QueryRowContext(ctx, `SELECT owner_generation::text, media_version, position_seconds, last_sequence,
		owner_session_id::text, claim_id::text, duration_seconds
		FROM hhc_web.member_recording_progress
		WHERE account_id=$1 AND recording_id=$2 AND media_version=$3 AND expires_at>GREATEST($4,clock_timestamp()) FOR UPDATE`,
		user, id, version, now).Scan(&row.Generation, &row.MediaVersion, &row.PositionSeconds,
		&row.Sequence, &row.sessionID, &row.claimID, &row.duration)
	return row, err
}

func (r *RecordingRepository) ClaimPlaybackProgress(ctx context.Context, id, user string, claim recordings.ProgressClaim, duration float64, now time.Time) (recordings.ProgressOwner, error) {
	if err := claim.Validate(); err != nil {
		return recordings.ProgressOwner{}, err
	}
	tx, finish, recording, err := r.recordingMemberWrite(ctx, id, user)
	if err != nil {
		return recordings.ProgressOwner{}, err
	}
	defer finish()
	media, err := currentProgressMedia(ctx, tx, recording, now, true)
	if err != nil {
		return recordings.ProgressOwner{}, err
	}
	if media.version != claim.MediaVersion {
		return recordings.ProgressOwner{}, recordings.ErrProgressConflict
	}
	row, err := readProgressRow(ctx, tx, id, user, media.version, now)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return recordings.ProgressOwner{}, err
	}
	if row.sessionID == claim.SessionID && row.claimID == claim.ClaimID {
		return row.ProgressOwner, nil
	}
	expected := ""
	if claim.ExpectedGeneration != nil {
		expected = *claim.ExpectedGeneration
	}
	if expected != row.Generation {
		return recordings.ProgressOwner{}, recordings.ErrProgressConflict
	}
	var bound *float64
	if media.mode == "vod" {
		if duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
			return recordings.ProgressOwner{}, recordings.ErrInvalid
		}
		bound = &duration
	}
	position, err := recordings.ProgressPosition(claim.ObservedPositionSeconds, bound)
	if err != nil {
		return recordings.ProgressOwner{}, err
	}
	owner := recordings.ProgressOwner{Generation: platform.NewID(), MediaVersion: media.version, PositionSeconds: position}
	_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_progress
		(account_id, recording_id, media_version, mode, capture_id, duration_seconds, position_seconds,
		 owner_session_id, owner_generation, claim_id, last_sequence, claimed_at, updated_at, expires_at)
		VALUES($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9,$10,0,statement_timestamp(),statement_timestamp(),
			LEAST(statement_timestamp()+interval '720 hours',$11::timestamptz))
		ON CONFLICT(account_id,recording_id) DO UPDATE SET
			media_version=EXCLUDED.media_version, mode=EXCLUDED.mode, capture_id=EXCLUDED.capture_id,
			duration_seconds=EXCLUDED.duration_seconds, position_seconds=EXCLUDED.position_seconds,
			completed=false, owner_session_id=EXCLUDED.owner_session_id, owner_generation=EXCLUDED.owner_generation,
			claim_id=EXCLUDED.claim_id, last_sequence=0, claimed_at=EXCLUDED.claimed_at,
			updated_at=EXCLUDED.updated_at, expires_at=EXCLUDED.expires_at`,
		user, id, media.version, media.mode, media.captureID, bound, position, claim.SessionID, owner.Generation, claim.ClaimID, media.expiresAt)
	if err != nil {
		return recordings.ProgressOwner{}, err
	}
	return owner, tx.Commit()
}

func (r *RecordingRepository) UpdatePlaybackProgress(ctx context.Context, id, user string, input recordings.ProgressUpdate, now time.Time) (recordings.ProgressWriteResult, error) {
	if err := input.Validate(); err != nil {
		return recordings.ProgressWriteResult{}, err
	}
	tx, finish, recording, err := r.recordingMemberWrite(ctx, id, user)
	if err != nil {
		return recordings.ProgressWriteResult{}, err
	}
	defer finish()
	media, err := currentProgressMedia(ctx, tx, recording, now, true)
	if err != nil {
		return recordings.ProgressWriteResult{}, err
	}
	if media.version != input.MediaVersion {
		return recordings.ProgressWriteResult{}, recordings.ErrProgressSuperseded
	}
	row, err := readProgressRow(ctx, tx, id, user, media.version, now)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return recordings.ProgressWriteResult{}, err
	}
	if row.Generation != input.Generation || row.sessionID != input.SessionID {
		return recordings.ProgressWriteResult{}, recordings.ErrProgressSuperseded
	}
	if input.Sequence <= row.Sequence {
		return recordings.ProgressWriteResult{Sequence: row.Sequence}, nil
	}
	position, err := recordings.ProgressPosition(input.PositionSeconds, row.duration)
	if err != nil {
		return recordings.ProgressWriteResult{}, err
	}
	completed := input.State == "ended" && row.duration != nil && position >= *row.duration-1
	result, err := tx.ExecContext(ctx, `UPDATE hhc_web.member_recording_progress SET
		position_seconds=$3, last_sequence=$4, completed=completed OR $5,
		updated_at=statement_timestamp(), expires_at=LEAST(statement_timestamp()+interval '720 hours',$6::timestamptz)
		WHERE account_id=$1 AND recording_id=$2 AND expires_at>clock_timestamp()
		AND ($6::timestamptz IS NULL OR $6>clock_timestamp())`, user, id, position, input.Sequence, completed, media.expiresAt)
	if err != nil {
		return recordings.ProgressWriteResult{}, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return recordings.ProgressWriteResult{}, err
	}
	if count == 0 {
		return recordings.ProgressWriteResult{}, recordings.ErrProgressSuperseded
	}
	return recordings.ProgressWriteResult{Accepted: true, Sequence: input.Sequence}, tx.Commit()
}

func (r *RecordingRepository) GetContinueWatching(ctx context.Context, user string, now time.Time) (*recordings.ResumeItem, error) {
	var item *recordings.ResumeItem
	err := r.withRecordingSubject(ctx, user, func(tx *sql.Tx) error {
		var before *time.Time
		var afterID *string
		for {
			var candidate recordings.ResumeItem
			err := tx.QueryRowContext(ctx, `SELECT p.recording_id::text, p.media_version, p.mode, COALESCE(p.capture_id,''),
				p.position_seconds,p.duration_seconds,p.owner_generation::text,p.claimed_at,p.updated_at,p.owner_session_id::text
				FROM hhc_web.member_recording_progress p JOIN hhc_web.member_recording r ON r.id=p.recording_id
				WHERE p.account_id=$1 AND NOT p.completed AND p.expires_at>$2
				AND p.position_seconds>0 AND p.updated_at>p.claimed_at
				AND r.deleted_at IS NULL AND NOT r.hidden AND NOT r.media_terminal AND (r.expires_at IS NULL OR r.expires_at>$2)
				AND (p.mode='live' OR (r.status='published' AND split_part(p.media_version, ':', 2)=r.package_id))
				AND ($3::timestamptz IS NULL OR p.claimed_at<$3 OR (p.claimed_at=$3 AND p.recording_id>$4))
				ORDER BY p.claimed_at DESC,p.recording_id LIMIT 1`, user, now, before, afterID).
				Scan(&candidate.RecordingID, &candidate.MediaVersion, &candidate.Mode, &candidate.CaptureID,
					&candidate.PositionSeconds, &candidate.DurationSeconds, &candidate.Generation, &candidate.ClaimedAt, &candidate.UpdatedAt, &candidate.SessionID)
			if errors.Is(err, sql.ErrNoRows) {
				return nil
			}
			if err != nil {
				return err
			}
			before, afterID = &candidate.ClaimedAt, &candidate.RecordingID
			recording, err := readSubjectGet(ctx, tx, candidate.RecordingID)
			if errors.Is(err, recordings.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			media, err := currentProgressMedia(ctx, tx, recording, now, false)
			if errors.Is(err, recordings.ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if media.version != candidate.MediaVersion {
				continue
			}
			candidate.PublicID, candidate.Title = recording.PublicID, recording.Title
			item = &candidate
			return nil
		}
	})
	return item, err
}
