package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"time"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinreader"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/google/uuid"
)

func (r *Repository) ReaderState(ctx context.Context, issue, series, locale, owner string, from *int64, now time.Time) (bulletinreader.StateResponse, error) {
	state, mappings, _, err := r.privateReader(ctx, issue, series, locale, owner, from, nil, now)
	return bulletinreader.StateResponse{State: state, FromRevision: from, Mappings: mappings}, err
}
func (r *Repository) MutateReader(ctx context.Context, issue, series, locale, owner string, mutations []bulletinreader.Mutation, now time.Time) (bulletinreader.MutationResponse, error) {
	if len(mutations) < 1 || len(mutations) > 100 {
		return bulletinreader.MutationResponse{}, bulletinreader.ErrInvalid
	}
	state, _, results, err := r.privateReader(ctx, issue, series, locale, owner, nil, mutations, now)
	return bulletinreader.MutationResponse{State: state, Results: results}, err
}

// All replay checks follow current publication/subject authority. The existing
// DSR subject lock also serializes mutation IDs used on different documents.
func (r *Repository) privateReader(ctx context.Context, issue, series, locale, owner string, from *int64, mutations []bulletinreader.Mutation, now time.Time) (state bulletinreader.State, mappings []bulletinreader.DirectMapping, results []bulletinreader.Result, err error) {
	if uuid.Validate(issue) != nil || uuid.Validate(owner) != nil || now.IsZero() || from != nil && (*from < 1 || *from > 9007199254740991) {
		err = bulletinreader.ErrInvalid
		return
	}
	if _, ok := bulletins.NormalizeBulletinEdition(series, locale); !ok {
		err = bulletins.ErrNotFound
		return
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return
	}
	defer tx.Rollback()
	if err = lockWatermarkSubject(ctx, tx, owner); err != nil {
		return
	}
	var restricted bool
	if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.bulletin_watermark_subject WHERE subject_hash=$1)`, watermarkSubjectHash(owner)).Scan(&restricted); err != nil {
		return
	}
	if restricted {
		err = bulletins.ErrNotFound
		return
	}
	var document string
	var current int64
	err = tx.QueryRowContext(ctx, `SELECT id::text,published_revision FROM hhc_web.online_bulletin_document WHERE issue_id=$1 AND series=$2 AND content_locale=$3 AND published_revision IS NOT NULL FOR SHARE`, issue, series, locale).Scan(&document, &current)
	if errors.Is(err, sql.ErrNoRows) {
		err = bulletins.ErrNotFound
	}
	if err != nil {
		return
	}
	state, err = loadReaderState(ctx, tx, owner, document, current, now)
	if err != nil {
		return
	}
	var snapshot bulletinreader.Snapshot
	state, snapshot, mappings, err = migrateReaderState(ctx, tx, state, from, now)
	if err != nil {
		return
	}
	// Expiry is server-owned and bounded. Aged unseen operations still require
	// explicit recovery even after their old dedupe row has been pruned.
	if _, err = tx.ExecContext(ctx, `DELETE FROM hhc_web.bulletin_reader_processed_mutation WHERE (account_id,mutation_id) IN (SELECT account_id,mutation_id FROM hhc_web.bulletin_reader_processed_mutation WHERE account_id=$1 AND expires_at<=$2 ORDER BY expires_at LIMIT 1000)`, owner, now); err != nil {
		return
	}
	results = []bulletinreader.Result{}
	for _, mutation := range mutations {
		if uuid.Validate(mutation.MutationID) != nil {
			err = bulletinreader.ErrInvalid
			return
		}
		var fingerprint string
		fingerprint, err = readerMutationFingerprint(document, mutation)
		if err != nil {
			return
		}
		var previousHash string
		var previousJSON []byte
		err = tx.QueryRowContext(ctx, `SELECT request_hash,result_json FROM hhc_web.bulletin_reader_processed_mutation WHERE account_id=$1 AND mutation_id=$2 AND expires_at>$3`, owner, mutation.MutationID, now).Scan(&previousHash, &previousJSON)
		var result bulletinreader.Result
		if err == nil {
			if previousHash != fingerprint {
				err = bulletinreader.ErrMutationReuse
				return
			}
			if err = json.Unmarshal(previousJSON, &result); err != nil {
				return
			}
		} else {
			if !errors.Is(err, sql.ErrNoRows) {
				return
			}
			state, result, err = bulletinreader.Apply(owner, state, snapshot, mutation, now)
			if err != nil {
				return
			}
			if result.Status != "revision_changed" {
				var raw []byte
				raw, err = json.Marshal(result)
				if err != nil {
					return
				}
				// A bounded prune may leave this expired ID behind; it must not block
				// explicit recovery or return a stale terminal result.
				_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.bulletin_reader_processed_mutation(account_id,mutation_id,document_id,request_hash,result_json,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(account_id,mutation_id) DO UPDATE SET document_id=EXCLUDED.document_id,request_hash=EXCLUDED.request_hash,result_json=EXCLUDED.result_json,created_at=EXCLUDED.created_at,expires_at=EXCLUDED.expires_at WHERE bulletin_reader_processed_mutation.expires_at<=EXCLUDED.created_at`, owner, mutation.MutationID, document, fingerprint, raw, now, now.Add(bulletinreader.MutationRetention))
				if err != nil {
					return
				}
			}
		}
		results = append(results, result)
	}
	if err = saveReaderState(ctx, tx, state, now); err != nil {
		return
	}
	err = tx.Commit()
	return
}

func readerMutationFingerprint(document string, mutation bulletinreader.Mutation) (string, error) {
	// UseNumber avoids collapsing different large numeric payloads to float64.
	decoder := json.NewDecoder(bytes.NewReader(mutation.Payload))
	decoder.UseNumber()
	var payload any
	if !utf8.Valid(mutation.Payload) || decoder.Decode(&payload) != nil || decoder.Decode(new(any)) != io.EOF {
		return "", bulletinreader.ErrInvalid
	}
	canonical, err := json.Marshal(payload)
	if err != nil {
		return "", bulletinreader.ErrInvalid
	}
	mutation.Payload = canonical
	mutation.CreatedAt = mutation.CreatedAt.UTC()
	raw, err := json.Marshal(struct {
		Document string
		Mutation bulletinreader.Mutation
	}{document, mutation})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}
