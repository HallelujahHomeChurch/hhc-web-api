package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"io"
	"strings"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

// Version 1: a bounded list of common English strings and Taiwanese Latin abbreviations.
// shortcut: this cannot cover every language or phonetic variant; extend with reviewed examples.
var recordingPublicIDBlockedStrings = [...]string{
	"fuck", "fck", "fcuk", "shit", "bitch", "cunt", "dick", "cock", "pussy",
	"porn", "sex", "slut", "whore", "asshole", "bastard", "nigger", "nigga",
	"faggot", "nazi", "kkk", "wtf", "stfu", "lmfao", "tmd", "nmsl", "cnm", "g8", "gy",
}

var recordingPublicIDLeet = strings.NewReplacer("0", "o", "1", "i", "3", "e", "4", "a", "5", "s", "7", "t")

func inappropriateRecordingPublicID(id string) bool {
	normalized := strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(id), "_", ""), "-", "")
	leet := recordingPublicIDLeet.Replace(normalized)
	for _, blocked := range recordingPublicIDBlockedStrings {
		if strings.Contains(normalized, blocked) || strings.Contains(leet, blocked) {
			return true
		}
	}
	return false
}

func newRecordingPublicID() (string, error) {
	return recordingPublicIDFromEntropy(rand.Reader)
}

func recordingPublicIDFromEntropy(entropy io.Reader) (string, error) {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-"
	for range 32 {
		var data [11]byte
		if _, err := io.ReadFull(entropy, data[:]); err != nil {
			return "", err
		}
		for i := range data {
			data[i] = alphabet[data[i]&63]
		}
		candidate := string(data[:])
		if !inappropriateRecordingPublicID(candidate) {
			return candidate, nil
		}
	}
	return "", errors.New("recording public ID content retry limit reached")
}

func assignRecordingPublicID(ctx context.Context, tx *sql.Tx, id string, generate func() (string, error)) (string, error) {
	var existing string
	err := tx.QueryRowContext(ctx, `SELECT public_id FROM hhc_web.member_recording_public_id WHERE recording_id=$1`, id).Scan(&existing)
	if err == nil {
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	for range 8 {
		candidate, err := generate()
		if err != nil {
			return "", err
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_public_id(recording_id,public_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, id, candidate)
		if err != nil {
			return "", err
		}
		rows, err := result.RowsAffected()
		if err != nil {
			return "", err
		}
		if rows == 1 {
			return candidate, nil
		}
		err = tx.QueryRowContext(ctx, `SELECT public_id FROM hhc_web.member_recording_public_id WHERE recording_id=$1`, id).Scan(&existing)
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	return "", errors.New("recording public ID collision retry limit reached")
}

// PlanRecordingPublicIDs reports counts only; it never allocates identifiers.
func (r *RecordingRepository) PlanRecordingPublicIDs(ctx context.Context) (total, missing int64, err error) {
	err = r.db.QueryRowContext(ctx, `SELECT count(*),count(*) FILTER (WHERE p.recording_id IS NULL) FROM hhc_web.member_recording r LEFT JOIN hhc_web.member_recording_public_id p ON p.recording_id=r.id`).Scan(&total, &missing)
	return
}

// BackfillRecordingPublicIDs commits at most one bounded batch per call.
func (r *RecordingRepository) BackfillRecordingPublicIDs(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 100 {
		return 0, errors.New("public ID batch size must be between 1 and 100")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT r.id::text FROM hhc_web.member_recording r WHERE NOT EXISTS(SELECT 1 FROM hhc_web.member_recording_public_id p WHERE p.recording_id=r.id) ORDER BY r.id LIMIT $1 FOR UPDATE OF r SKIP LOCKED`, limit)
	if err != nil {
		return 0, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		if _, err = assignRecordingPublicID(ctx, tx, id, newRecordingPublicID); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	return len(ids), nil
}

func (r *RecordingRepository) RecordingIDByPublicID(ctx context.Context, publicID string) (string, error) {
	var id string
	err := r.db.QueryRowContext(ctx, `SELECT p.recording_id::text FROM hhc_web.member_recording_public_id p JOIN hhc_web.member_recording r ON r.id=p.recording_id WHERE p.public_id=$1 AND r.deleted_at IS NULL`, publicID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", recordings.ErrNotFound
	}
	return id, err
}
