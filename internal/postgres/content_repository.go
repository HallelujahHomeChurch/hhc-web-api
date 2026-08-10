package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

func (r *Repository) CreateContent(ctx context.Context, module content.Module, input content.WriteInput, actor, key string, now time.Time) (content.Item, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return content.Item{}, err
	}
	defer tx.Rollback()
	id := platform.NewID()
	encoded, err := json.Marshal(struct {
		Module content.Module     `json:"module"`
		Input  content.WriteInput `json:"input"`
	}{Module: module, Input: input})
	if err != nil {
		return content.Item{}, err
	}
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(encoded))
	result, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.content_entry(id,module,status,version,idempotency_key,idempotency_fingerprint,created_by,updated_by,created_at,updated_at) VALUES($1,$2,'draft',1,$3,$4,$5,$5,$6,$6) ON CONFLICT(idempotency_key) DO NOTHING`, id, module, key, fingerprint, actor, now)
	if err != nil {
		return content.Item{}, mapContentConflict(err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		var existingModule content.Module
		var existingFingerprint string
		if err := tx.QueryRowContext(ctx, `SELECT id::text,module,idempotency_fingerprint FROM hhc_web.content_entry WHERE idempotency_key=$1`, key).Scan(&id, &existingModule, &existingFingerprint); err != nil {
			return content.Item{}, err
		}
		if existingModule != module || existingFingerprint != fingerprint {
			return content.Item{}, content.ErrConflict
		}
		item, err := loadContent(ctx, tx, module, id)
		if err != nil {
			return content.Item{}, err
		}
		return item, tx.Commit()
	}
	if err := writeTypedContent(ctx, tx, module, id, input, true); err != nil {
		return content.Item{}, mapContentConflict(err)
	}
	if err := replaceTranslations(ctx, tx, id, input.Translations); err != nil {
		return content.Item{}, err
	}
	item, err := loadContent(ctx, tx, module, id)
	if err != nil {
		return content.Item{}, err
	}
	if err := insertRevision(ctx, tx, item, actor, now); err != nil {
		return content.Item{}, err
	}
	return item, tx.Commit()
}

func (r *Repository) ListContent(ctx context.Context, module content.Module, options content.ListOptions) (content.Page, error) {
	where := ` WHERE e.module=$1`
	args := []any{module}
	if options.Status != "" {
		args = append(args, options.Status)
		where += fmt.Sprintf(" AND e.status=$%d", len(args))
	}
	if options.Query != "" {
		args = append(args, options.Query)
		where += fmt.Sprintf(` AND (
			EXISTS(SELECT 1 FROM hhc_web.content_translation search WHERE search.entry_id=e.id AND strpos(lower(search.title || ' ' || search.summary || ' ' || search.body),lower($%d))>0)
			OR EXISTS(SELECT 1 FROM hhc_web.video_item search_video WHERE search_video.entry_id=e.id AND strpos(lower(search_video.youtube_video_id),lower($%d))>0)
		)`, len(args), len(args))
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.content_entry e`+where, args...).Scan(&total); err != nil {
		return content.Page{}, err
	}
	order, join := contentOrdering(module, options.Sort, options.Direction)
	args = append(args, options.PageSize, (options.Page-1)*options.PageSize)
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
		WITH selected AS (
			SELECT e.id,row_number() OVER (ORDER BY %s) AS ordinal
			FROM hhc_web.content_entry e
			%s
			%s
			ORDER BY %s
			LIMIT $%d OFFSET $%d
		)
		SELECT e.id::text,e.module,e.status,e.version,e.created_by,e.updated_by,e.published_at,e.created_at,e.updated_at,
			COALESCE(n.slug,''),COALESCE(n.display_date::text,''),COALESCE(n.cover_asset_id,''),COALESCE(n.home_cover_asset_id,''),COALESCE(n.detail_layout,'top'),COALESCE(n.featured,false),
			COALESCE(n.public_grant_id,''),COALESCE(n.home_public_grant_id,''),COALESCE(n.published_cover_asset_id,''),COALESCE(n.published_home_cover_asset_id,''),
			COALESCE(h.event_date,''),COALESCE(v.youtube_video_id,''),COALESCE(v.home_eligible,false),
			p.published_version,t.locale,t.title,t.summary,'' AS body,t.date_label,t.image_alt
		FROM selected s
		JOIN hhc_web.content_entry e ON e.id=s.id
		LEFT JOIN hhc_web.news_item n ON n.entry_id=e.id
		LEFT JOIN hhc_web.history_event h ON h.entry_id=e.id
		LEFT JOIN hhc_web.video_item v ON v.entry_id=e.id
		LEFT JOIN LATERAL (
			SELECT COALESCE(MAX(version),0) AS published_version
			FROM hhc_web.public_projection
			WHERE resource_type=e.module AND resource_id=e.id
		) p ON true
		JOIN hhc_web.content_translation t ON t.entry_id=e.id
		ORDER BY s.ordinal,t.locale`,
		order, join, where, order, len(args)-1, len(args)), args...)
	if err != nil {
		return content.Page{}, err
	}
	defer rows.Close()
	items := []content.Item{}
	indexes := map[string]int{}
	for rows.Next() {
		var item content.Item
		var translation content.Translation
		if err := rows.Scan(
			&item.ID, &item.Module, &item.Status, &item.Version, &item.CreatedBy, &item.UpdatedBy, &item.PublishedAt, &item.CreatedAt, &item.UpdatedAt,
			&item.Slug, &item.DisplayDate, &item.CoverAssetID, &item.HomeCoverAssetID, &item.DetailLayout, &item.Featured, &item.PublicGrantID, &item.HomePublicGrantID, &item.PublishedCoverID, &item.PublishedHomeCoverID,
			&item.EventDate, &item.YouTubeVideoID, &item.HomeEligible, &item.PublishedVersion,
			&translation.Locale, &translation.Title, &translation.Summary, &translation.Body, &translation.DateLabel, &translation.ImageAlt,
		); err != nil {
			return content.Page{}, err
		}
		item.IsPublished = item.PublishedVersion > 0
		index, exists := indexes[item.ID]
		if !exists {
			index = len(items)
			indexes[item.ID] = index
			items = append(items, item)
		}
		items[index].Translations = append(items[index].Translations, translation)
	}
	return content.Page{Items: items, Page: options.Page, PageSize: options.PageSize, Total: total}, rows.Err()
}

func (r *Repository) GetContent(ctx context.Context, module content.Module, id string) (content.Item, error) {
	return loadContent(ctx, r.db, module, id)
}

func (r *Repository) UpdateContent(ctx context.Context, module content.Module, id string, expected int64, input content.WriteInput, actor string, now time.Time) (content.Item, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return content.Item{}, err
	}
	defer tx.Rollback()
	if err := lockContentVersion(ctx, tx, module, id, expected); err != nil {
		return content.Item{}, err
	}
	if err := writeTypedContent(ctx, tx, module, id, input, false); err != nil {
		return content.Item{}, mapContentConflict(err)
	}
	if err := replaceTranslations(ctx, tx, id, input.Translations); err != nil {
		return content.Item{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='draft',version=version+1,updated_by=$2,updated_at=$3 WHERE id=$1`, id, actor, now); err != nil {
		return content.Item{}, err
	}
	item, err := loadContent(ctx, tx, module, id)
	if err != nil {
		return content.Item{}, err
	}
	if err := insertRevision(ctx, tx, item, actor, now); err != nil {
		return content.Item{}, err
	}
	return item, tx.Commit()
}

