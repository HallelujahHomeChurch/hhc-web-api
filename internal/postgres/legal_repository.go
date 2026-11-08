package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/legal"
	"github.com/google/uuid"
)

type LegalRepository struct{ db *sql.DB }

func NewLegalRepository(db *sql.DB) *LegalRepository { return &LegalRepository{db} }
func scanLegalDraft(row scanner) (legal.Draft, error) {
	var raw []byte
	var version int64
	if err := row.Scan(&raw, &version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = legal.ErrNotFound
		}
		return legal.Draft{}, err
	}
	var d legal.Draft
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, err
	}
	d.Version = version
	return d, nil
}
func (r *LegalRepository) Draft(ctx context.Context, scope legal.Scope) (legal.Draft, error) {
	return scanLegalDraft(r.db.QueryRowContext(ctx, `SELECT payload,version FROM hhc_web.legal_draft WHERE scope=$1`, scope))
}
func (r *LegalRepository) SaveDraft(ctx context.Context, d legal.Draft, expected int64, actor string) (legal.Draft, error) {
	if err := validateLegalProvenance(ctx, actor); err != nil {
		return d, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return d, err
	}
	defer tx.Rollback()
	d.Version = expected + 1
	raw, err := json.Marshal(d)
	if err != nil {
		return d, err
	}
	var result sql.Result
	if expected == 0 {
		result, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.legal_draft(scope,version,payload,updated_by) VALUES($1,1,$2,$3) ON CONFLICT DO NOTHING`, d.Scope, raw, actor)
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE hhc_web.legal_draft SET version=version+1,payload=$1,updated_by=$2,updated_at=now() WHERE scope=$3 AND version=$4`, raw, actor, d.Scope, expected)
	}
	if err != nil {
		return d, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return d, legal.ErrConflict
	}
	if err := recordLegalChange(ctx, tx, d.Scope, "draft_saved", d.Version, actor); err != nil {
		return d, err
	}
	return d, tx.Commit()
}
func (r *LegalRepository) Publish(ctx context.Context, d legal.Draft, expected int64, actor string) ([]legal.Snapshot, error) {
	if err := validateLegalProvenance(ctx, actor); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Serialize the two publication pointers as well as draft edits.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(2026093001)`); err != nil {
		return nil, err
	}
	d, err = scanLegalDraft(tx.QueryRowContext(ctx, `SELECT payload,version FROM hhc_web.legal_draft WHERE scope=$1 FOR UPDATE`, d.Scope))
	if err != nil {
		return nil, err
	}
	if d.Version != expected {
		return nil, legal.ErrConflict
	}
	snapshots, err := legal.BuildSnapshots(d)
	if err != nil {
		return nil, err
	}
	if d.Scope == legal.VerifiedMember {
		var exists bool
		err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.legal_current WHERE scope='common' AND terms_version=$1 AND privacy_version=$2)`, d.CommonTermsVersion, d.CommonPrivacyNoticeVersion).Scan(&exists)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, legal.ErrConflict
		}
	}
	for i, s := range snapshots {
		m := s.Manifest
		for _, doc := range []struct {
			key, version, hash string
			body               json.RawMessage
		}{{"terms-of-use", m.TermsVersion, m.TermsSHA256, s.Documents.Terms}, {"privacy-policy", m.PrivacyNoticeVersion, m.PrivacySHA256, s.Documents.Privacy}} {
			if d.Scope == legal.Common {
				var published []byte
				err = tx.QueryRowContext(ctx, `SELECT payload_json->'content' FROM hhc_web.public_projection WHERE projection_key=$1 FOR SHARE`, "page:"+m.Locale+":"+doc.key).Scan(&published)
				if err != nil {
					return nil, err
				}
				_, hash, e := legal.Canonical(published)
				if e != nil || hash != doc.hash {
					return nil, legal.ErrConflict
				}
			}
			if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.legal_document(scope,document_key,document_version,locale,sha256,payload) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, m.Scope, doc.key, doc.version, m.Locale, doc.hash, []byte(doc.body)); err != nil {
				return nil, err
			}
			var stored string
			err = tx.QueryRowContext(ctx, `SELECT sha256 FROM hhc_web.legal_document WHERE scope=$1 AND document_key=$2 AND document_version=$3 AND locale=$4`, m.Scope, doc.key, doc.version, m.Locale).Scan(&stored)
			if err != nil {
				return nil, err
			}
			if stored != doc.hash {
				return nil, legal.ErrConflict
			}
		}
		raw, _ := json.Marshal(s)
		if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.legal_snapshot(id,scope,terms_version,privacy_version,locale,payload,published_by) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(scope,terms_version,privacy_version,locale) DO NOTHING`, s.SnapshotID, m.Scope, m.TermsVersion, m.PrivacyNoticeVersion, m.Locale, raw, actor); err != nil {
			return nil, err
		}
		var stored []byte
		err = tx.QueryRowContext(ctx, `SELECT payload FROM hhc_web.legal_snapshot WHERE scope=$1 AND terms_version=$2 AND privacy_version=$3 AND locale=$4`, m.Scope, m.TermsVersion, m.PrivacyNoticeVersion, m.Locale).Scan(&stored)
		if err != nil {
			return nil, err
		}
		var previous legal.Snapshot
		if err = json.Unmarshal(stored, &previous); err != nil {
			return nil, err
		}
		if previous.Manifest != m {
			return nil, legal.ErrConflict
		}
		snapshots[i] = previous
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.legal_current(scope,terms_version,privacy_version) VALUES($1,$2,$3) ON CONFLICT(scope) DO UPDATE SET terms_version=EXCLUDED.terms_version,privacy_version=EXCLUDED.privacy_version`, d.Scope, d.TermsVersion, d.PrivacyNoticeVersion); err != nil {
		return nil, err
	}
	if err := recordLegalChange(ctx, tx, d.Scope, "published", d.Version, actor); err != nil {
		return nil, err
	}
	return snapshots, tx.Commit()
}
func scanLegalSnapshot(row scanner) (legal.Snapshot, error) {
	var raw []byte
	if err := row.Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = legal.ErrNotFound
		}
		return legal.Snapshot{}, err
	}
	var s legal.Snapshot
	err := json.Unmarshal(raw, &s)
	return s, err
}
func (r *LegalRepository) Current(ctx context.Context, scope legal.Scope, locale string) (legal.Snapshot, error) {
	return scanLegalSnapshot(r.db.QueryRowContext(ctx, `SELECT s.payload FROM hhc_web.legal_snapshot s JOIN hhc_web.legal_current c USING(scope,terms_version,privacy_version) WHERE s.scope=$1 AND s.locale=$2`, scope, locale))
}
func (r *LegalRepository) Resolve(ctx context.Context, id string) (legal.Snapshot, error) {
	return scanLegalSnapshot(r.db.QueryRowContext(ctx, `SELECT payload FROM hhc_web.legal_snapshot WHERE id=$1`, id))
}

func validateLegalProvenance(ctx context.Context, actor string) error {
	p, ok := auditclient.ProvenanceFromContext(ctx)
	if !ok || p.ActorID != actor || !auditclient.ValidActorRequest(p.ActorID, p.RequestID) {
		return content.ErrAuditUnavailable
	}
	return nil
}
func recordLegalChange(ctx context.Context, tx *sql.Tx, scope legal.Scope, action string, version int64, actor string) error {
	p, _ := auditclient.ProvenanceFromContext(ctx)
	_, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.legal_change(id,scope,action,draft_version,actor_id,request_id) VALUES($1,$2,$3,$4,$5,$6)`, uuid.NewString(), scope, action, version, actor, p.RequestID)
	return err
}
