package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
)

// The caller holds the shared statement advisory lock before any entry lock.
func checkStatementWindow(ctx context.Context, tx *sql.Tx, item content.Item) error {
	if item.Kind != content.KindStatement || item.PopupStartsAt == nil || item.PopupEndsAt == nil {
		return nil
	}
	var conflict string
	err := tx.QueryRowContext(ctx, `
 SELECT id FROM (
  SELECT resource_id::text AS id,(payload_json->>'popupStartsAt')::timestamptz AS starts,(payload_json->>'popupEndsAt')::timestamptz AS ends
  FROM hhc_web.public_projection WHERE resource_type='news' AND payload_json->>'kind'='statement'
  UNION ALL
  SELECT e.id::text,n.popup_starts_at,n.popup_ends_at FROM hhc_web.content_entry e
  JOIN hhc_web.news_item n ON n.entry_id=e.id WHERE e.status='publishing' AND n.kind='statement'
 ) windows WHERE id<>$1 AND starts<$3 AND ends>$2 LIMIT 1`, item.ID, item.PopupStartsAt, item.PopupEndsAt).Scan(&conflict)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	return &content.StatementWindowConflict{ContentID: conflict}
}

func (r *Repository) ActiveStatement(ctx context.Context, locale string, now time.Time) (content.ActiveStatement, error) {
	result := content.ActiveStatement{ServerNow: now}
	// ponytail: scan the small statement archive; index window columns if this grows materially.
	rows, err := r.db.QueryContext(ctx, `SELECT resource_id::text,payload_json FROM hhc_web.public_projection WHERE resource_type='news' AND locale='zh-Hant' AND payload_json->>'kind'='statement'`)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	var activeSlug string
	for rows.Next() {
		var id string
		var data []byte
		if err = rows.Scan(&id, &data); err != nil {
			return result, err
		}
		var item content.PublicItem
		if err = json.Unmarshal(data, &item); err != nil {
			return result, err
		}
		for _, boundary := range []*time.Time{item.PopupStartsAt, item.PopupEndsAt} {
			if boundary != nil && boundary.After(now) && (result.NextChangeAt == nil || boundary.Before(*result.NextChangeAt)) {
				value := *boundary
				result.NextChangeAt = &value
			}
		}
		if content.StatementWindowActive(item.PopupStartsAt, item.PopupEndsAt, now) {
			// href is published alongside the snapshot, independent of mutable drafts.
			for i := len(item.Href) - 1; i >= 0; i-- {
				if item.Href[i] == '/' {
					activeSlug = item.Href[i+1:]
					break
				}
			}
		}
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if err = rows.Close(); err != nil {
		return result, err
	}
	if activeSlug != "" {
		item, _, err := r.PublicNews(ctx, locale, activeSlug)
		if errors.Is(err, content.ErrNotFound) {
			return result, nil
		}
		if err != nil {
			return result, err
		}
		// Publication may have changed between the window scan and localized read.
		if content.StatementWindowActive(item.PopupStartsAt, item.PopupEndsAt, now) {
			result.Statement = &item
		}
	}
	return result, nil
}

func (r *Repository) EndStatementPopup(ctx context.Context, id string, expected int64, actor string, now time.Time) (content.Item, error) {
	return r.startNewsPublish(ctx, id, expected, actor, now, true, nil)
}
