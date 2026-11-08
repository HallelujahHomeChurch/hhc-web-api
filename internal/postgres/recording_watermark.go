package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"github.com/google/uuid"
	"slices"
	"time"
)

const recordingWatermarkColumns = `id::text,trace_code,user_id::text,recording_id::text,package_id,format_version,issued_at,expires_at`

func scanRecordingWatermark(row interface{ Scan(...any) error }) (recordings.WatermarkReceipt, error) {
	var v recordings.WatermarkReceipt
	err := row.Scan(&v.ID, &v.Code, &v.UserID, &v.RecordingID, &v.PackageID, &v.FormatVersion, &v.IssuedAt, &v.ExpiresAt)
	return v, err
}
func (r *RecordingRepository) EnsureRecordingWatermark(ctx context.Context, user, id, pkg string, now time.Time) (recordings.WatermarkReceipt, error) {
	return r.ensureRecordingWatermark(ctx, user, id, pkg, now, recordings.NewWatermarkCode)
}
func (r *RecordingRepository) ensureRecordingWatermark(ctx context.Context, user, id, pkg string, now time.Time, newCode func() (string, error)) (recordings.WatermarkReceipt, error) {
	var empty recordings.WatermarkReceipt
	if _, err := uuid.Parse(user); err != nil {
		return empty, recordings.ErrInvalid
	}
	tx, finishTx, err := r.beginRecordingTransaction(ctx)
	if err != nil {
		return empty, err
	}
	defer finishTx()
	if err = lockWatermarkSubject(ctx, tx, user); err != nil {
		return empty, err
	}
	blocked, err := watermarkRestricted(ctx, tx, user)
	if err != nil {
		return empty, err
	}
	if blocked {
		return empty, bulletinwatermark.ErrRestricted
	}
	// Lock the publication row until commit so unpublish/delete cannot overtake issuance.
	current, err := scanRecording(tx.QueryRowContext(ctx, `SELECT `+recordingColumns+` FROM hhc_web.member_recording WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, id))
	if err != nil {
		return empty, err
	}
	if !current.Visible(now) || current.PackageID != pkg {
		return empty, recordings.ErrNotFound
	}

	v, err := ensureRecordingWatermarkTx(ctx, tx, user, id, pkg, now, newCode)
	if err != nil {
		return empty, err
	}
	return v, tx.Commit()
}
func readRecordingWatermarkTx(ctx context.Context, tx *sql.Tx, user, id, pkg string, now time.Time) (recordings.WatermarkReceipt, error) {
	v, err := scanRecordingWatermark(tx.QueryRowContext(ctx, `SELECT `+recordingWatermarkColumns+` FROM hhc_web.recording_watermark_receipt WHERE user_id=$1 AND recording_id=$2`, user, id))
	if err == nil && (!now.Before(v.ExpiresAt) || v.PackageID != pkg) {
		return recordings.WatermarkReceipt{}, recordings.ErrNotFound
	}
	return v, err
}

func ensureRecordingWatermarkTx(ctx context.Context, tx *sql.Tx, user, id, pkg string, now time.Time, newCode func() (string, error)) (recordings.WatermarkReceipt, error) {
	var empty recordings.WatermarkReceipt
	v, err := readRecordingWatermarkTx(ctx, tx, user, id, pkg, now)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	for attempt := 0; attempt < 5; attempt++ {
		code, e := newCode()
		if e != nil {
			return empty, e
		}
		v, e = scanRecordingWatermark(tx.QueryRowContext(ctx, `INSERT INTO hhc_web.recording_watermark_receipt(id,trace_code,user_id,recording_id,package_id,issued_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING RETURNING `+recordingWatermarkColumns, uuid.NewString(), code, user, id, pkg, now, now.Add(recordings.WatermarkRetention)))
		if e == nil {
			return v, nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return empty, e
		}
		// Another shared issuer may have committed this member's first receipt.
		existing, readErr := readRecordingWatermarkTx(ctx, tx, user, id, pkg, now)
		if readErr == nil {
			return existing, nil
		}
		if !errors.Is(readErr, sql.ErrNoRows) {
			return empty, readErr
		}
	}
	return empty, recordings.ErrConflict
}

func (r *RecordingRepository) LookupRecordingWatermark(ctx context.Context, actor, id, input string, now time.Time) (recordings.WatermarkReceipt, error) {
	var empty recordings.WatermarkReceipt
	provenance, ok := auditclient.ProvenanceFromContext(ctx)
	if r.auditOutbox == nil || !ok || provenance.ActorType != "user" || provenance.ActorID != actor {
		return empty, recordings.ErrAuditUnavailable
	}
	code, invalid := recordings.NormalizeWatermarkCode(input)
	if parsed, err := uuid.Parse(id); err != nil || len(id) != 36 || parsed.Version() != 4 || parsed.Variant() != uuid.RFC4122 {
		invalid = recordings.ErrInvalid
		id = uuid.NewString()
	} else {
		id = parsed.String()
	}
	tx, finishTx, err := r.beginRecordingTransaction(ctx)
	if err != nil {
		return empty, err
	}
	defer finishTx()
	var subject string
	err = tx.QueryRowContext(ctx, `SELECT user_id::text FROM hhc_web.recording_watermark_receipt WHERE trace_code=$1 AND recording_id=$2 AND expires_at>$3`, code, id, now).Scan(&subject)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return empty, err
	}
	subjects := []string{actor}
	if subject != "" && subject != actor {
		subjects = append(subjects, subject)
	}
	slices.Sort(subjects)
	for _, user := range subjects {
		if err = lockWatermarkSubject(ctx, tx, user); err != nil {
			return empty, err
		}
	}
	blocked, err := watermarkRestricted(ctx, tx, actor)
	if err != nil {
		return empty, err
	}
	if blocked {
		// Match the existing DSR fence: a stale request must not recreate an erased actor's trace data.
		return empty, bulletinwatermark.ErrRestricted
	}
	var count int
	err = tx.QueryRowContext(ctx, `INSERT INTO hhc_web.recording_watermark_lookup_rate AS c(subject_hash,window_start,count) VALUES($1,$2,1) ON CONFLICT(subject_hash,window_start) DO UPDATE SET count=LEAST(c.count+1,11) RETURNING count`, watermarkSubjectHash(actor), now.UTC().Truncate(time.Minute)).Scan(&count)
	if err != nil {
		return empty, err
	}
	result := "not_found"
	lookupErr := recordings.ErrNotFound
	v := empty
	switch {
	case count > 10:
		result = "rate_limited"
		lookupErr = bulletinwatermark.ErrRateLimited
	case invalid != nil:
		result = "invalid"
		lookupErr = recordings.ErrInvalid
	case subject != "":
		restricted, e := watermarkRestricted(ctx, tx, subject)
		if e != nil {
			return empty, e
		}
		if !restricted {
			v, err = scanRecordingWatermark(tx.QueryRowContext(ctx, `SELECT `+recordingWatermarkColumns+` FROM hhc_web.recording_watermark_receipt WHERE trace_code=$1 AND recording_id=$2 AND user_id=$3 AND expires_at>$4 FOR SHARE`, code, id, subject, now))
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return empty, err
			}
			if err == nil {
				result = "matched"
				lookupErr = nil
			}
		}
	}
	event, err := auditclient.NewEventForPrincipal("cms.recording.watermark.lookup", id, "user", actor, provenance.RequestID, now, auditclient.Metadata{"result": result})
	if err == nil {
		_, err = r.auditOutbox.EnqueueTx(ctx, tx, event, now)
	}
	if err != nil {
		return empty, recordings.ErrAuditUnavailable
	}
	payload, _ := json.Marshal(map[string]string{"result": result})
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.cms_audit_event(id,action,resource_type,resource_id,actor,payload_json,created_at) VALUES($1,'recording_watermark.lookup','recording',$2,$3,$4,$5)`, uuid.NewString(), id, actor, payload, now); err != nil {
		return empty, recordings.ErrAuditUnavailable
	}
	// Commit unsuccessful attempts too: rollback would reset the rate budget.
	if err = tx.Commit(); err != nil {
		return empty, err
	}
	if lookupErr != nil {
		return empty, lookupErr
	}
	return v, nil
}