func (r *Repository) PublishContent(ctx context.Context, module content.Module, id string, expected int64, actor string, now time.Time) (content.Item, error) {
	if module == content.ModuleNews {
		return r.startNewsPublish(ctx, id, expected, actor, now)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return content.Item{}, err
	}
	defer tx.Rollback()
	if err := lockContentVersion(ctx, tx, module, id, expected); err != nil {
		return content.Item{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='published',version=version+1,published_at=$2,updated_by=$3,updated_at=$2 WHERE id=$1`, id, now, actor); err != nil {
		return content.Item{}, err
	}
	item, err := loadContent(ctx, tx, module, id)
	if err != nil {
		return content.Item{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.public_projection WHERE resource_type=$1 AND resource_id=$2`, module, id); err != nil {
		return content.Item{}, err
	}
	for _, translation := range item.Translations {
		public := publicContent(item, translation)
		payload, _ := json.Marshal(public)
		etag := fmt.Sprintf(`%x`, sha256.Sum256(payload))
		key := fmt.Sprintf("%s:%s:%s", module, translation.Locale, id)
		if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.public_projection(projection_key,resource_type,resource_id,locale,route_path,version,etag,payload_json,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(projection_key) DO UPDATE SET route_path=EXCLUDED.route_path,version=EXCLUDED.version,etag=EXCLUDED.etag,payload_json=EXCLUDED.payload_json,updated_at=EXCLUDED.updated_at`, key, module, id, translation.Locale, public.Href, item.Version, etag, payload, now); err != nil {
			return content.Item{}, err
		}
	}
	if err := insertRevision(ctx, tx, item, actor, now); err != nil {
		return content.Item{}, err
	}
	return item, tx.Commit()
}

func (r *Repository) UnpublishContent(ctx context.Context, module content.Module, id string, expected int64, actor string, now time.Time) (content.Item, error) {
	if module == content.ModuleNews {
		return r.startNewsUnpublish(ctx, id, expected, actor, now)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return content.Item{}, err
	}
	defer tx.Rollback()
	if err := lockContentVersion(ctx, tx, module, id, expected); err != nil {
		return content.Item{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='unpublished',version=version+1,updated_by=$2,updated_at=$3 WHERE id=$1`, id, actor, now); err != nil {
		return content.Item{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.public_projection WHERE resource_type=$1 AND resource_id=$2`, module, id); err != nil {
		return content.Item{}, err
	}
	item, err := loadContent(ctx, tx, module, id)
	if err != nil {
		return content.Item{}, err
	}
	if err := insertRevision(ctx, tx, item, actor, now); err != nil {
		return content.Item{}, err
	}
	return item, tx.Commit()
}

func (r *Repository) startNewsPublish(ctx context.Context, id string, expected int64, actor string, now time.Time) (content.Item, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return content.Item{}, err
	}
	defer tx.Rollback()
	if err := lockContentVersion(ctx, tx, content.ModuleNews, id, expected); err != nil {
		return content.Item{}, err
	}
	current, err := loadContent(ctx, tx, content.ModuleNews, id)
	if err != nil {
		return content.Item{}, err
	}
	if current.Status == content.StatusPublishing || current.Status == content.StatusUnpublishing {
		return content.Item{}, content.ErrNotPublishable
	}
	next := expected + 1
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='publishing',version=$2,updated_by=$3,updated_at=$4 WHERE id=$1`, id, next, actor, now); err != nil {
		return content.Item{}, err
	}
	payload, _ := json.Marshal(publication.ContentPublishPayload{ContentID: id, AssetID: current.CoverAssetID, HomeAssetID: current.HomeCoverAssetID, AggregateVersion: next})
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at)
		VALUES($1,'asset-api','news.publish.ensure_asset','news',$2,$3,$4,$5,'pending',$6,$6,$6)`,
		platform.NewID(), id, next, payload, fmt.Sprintf("news:%s:publish:v%d", id, next), now); err != nil {
		return content.Item{}, err
	}
	item, err := loadContent(ctx, tx, content.ModuleNews, id)
	if err != nil {
		return content.Item{}, err
	}
	if err := insertRevision(ctx, tx, item, actor, now); err != nil {
		return content.Item{}, err
	}
	return item, tx.Commit()
}

func (r *Repository) startNewsUnpublish(ctx context.Context, id string, expected int64, actor string, now time.Time) (content.Item, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return content.Item{}, err
	}
	defer tx.Rollback()
	if err := lockContentVersion(ctx, tx, content.ModuleNews, id, expected); err != nil {
		return content.Item{}, err
	}
	current, err := loadContent(ctx, tx, content.ModuleNews, id)
	if err != nil {
		return content.Item{}, err
	}
	if !current.IsPublished || current.Status == content.StatusPublishing || current.Status == content.StatusUnpublishing {
		return content.Item{}, content.ErrNotPublishable
	}
	next := expected + 1
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='unpublishing',version=$2,updated_by=$3,updated_at=$4 WHERE id=$1`, id, next, actor, now); err != nil {
		return content.Item{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.public_projection WHERE resource_type='news' AND resource_id=$1`, id); err != nil {
		return content.Item{}, err
	}
	assets := publishedAssets(current)
	payload, _ := json.Marshal(publication.ContentUnpublishPayload{ContentID: id, Assets: assets, AggregateVersion: next})
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at)
		VALUES($1,'asset-api','news.unpublish.revoke_asset','news',$2,$3,$4,$5,'pending',$6,$6,$6)`,
		platform.NewID(), id, next, payload, fmt.Sprintf("news:%s:unpublish:v%d", id, next), now); err != nil {
		return content.Item{}, err
	}
	item, err := loadContent(ctx, tx, content.ModuleNews, id)
	if err != nil {
		return content.Item{}, err
	}
	return item, tx.Commit()
}

func (r *Repository) CompleteContentPublish(ctx context.Context, event publication.Event, assets []publication.PublishedAsset, now time.Time) error {
	var payload publication.ContentPublishPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	delivered, err := eventDelivered(ctx, tx, event.ID)
	if err != nil {
		return err
	}
	if delivered {
		return nil
	}
	var status, oldAssetID, oldGrantID, oldHomeAssetID, oldHomeGrantID string
	var version int64
	if err := tx.QueryRowContext(ctx, `
		SELECT e.status,e.version,n.published_cover_asset_id,n.public_grant_id,n.published_home_cover_asset_id,n.home_public_grant_id
		FROM hhc_web.content_entry e
		JOIN hhc_web.news_item n ON n.entry_id=e.id
		WHERE e.id=$1 AND e.module='news'
		FOR UPDATE OF e,n`, payload.ContentID).Scan(&status, &version, &oldAssetID, &oldGrantID, &oldHomeAssetID, &oldHomeGrantID); err != nil {
		return err
	}
	if status != content.StatusPublishing || version != payload.AggregateVersion {
		return publication.ErrStalePublication
	}
	item, err := loadContent(ctx, tx, content.ModuleNews, payload.ContentID)
	if err != nil {
		return err
	}
	item.Status = content.StatusPublished
	item.PublishedAt = &now
	detail, home := publishedAsset(assets, "detail"), publishedAsset(assets, "home")
	if detail.AssetID != payload.AssetID || home.AssetID != payload.HomeAssetID {
		return fmt.Errorf("published asset set does not match event payload")
	}
	item.CoverURL = detail.PublicURL
	item.HomeCoverURL = home.PublicURL
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.public_projection WHERE resource_type='news' AND resource_id=$1`, item.ID); err != nil {
		return err
	}
	for _, translation := range item.Translations {
		public := publicContent(item, translation)
		encoded, err := json.Marshal(public)
		if err != nil {
			return err
		}
		etag := fmt.Sprintf(`%x`, sha256.Sum256(encoded))
		key := fmt.Sprintf("%s:%s:%s", content.ModuleNews, translation.Locale, item.ID)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO hhc_web.public_projection(projection_key,resource_type,resource_id,locale,route_path,version,etag,payload_json,updated_at)
			VALUES($1,'news',$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT(projection_key) DO UPDATE SET route_path=EXCLUDED.route_path,version=EXCLUDED.version,etag=EXCLUDED.etag,payload_json=EXCLUDED.payload_json,updated_at=EXCLUDED.updated_at`,
			key, item.ID, translation.Locale, public.Href, version, etag, encoded, now); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='published',published_at=$2,updated_at=$2 WHERE id=$1`, item.ID, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.news_item SET public_grant_id=$2,published_cover_asset_id=$3,home_public_grant_id=$4,published_home_cover_asset_id=$5,published_version=$6 WHERE entry_id=$1`, item.ID, detail.GrantID, detail.AssetID, home.GrantID, home.AssetID, version); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='delivered',claimed_until=NULL,last_error='',updated_at=$2 WHERE id=$1`, event.ID, now); err != nil {
		return err
	}
	for _, old := range []publication.PublishedAsset{{Usage: "detail", AssetID: oldAssetID, GrantID: oldGrantID}, {Usage: "home", AssetID: oldHomeAssetID, GrantID: oldHomeGrantID}} {
		current := publishedAsset(assets, old.Usage)
		if old.GrantID != "" && old.GrantID != current.GrantID {
			if err := enqueueGrantRevoke(ctx, tx, item.ID, version, old, now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (r *Repository) CompleteContentUnpublish(ctx context.Context, event publication.Event, now time.Time) error {
	var payload publication.ContentUnpublishPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	delivered, err := eventDelivered(ctx, tx, event.ID)
	if err != nil {
		return err
	}
	if delivered {
		return nil
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE hhc_web.content_entry
		SET status='unpublished',updated_at=$3
		WHERE id=$1 AND module='news' AND version=$2 AND status='unpublishing'`,
		payload.ContentID, payload.AggregateVersion, now)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return publication.ErrStalePublication
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.news_item SET public_grant_id='',published_cover_asset_id='',home_public_grant_id='',published_home_cover_asset_id='',published_version=NULL WHERE entry_id=$1`, payload.ContentID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='delivered',claimed_until=NULL,last_error='',updated_at=$2 WHERE id=$1`, event.ID, now); err != nil {
		return err
	}
	return tx.Commit()
}

