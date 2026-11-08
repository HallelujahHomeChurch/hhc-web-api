package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func publicationActor(ctx context.Context, actor string) (string, error) {
	p, ok := auditclient.ProvenanceFromContext(ctx)
	if !ok || p.ActorID != actor || (p.ActorType != "user" && p.ActorType != "service") || !auditclient.ValidActorRequest(actor, p.RequestID) {
		return "", recordings.ErrAuditUnavailable
	}
	return p.ActorType, nil
}

func readPublishReceipt(row *sql.Row, id string, expected int64) (recordings.PublishReceipt, error) {
	var v recordings.PublishReceipt
	err := row.Scan(&v.OperationKey, &v.ActorType, &v.ActorID, &v.RecordingID, &v.AssetVersionID, &v.ExpectedVersion, &v.PublishedVersion, &v.PublishedAt, &v.RequestFingerprint, &v.PackageID)
	if errors.Is(err, sql.ErrNoRows) {
		return v, recordings.ErrNotFound
	}
	if err != nil {
		return v, err
	}
	v.PublishedAt = v.PublishedAt.UTC()
	if v.RecordingID != id || v.ExpectedVersion != expected {
		return recordings.PublishReceipt{}, recordings.ErrConflict
	}
	return v, nil
}

const publishReceiptQuery = `SELECT operation_key,actor_type,actor_id,recording_id::text,asset_version_id,expected_version,published_version,published_at,request_fingerprint,COALESCE(package_id,'') FROM hhc_web.member_recording_publish_receipt WHERE actor_type=$1 AND actor_id=$2 AND operation_key=$3`

func (r *RecordingRepository) GetPublishOperation(ctx context.Context, id string, expected int64, actor, key string, now time.Time) (recordings.PublishResult, error) {
	if expected < 1 || !recordings.ValidOperationKey(key) {
		return recordings.PublishResult{}, recordings.ErrInvalid
	}
	kind, err := publicationActor(ctx, actor)
	if err != nil {
		return recordings.PublishResult{}, err
	}
	receipt, err := readPublishReceipt(r.db.QueryRowContext(ctx, publishReceiptQuery, kind, actor, key), id, expected)
	if err != nil {
		return recordings.PublishResult{}, err
	}
	current, err := r.Get(ctx, id)
	if err != nil {
		return recordings.PublishResult{}, err
	}
	return recordings.NewPublishResult(receipt, current, now), nil
}

// Receipt, publication, audit and first-publication notification commit in one
// transaction. The actor/key lock also serializes conflicting recording IDs.
func (r *RecordingRepository) PublishOperation(ctx context.Context, id string, expected int64, actor, key string, now time.Time) (recordings.PublishResult, error) {
	if expected < 1 || !recordings.ValidOperationKey(key) {
		return recordings.PublishResult{}, recordings.ErrInvalid
	}
	kind, err := publicationActor(ctx, actor)
	if err != nil {
		return recordings.PublishResult{}, err
	}
	tx, finishTx, err := r.beginRecordingTransaction(ctx)
	if err != nil {
		return recordings.PublishResult{}, err
	}
	defer finishTx()
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "recording-publish:"+kind+":"+actor+":"+key); err != nil {
		return recordings.PublishResult{}, err
	}
	receipt, err := readPublishReceipt(tx.QueryRowContext(ctx, publishReceiptQuery, kind, actor, key), id, expected)
	if err != nil && !errors.Is(err, recordings.ErrNotFound) {
		return recordings.PublishResult{}, err
	}
	if errors.Is(err, recordings.ErrNotFound) {
		current, err := r.mutateTx(ctx, tx, id, expected, actor, now, "cms.recording.publish", publishRecordingQuery, actor, now)
		if err != nil {
			return recordings.PublishResult{}, err
		}
		receipt = recordings.PublishReceipt{OperationKey: key, ActorType: kind, ActorID: actor, RecordingID: id, AssetVersionID: current.PrimaryAssetVersionID, ExpectedVersion: expected, PublishedVersion: current.Version, PublishedAt: now.UTC().Truncate(time.Microsecond)}
		receipt.PackageID = current.PackageID
		// Struct JSON defines the versioned empty-body publish request fingerprint.
		request, err := json.Marshal(struct {
			SchemaVersion   int    `json:"schemaVersion"`
			ActorType       string `json:"actorType"`
			ActorID         string `json:"actorId"`
			RecordingID     string `json:"recordingId"`
			AssetVersionID  string `json:"assetVersionId"`
			ExpectedVersion int64  `json:"expectedVersion"`
			PackageID       string `json:"packageId,omitempty"`
		}{1, kind, actor, id, current.PrimaryAssetVersionID, expected, current.PackageID})
		if err != nil {
			return recordings.PublishResult{}, err
		}
		digest := sha256.Sum256(request)
		receipt.RequestFingerprint = hex.EncodeToString(digest[:])
		_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.member_recording_publish_receipt(actor_type,actor_id,operation_key,recording_id,asset_version_id,expected_version,published_version,published_at,request_fingerprint,package_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,NULLIF($10,''))`, kind, actor, key, id, receipt.AssetVersionID, expected, receipt.PublishedVersion, receipt.PublishedAt, receipt.RequestFingerprint, receipt.PackageID)
		if err != nil {
			return recordings.PublishResult{}, recordingConflict(err)
		}
	}
	if err = tx.Commit(); err != nil {
		return recordings.PublishResult{}, err
	}
	current, err := r.Get(ctx, id)
	if err != nil {
		return recordings.PublishResult{}, err
	}
	return recordings.NewPublishResult(receipt, current, now), nil
}
