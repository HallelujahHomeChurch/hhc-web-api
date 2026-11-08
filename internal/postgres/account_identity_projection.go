package postgres

import (
	"context"
	"sort"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/accountclient"
)

func (r *Repository) ListStaleWatermarkIdentityIDs(ctx context.Context, before time.Time, limit int) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `WITH referenced AS (
		SELECT actor_id AS account_user_id FROM hhc_web.bulletin_watermark_investigation
		UNION SELECT user_id FROM hhc_web.bulletin_watermark_receipt
	) SELECT referenced.account_user_id::text FROM referenced
	LEFT JOIN hhc_web.account_identity_projection projection USING(account_user_id)
	WHERE projection.account_user_id IS NULL OR projection.refreshed_at<$1
	ORDER BY COALESCE(projection.refreshed_at,'epoch'),referenced.account_user_id LIMIT $2`, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (r *Repository) UpsertWatermarkIdentities(ctx context.Context, ids []string, identities []accountclient.Identity, now time.Time) error {
	resolved := make(map[string]accountclient.Identity, len(identities))
	for _, identity := range identities {
		resolved[identity.ID] = identity
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	for _, id := range ids {
		if err := lockWatermarkSubject(ctx, tx, id); err != nil {
			return err
		}
		var restricted bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.bulletin_watermark_subject WHERE subject_hash=$1)`, watermarkSubjectHash(id)).Scan(&restricted); err != nil {
			return err
		}
		if restricted {
			continue
		}
		identity, available := resolved[id]
		if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.account_identity_projection(account_user_id,display_name,email,available,refreshed_at) VALUES($1,$2,$3,$4,$5)
			ON CONFLICT(account_user_id) DO UPDATE SET display_name=EXCLUDED.display_name,email=EXCLUDED.email,available=EXCLUDED.available,refreshed_at=EXCLUDED.refreshed_at`, id, identity.DisplayName, identity.Email, available, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