func publishedAssets(item content.Item) []publication.PublishedAsset {
	assets := make([]publication.PublishedAsset, 0, 2)
	if item.PublishedCoverID != "" && item.PublicGrantID != "" {
		assets = append(assets, publication.PublishedAsset{Usage: "detail", AssetID: item.PublishedCoverID, GrantID: item.PublicGrantID})
	}
	if item.PublishedHomeCoverID != "" && item.HomePublicGrantID != "" {
		assets = append(assets, publication.PublishedAsset{Usage: "home", AssetID: item.PublishedHomeCoverID, GrantID: item.HomePublicGrantID})
	}
	return assets
}

func publishedAsset(assets []publication.PublishedAsset, usage string) publication.PublishedAsset {
	for _, asset := range assets {
		if asset.Usage == usage {
			return asset
		}
	}
	return publication.PublishedAsset{Usage: usage}
}

func enqueueGrantRevoke(ctx context.Context, tx *sql.Tx, contentID string, version int64, asset publication.PublishedAsset, now time.Time) error {
	payload, _ := json.Marshal(publication.ContentUnpublishPayload{ContentID: contentID, AssetID: asset.AssetID, GrantID: asset.GrantID, AggregateVersion: version})
	_, err := tx.ExecContext(ctx, `
		INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at)
		VALUES($1,'asset-api','asset.grant.revoke','news',$2,$3,$4,$5,'pending',$6,$6,$6)
		ON CONFLICT(destination,idempotency_key) DO NOTHING`,
		platform.NewID(), contentID, version, payload, fmt.Sprintf("news:%s:revoke:%s", contentID, asset.GrantID), now)
	return err
}

func (r *Repository) ContentRevisions(ctx context.Context, module content.Module, id string) ([]content.Revision, error) {
	if _, err := loadContent(ctx, r.db, module, id); err != nil {
		return nil, err
	}
	rows, err := r.db.QueryContext(ctx, `SELECT version,snapshot_json,created_by,created_at FROM hhc_web.content_revision WHERE entry_id=$1 ORDER BY version DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []content.Revision{}
	for rows.Next() {
		var value content.Revision
		var payload []byte
		if err := rows.Scan(&value.Version, &payload, &value.CreatedBy, &value.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &value.Snapshot); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *Repository) ContentRevision(ctx context.Context, module content.Module, id string, revision int64) (content.Revision, error) {
	var value content.Revision
	var payload []byte
	err := r.db.QueryRowContext(ctx, `
		SELECT r.version,r.snapshot_json,r.created_by,r.created_at
		FROM hhc_web.content_revision r
		JOIN hhc_web.content_entry e ON e.id=r.entry_id
		WHERE r.entry_id=$1 AND e.module=$2 AND r.version=$3`, id, module, revision).
		Scan(&value.Version, &payload, &value.CreatedBy, &value.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return content.Revision{}, content.ErrNotFound
	}
	if err != nil {
		return content.Revision{}, err
	}
	if err := json.Unmarshal(payload, &value.Snapshot); err != nil {
		return content.Revision{}, err
	}
	return value, nil
}

func (r *Repository) DeleteContent(ctx context.Context, module content.Module, id string, expected int64, actor string, now time.Time) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	current, status, err := lockContent(ctx, tx, module, id)
	if err != nil {
		return err
	}
	if current != expected {
		return content.ErrPrecondition
	}
	if status == content.StatusPublishing || status == content.StatusPublished || status == content.StatusUnpublishing || status == content.StatusUnpublishFailed {
		return content.ErrConflict
	}
	hasPublicState, err := hasPublicContentState(ctx, tx, module, id)
	if err != nil {
		return err
	}
	if hasPublicState {
		return content.ErrConflict
	}
	assetIDs, err := contentAssetIDs(ctx, tx, module, id)
	if err != nil {
		return err
	}
	if err := insertDeleteAudit(ctx, tx, string(module), id, current, actor, assetIDs, now); err != nil {
		return err
	}
	if err := enqueueAssetDeletes(ctx, tx, string(module), id, current, assetIDs, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.public_projection WHERE resource_type=$1 AND resource_id=$2`, module, id); err != nil {
		return err
	}
	if result, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.content_entry WHERE id=$1 AND module=$2`, id, module); err != nil {
		return err
	} else if affected, _ := result.RowsAffected(); affected != 1 {
		return content.ErrNotFound
	}
	return tx.Commit()
}

func contentAssetIDs(ctx context.Context, tx *sql.Tx, module content.Module, id string) ([]string, error) {
	if module != content.ModuleNews {
		return nil, nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT asset_id FROM (
			SELECT cover_asset_id AS asset_id FROM hhc_web.news_item WHERE entry_id=$1
			UNION
			SELECT home_cover_asset_id AS asset_id FROM hhc_web.news_item WHERE entry_id=$1
			UNION
			SELECT snapshot_json->>'coverAssetId' FROM hhc_web.content_revision WHERE entry_id=$1
			UNION
			SELECT snapshot_json->>'homeCoverAssetId' FROM hhc_web.content_revision WHERE entry_id=$1
		) assets WHERE COALESCE(asset_id,'')<>'' ORDER BY asset_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func insertDeleteAudit(ctx context.Context, tx *sql.Tx, resourceType, resourceID string, version int64, actor string, assetIDs []string, now time.Time) error {
	payload, err := json.Marshal(map[string]any{"version": version, "assetIds": assetIDs})
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.cms_audit_event(id,action,resource_type,resource_id,actor,payload_json,created_at) VALUES($1,'delete',$2,$3,$4,$5,$6)`, platform.NewID(), resourceType, resourceID, actor, payload, now)
	return err
}

func enqueueAssetDeletes(ctx context.Context, tx *sql.Tx, resourceType, resourceID string, version int64, assetIDs []string, now time.Time) error {
	for _, assetID := range assetIDs {
		payload, _ := json.Marshal(map[string]any{"assetId": assetID})
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at)
			VALUES($1,'asset-api','asset.owner.delete',$2,$3,$4,$5,$6,'pending',$7,$7,$7)
			ON CONFLICT(destination,idempotency_key) DO NOTHING`,
			platform.NewID(), resourceType, resourceID, version, payload, fmt.Sprintf("%s:%s:delete:%s", resourceType, resourceID, assetID), now); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) PublicContent(ctx context.Context, module content.Module, locale string, page, pageSize int) (content.PublicPage, error) {
	order := publicContentOrdering(module)
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT count(DISTINCT resource_id) FROM hhc_web.public_projection WHERE resource_type=$1 AND locale IN ($2,'zh-Hant')`, module, locale).Scan(&total); err != nil {
		return content.PublicPage{}, err
	}
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
		WITH localized AS (
			SELECT DISTINCT ON (resource_id) resource_id,locale,payload_json,updated_at
			FROM hhc_web.public_projection
			WHERE resource_type=$1 AND locale IN ($2,'zh-Hant')
			ORDER BY resource_id,CASE WHEN locale=$2 THEN 0 ELSE 1 END
		), paged AS (
			SELECT resource_id,locale,payload_json,updated_at
			FROM localized
			ORDER BY %s
			LIMIT $3 OFFSET $4
		), availability AS (
			SELECT projection.resource_id AS available_resource_id,
				jsonb_agg(projection.locale ORDER BY projection.locale) AS locales
			FROM hhc_web.public_projection projection
			JOIN paged ON paged.resource_id=projection.resource_id
			WHERE projection.resource_type=$1
			GROUP BY projection.resource_id
		)
		SELECT paged.resource_id,paged.locale,paged.payload_json,availability.locales
		FROM paged
		JOIN availability ON availability.available_resource_id=paged.resource_id
		ORDER BY %s`, order, order), module, locale, pageSize, (page-1)*pageSize)
	if err != nil {
		return content.PublicPage{}, err
	}
	defer rows.Close()
	values := []content.PublicItem{}
	for rows.Next() {
		var id, resolvedLocale string
		var payload, availableLocales []byte
		var value content.PublicItem
		if err := rows.Scan(&id, &resolvedLocale, &payload, &availableLocales); err != nil {
			return content.PublicPage{}, err
		}
		if err := json.Unmarshal(payload, &value); err != nil {
			return content.PublicPage{}, err
		}
		if err := enrichPublicItem(&value, id, locale, resolvedLocale, availableLocales); err != nil {
			return content.PublicPage{}, err
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return content.PublicPage{}, err
	}
	return content.PublicPage{Items: values, Page: page, PageSize: pageSize, Total: total}, nil
}

func (r *Repository) PublicNews(ctx context.Context, locale, slug string) (content.PublicItem, string, error) {
	var id, resolvedLocale string
	var payload, availableLocales []byte
	var etag string
	err := r.db.QueryRowContext(ctx, `
		WITH selected AS (
			SELECT resource_id,locale,payload_json,etag
			FROM hhc_web.public_projection
			WHERE resource_type='news'
				AND locale IN ($1,'zh-Hant')
				AND route_path IN ('/' || $1 || '/news/' || $2,'/zh-Hant/news/' || $2)
			ORDER BY CASE WHEN locale=$1 THEN 0 ELSE 1 END
			LIMIT 1
		), availability AS (
			SELECT projection.resource_id AS available_resource_id,
				jsonb_agg(projection.locale ORDER BY projection.locale) AS locales
			FROM hhc_web.public_projection projection
			JOIN selected ON selected.resource_id=projection.resource_id
			WHERE projection.resource_type='news'
			GROUP BY projection.resource_id
		)
		SELECT selected.resource_id,selected.locale,selected.payload_json,selected.etag,availability.locales
		FROM selected
		JOIN availability ON availability.available_resource_id=selected.resource_id`, locale, slug).
		Scan(&id, &resolvedLocale, &payload, &etag, &availableLocales)
	if errors.Is(err, sql.ErrNoRows) {
		return content.PublicItem{}, "", content.ErrNotFound
	}
	if err != nil {
		return content.PublicItem{}, "", err
	}
	var item content.PublicItem
	if err := json.Unmarshal(payload, &item); err != nil {
		return content.PublicItem{}, "", err
	}
	if err := enrichPublicItem(&item, id, locale, resolvedLocale, availableLocales); err != nil {
		return content.PublicItem{}, "", err
	}
	return item, publicResponseETag(etag, locale, item), nil
}

func enrichPublicItem(value *content.PublicItem, id, requestedLocale, resolvedLocale string, availableLocales []byte) error {
	if err := json.Unmarshal(availableLocales, &value.AvailableLocales); err != nil {
		return err
	}
	if value.ID == "" {
		value.ID = id
	}
	sortPublicLocales(value.AvailableLocales)
	value.ResolvedLocale = resolvedLocale
	value.Href = localizedPublicHref(value.Href, resolvedLocale, requestedLocale)
	return nil
}

func sortPublicLocales(locales []string) {
	sort.Slice(locales, func(i, j int) bool {
		left, right := publicLocaleRank(locales[i]), publicLocaleRank(locales[j])
		if left == right {
			return locales[i] < locales[j]
		}
		return left < right
	})
}

func publicLocaleRank(locale string) int {
	switch locale {
	case "zh-Hant":
		return 0
	case "zh-Hans":
		return 1
	case "en":
		return 2
	case "ja":
		return 3
	case "ko":
		return 4
	default:
		return 5
	}
}

func publicResponseETag(projectionETag, requestedLocale string, item content.PublicItem) string {
	value := strings.Join([]string{projectionETag, requestedLocale, item.ResolvedLocale, strings.Join(item.AvailableLocales, ",")}, "\x00")
	return fmt.Sprintf("%x", sha256.Sum256([]byte(value)))
}

func localizedPublicHref(href, resolvedLocale, requestedLocale string) string {
	prefix := "/" + resolvedLocale + "/"
	if strings.HasPrefix(href, prefix) {
		return "/" + requestedLocale + "/" + strings.TrimPrefix(href, prefix)
	}
	return href
}

func lockContent(ctx context.Context, tx *sql.Tx, module content.Module, id string) (int64, string, error) {
	var version int64
	var status string
	err := tx.QueryRowContext(ctx, `SELECT version,status FROM hhc_web.content_entry WHERE id=$1 AND module=$2 FOR UPDATE`, id, module).Scan(&version, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", content.ErrNotFound
	}
	return version, status, err
}

func hasPublicContentState(ctx context.Context, tx *sql.Tx, module content.Module, id string) (bool, error) {
	var hasProjection bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.public_projection WHERE resource_type=$1 AND resource_id=$2)`, module, id).Scan(&hasProjection); err != nil {
		return false, err
	}
	if module != content.ModuleNews {
		return hasProjection, nil
	}
	var hasGrant bool
	if err := tx.QueryRowContext(ctx, `SELECT public_grant_id<>'' OR home_public_grant_id<>'' FROM hhc_web.news_item WHERE entry_id=$1`, id).Scan(&hasGrant); err != nil {
		return false, err
	}
	return hasProjection || hasGrant, nil
}

type contentQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func loadContent(ctx context.Context, query contentQueryer, module content.Module, id string) (content.Item, error) {
	var item content.Item
	err := query.QueryRowContext(ctx, `SELECT id::text,module,status,version,created_by,updated_by,published_at,created_at,updated_at FROM hhc_web.content_entry WHERE id=$1 AND module=$2`, id, module).Scan(&item.ID, &item.Module, &item.Status, &item.Version, &item.CreatedBy, &item.UpdatedBy, &item.PublishedAt, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return content.Item{}, content.ErrNotFound
	}
	if err != nil {
		return content.Item{}, err
	}
	switch module {
	case content.ModuleNews:
		var publishedVersion sql.NullInt64
		err = query.QueryRowContext(ctx, `SELECT slug,display_date::text,cover_asset_id,home_cover_asset_id,detail_layout,featured,public_grant_id,home_public_grant_id,published_cover_asset_id,published_home_cover_asset_id,published_version FROM hhc_web.news_item WHERE entry_id=$1`, id).
			Scan(&item.Slug, &item.DisplayDate, &item.CoverAssetID, &item.HomeCoverAssetID, &item.DetailLayout, &item.Featured, &item.PublicGrantID, &item.HomePublicGrantID, &item.PublishedCoverID, &item.PublishedHomeCoverID, &publishedVersion)
		if publishedVersion.Valid {
			item.PublishedVersion = publishedVersion.Int64
		}
	case content.ModuleHistory:
		err = query.QueryRowContext(ctx, `SELECT COALESCE(event_date,'') FROM hhc_web.history_event WHERE entry_id=$1`, id).Scan(&item.EventDate)
	case content.ModuleVideos:
		err = query.QueryRowContext(ctx, `SELECT youtube_video_id,home_eligible FROM hhc_web.video_item WHERE entry_id=$1`, id).Scan(&item.YouTubeVideoID, &item.HomeEligible)
	}
	if err != nil {
		return content.Item{}, err
	}
	if err := query.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.public_projection WHERE resource_type=$1 AND resource_id=$2),COALESCE(MAX(version),0) FROM hhc_web.public_projection WHERE resource_type=$1 AND resource_id=$2`, module, id).
		Scan(&item.IsPublished, &item.PublishedVersion); err != nil {
		return content.Item{}, err
	}
	rows, err := query.QueryContext(ctx, `SELECT locale,title,summary,body,date_label,image_alt FROM hhc_web.content_translation WHERE entry_id=$1 ORDER BY locale`, id)
	if err != nil {
		return content.Item{}, err
	}
	defer rows.Close()
	item.Translations = []content.Translation{}
	for rows.Next() {
		var value content.Translation
		if err := rows.Scan(&value.Locale, &value.Title, &value.Summary, &value.Body, &value.DateLabel, &value.ImageAlt); err != nil {
			return content.Item{}, err
		}
		item.Translations = append(item.Translations, value)
	}
	return item, rows.Err()
}

func contentOrdering(module content.Module, sort, direction string) (string, string) {
	column, join := "e.updated_at", ""
	switch {
	case module == content.ModuleNews && sort == "displayDate":
		column, join = "n.display_date", "JOIN hhc_web.news_item n ON n.entry_id=e.id"
	case module == content.ModuleHistory && sort == "eventDate":
		column, join = "h.event_date", "JOIN hhc_web.history_event h ON h.entry_id=e.id"
	}
	if direction != "asc" {
		direction = "desc"
	}
	nulls := ""
	if module == content.ModuleHistory && sort == "eventDate" {
		nulls = " NULLS LAST"
	}
	return column + " " + strings.ToUpper(direction) + nulls + ",e.id " + strings.ToUpper(direction), join
}

func publicContentOrdering(module content.Module) string {
	switch module {
	case content.ModuleNews:
		return "payload_json->>'displayDate' DESC, resource_id"
	case content.ModuleHistory:
		return "payload_json->>'eventDate' ASC NULLS LAST, resource_id ASC"
	default:
		return "updated_at DESC, resource_id"
	}
}

func mapContentConflict(err error) error {
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "duplicate key") {
		return content.ErrConflict
	}
	return err
}
func writeTypedContent(ctx context.Context, tx *sql.Tx, module content.Module, id string, input content.WriteInput, insert bool) error {
	verb := "UPDATE"
	if insert {
		verb = "INSERT"
	}
	switch module {
	case content.ModuleNews:
		if input.DetailLayout == "" {
			input.DetailLayout = "top"
		}
		if verb == "INSERT" {
			_, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.news_item(entry_id,slug,display_date,cover_asset_id,home_cover_asset_id,detail_layout,featured) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, input.Slug, input.DisplayDate, input.CoverAssetID, input.HomeCoverAssetID, input.DetailLayout, input.Featured)
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE hhc_web.news_item SET slug=$2,display_date=$3,cover_asset_id=$4,home_cover_asset_id=$5,detail_layout=$6,featured=$7 WHERE entry_id=$1`, id, input.Slug, input.DisplayDate, input.CoverAssetID, input.HomeCoverAssetID, input.DetailLayout, input.Featured)
		return err
	case content.ModuleHistory:
		if verb == "INSERT" {
			// Retain a unique legacy sort_order until a later contract migration removes it.
			if _, err := tx.ExecContext(ctx, `LOCK TABLE hhc_web.history_event IN SHARE ROW EXCLUSIVE MODE`); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.history_event(entry_id,event_date,sort_order) SELECT $1,NULLIF($2,''),COALESCE(MAX(sort_order),0)+1 FROM hhc_web.history_event`, id, input.EventDate)
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE hhc_web.history_event SET event_date=NULLIF($2,'') WHERE entry_id=$1`, id, input.EventDate)
		return err
	case content.ModuleVideos:
		if verb == "INSERT" {
			_, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.video_item(entry_id,youtube_video_id,home_eligible) VALUES($1,$2,$3)`, id, input.YouTubeVideoID, input.HomeEligible)
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE hhc_web.video_item SET youtube_video_id=$2,home_eligible=$3 WHERE entry_id=$1`, id, input.YouTubeVideoID, input.HomeEligible)
		return err
	}
	return content.ErrInvalid
}
func replaceTranslations(ctx context.Context, tx *sql.Tx, id string, values []content.Translation) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.content_translation WHERE entry_id=$1`, id); err != nil {
		return err
	}
	for _, value := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.content_translation(entry_id,locale,title,summary,body,date_label,image_alt) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, value.Locale, value.Title, value.Summary, value.Body, value.DateLabel, value.ImageAlt); err != nil {
			return err
		}
	}
	return nil
}
func lockContentVersion(ctx context.Context, tx *sql.Tx, module content.Module, id string, expected int64) error {
	var current int64
	var status string
	err := tx.QueryRowContext(ctx, `SELECT version,status FROM hhc_web.content_entry WHERE id=$1 AND module=$2 FOR UPDATE`, id, module).Scan(&current, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return content.ErrNotFound
	}
	if err != nil {
		return err
	}
	if current != expected {
		return content.ErrPrecondition
	}
	if status == content.StatusPublishing || status == content.StatusUnpublishing {
		return content.ErrConflict
	}
	return nil
}
func insertRevision(ctx context.Context, tx *sql.Tx, item content.Item, actor string, now time.Time) error {
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.content_revision(entry_id,version,snapshot_json,created_by,created_at) VALUES($1,$2,$3,$4,$5)`, item.ID, item.Version, payload, actor, now)
	return err
}
func publicContent(item content.Item, translation content.Translation) content.PublicItem {
	value := content.PublicItem{ID: item.ID, Title: translation.Title, Summary: translation.Summary, Body: translation.Body, DateLabel: translation.DateLabel, DisplayDate: item.DisplayDate, EventDate: item.EventDate, ImageAlt: translation.ImageAlt, YouTubeVideoID: item.YouTubeVideoID, Featured: item.Featured, HomeEligible: item.HomeEligible, DetailLayout: item.DetailLayout}
	switch item.Module {
	case content.ModuleNews:
		if item.CoverURL != "" {
			value.ImageURL = item.CoverURL + "/large"
		} else if item.CoverAssetID != "" {
			value.ImageURL = "/assets/" + item.CoverAssetID + "/large"
		}
		if item.HomeCoverURL != "" {
			value.HomeImageURL = item.HomeCoverURL + "/large"
		} else if item.HomeCoverAssetID != "" {
			value.HomeImageURL = "/assets/" + item.HomeCoverAssetID + "/large"
		} else {
			value.HomeImageURL = value.ImageURL
		}
		value.Href = "/" + translation.Locale + "/news/" + item.Slug
	case content.ModuleVideos:
		value.ImageURL = "https://i.ytimg.com/vi/" + item.YouTubeVideoID + "/hqdefault.jpg"
		value.Href = "https://www.youtube.com/watch?v=" + item.YouTubeVideoID
	}
	return value
}
