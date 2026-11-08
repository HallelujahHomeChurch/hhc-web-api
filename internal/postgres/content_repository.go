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

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	"github.com/google/uuid"
)

func (r *Repository) CreateContent(ctx context.Context, module content.Module, input content.WriteInput, actor, key string, now time.Time) (content.Item, error) {
	if err := r.validateContentAudit(ctx, actor); err != nil {
		return content.Item{}, err
	}
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
	if module == content.ModuleNews {
		if err := prepareNewsSummaries(ctx, tx, id, 1, input.Translations, actor, now, true); err != nil {
			return content.Item{}, err
		}
	}
	if err := replaceTranslations(ctx, tx, id, input.Translations); err != nil {
		return content.Item{}, err
	}
	if module == content.ModuleNews && input.Kind == content.KindStatement {
		if err := replaceContentAssetRefs(ctx, tx, id, input.Translations); err != nil {
			return content.Item{}, err
		}
	}
	item, err := loadContent(ctx, tx, module, id)
	if err != nil {
		return content.Item{}, err
	}
	if err := insertRevision(ctx, tx, item, actor, now); err != nil {
		return content.Item{}, err
	}
	if err := touchPageGroup(ctx, tx, module, actor, now); err != nil {
		return content.Item{}, err
	}
	if err := r.enqueueContentAudit(ctx, tx, "cms.content.create", item.ID, module, actor, now); err != nil {
		return content.Item{}, err
	}
	return item, tx.Commit()
}

func (r *Repository) ListContent(ctx context.Context, module content.Module, options content.ListOptions) (content.Page, error) {
	where := ` WHERE e.module=$1`
	args := []any{module}
	if options.Kind != "" {
		args = append(args, options.Kind)
		where += fmt.Sprintf(" AND EXISTS(SELECT 1 FROM hhc_web.news_item nk WHERE nk.entry_id=e.id AND nk.kind=$%d)", len(args))
	}
	if options.Status != "" {
		args = append(args, options.Status)
		where += fmt.Sprintf(" AND e.status=$%d", len(args))
	}
	if options.Query != "" {
		args = append(args, options.Query)
		where += fmt.Sprintf(` AND (
			EXISTS(SELECT 1 FROM hhc_web.content_translation search WHERE search.entry_id=e.id AND strpos(lower(search.title || ' ' || search.summary || ' ' || search.body),lower($%d))>0)
			OR EXISTS(SELECT 1 FROM hhc_web.video_item search_video WHERE search_video.entry_id=e.id AND strpos(lower(search_video.youtube_video_id),lower($%d))>0)
			OR EXISTS(SELECT 1 FROM hhc_web.location_item search_location WHERE search_location.content_id=e.id AND strpos(lower(search_location.stable_key || ' ' || search_location.map_href),lower($%d))>0)
			OR EXISTS(SELECT 1 FROM hhc_web.page_item search_page WHERE search_page.content_id=e.id AND strpos(lower(search_page.page_key),lower($%d))>0)
		)`, len(args), len(args), len(args), len(args))
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
		SELECT e.id::text,e.module,e.status,e.version,e.created_by,e.updated_by,e.first_published_at,e.published_at,e.created_at,e.updated_at,
				COALESCE(n.slug,''),COALESCE(n.display_date::text,''),COALESCE(n.cover_asset_id,''),COALESCE(n.home_cover_asset_id,''),COALESCE(n.detail_layout,''),COALESCE(n.featured,false),COALESCE(n.author_name,''),COALESCE(n.kind,''),n.popup_starts_at,n.popup_ends_at,
				COALESCE(n.public_grant_id,''),COALESCE(n.home_public_grant_id,''),COALESCE(n.published_cover_asset_id,''),COALESCE(n.published_home_cover_asset_id,''),
				COALESCE(h.event_date,''),COALESCE(v.youtube_video_id,''),COALESCE(v.home_eligible,false),
				COALESCE(l.stable_key,''),COALESCE(l.map_href,''),COALESCE(l.sort_order,0),
				COALESCE(pg.page_key,''),COALESCE(pg.page_template,''),COALESCE(pg.route_path,''),COALESCE(pg.indexable,false),
				COALESCE(pg.banner_asset_id,''),COALESCE(pg.published_banner_asset_id,''),COALESCE(pg.banner_public_grant_id,''),COALESCE(pg.published_banner_version,0),COALESCE(pg.home_settings,'{}'::jsonb),
				p.published_version,t.locale,t.title,t.summary,CASE WHEN e.module='locations' THEN t.body ELSE '' END AS body,t.date_label,t.image_alt,t.body_json
		FROM selected s
		JOIN hhc_web.content_entry e ON e.id=s.id
		LEFT JOIN hhc_web.news_item n ON n.entry_id=e.id
		LEFT JOIN hhc_web.history_event h ON h.entry_id=e.id
			LEFT JOIN hhc_web.video_item v ON v.entry_id=e.id
			LEFT JOIN hhc_web.location_item l ON l.content_id=e.id
			LEFT JOIN hhc_web.page_item pg ON pg.content_id=e.id
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
		var bodyJSON, homeSettingsJSON []byte
		if err := rows.Scan(
			&item.ID, &item.Module, &item.Status, &item.Version, &item.CreatedBy, &item.UpdatedBy, &item.FirstPublishedAt, &item.PublishedAt, &item.CreatedAt, &item.UpdatedAt,
			&item.Slug, &item.DisplayDate, &item.CoverAssetID, &item.HomeCoverAssetID, &item.DetailLayout, &item.Featured, &item.AuthorName, &item.Kind, &item.PopupStartsAt, &item.PopupEndsAt, &item.PublicGrantID, &item.HomePublicGrantID, &item.PublishedCoverID, &item.PublishedHomeCoverID,
			&item.EventDate, &item.YouTubeVideoID, &item.HomeEligible, &item.LocationKey, &item.MapHref, &item.SortOrder,
			&item.PageKey, &item.PageTemplate, &item.RoutePath, &item.Indexable,
			&item.BannerAssetID, &item.PublishedBannerAssetID, &item.BannerPublicGrantID, &item.PublishedBannerVersion, &homeSettingsJSON,
			&item.PublishedVersion,
			&translation.Locale, &translation.Title, &translation.Summary, &translation.Body, &translation.DateLabel, &translation.ImageAlt, &bodyJSON,
		); err != nil {
			return content.Page{}, err
		}
		if err := decodeHomeSettings(homeSettingsJSON, &item); err != nil {
			return content.Page{}, err
		}
		translation.BodyJSON = bodyJSON
		item.IsPublished = item.PublishedVersion > 0
		index, exists := indexes[item.ID]
		if !exists {
			index = len(items)
			indexes[item.ID] = index
			items = append(items, item)
		}
		items[index].Translations = append(items[index].Translations, translation)
	}
	if err := rows.Err(); err != nil {
		return content.Page{}, err
	}
	if err := rows.Close(); err != nil {
		return content.Page{}, err
	}
	if module == content.ModuleNews && len(items) > 0 {
		ids := make([]string, len(items))
		for i := range items {
			ids[i] = items[i].ID
			for j := range items[i].Translations {
				items[i].Translations[j].SummaryStatus = "pending"
				if items[i].Kind == content.KindStatement {
					items[i].Translations[j].SummaryStatus = "ready"
				}
			}
		}
		states, err := r.db.QueryContext(ctx, `SELECT entry_id::text,locale,status FROM hhc_web.news_summary_state WHERE entry_id=ANY($1::uuid[])`, ids)
		if err != nil {
			return content.Page{}, err
		}
		defer states.Close()
		for states.Next() {
			var id, locale, status string
			if err := states.Scan(&id, &locale, &status); err != nil {
				return content.Page{}, err
			}
			i := indexes[id]
			for j := range items[i].Translations {
				if items[i].Translations[j].Locale == locale && items[i].Kind != content.KindStatement {
					items[i].Translations[j].SummaryStatus = status
				}
			}
		}
		if err := states.Err(); err != nil {
			return content.Page{}, err
		}
	}
	return content.Page{Items: items, Page: options.Page, PageSize: options.PageSize, Total: total}, nil
}

func (r *Repository) GetContent(ctx context.Context, module content.Module, id string) (content.Item, error) {
	return loadContent(ctx, r.db, module, id)
}

func (r *Repository) UpdateContent(ctx context.Context, module content.Module, id string, expected int64, input content.WriteInput, actor string, now time.Time) (content.Item, error) {
	return r.updateContent(ctx, module, id, expected, input, actor, now, false)
}

func (r *Repository) RestoreContent(ctx context.Context, module content.Module, id string, expected int64, input content.WriteInput, actor string, now time.Time) (content.Item, error) {
	return r.updateContent(ctx, module, id, expected, input, actor, now, true)
}

func (r *Repository) updateContent(ctx context.Context, module content.Module, id string, expected int64, input content.WriteInput, actor string, now time.Time, restore bool) (content.Item, error) {
	if err := r.validateContentAudit(ctx, actor); err != nil {
		return content.Item{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return content.Item{}, err
	}
	defer tx.Rollback()
	if err := lockContentVersion(ctx, tx, module, id, expected); err != nil {
		return content.Item{}, err
	}
	if restore && module == content.ModulePages {
		err = writeRestoredPage(ctx, tx, id, input)
	} else {
		err = writeTypedContent(ctx, tx, module, id, input, false)
	}
	if err != nil {
		return content.Item{}, mapContentConflict(err)
	}
	if module == content.ModuleNews {
		if err := prepareNewsSummaries(ctx, tx, id, expected+1, input.Translations, actor, now, true); err != nil {
			return content.Item{}, err
		}
	}
	if err := replaceTranslations(ctx, tx, id, input.Translations); err != nil {
		return content.Item{}, err
	}
	if module == content.ModuleNews && input.Kind == content.KindStatement {
		if err := replaceContentAssetRefs(ctx, tx, id, input.Translations); err != nil {
			return content.Item{}, err
		}
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
	if err := touchPageGroup(ctx, tx, module, actor, now); err != nil {
		return content.Item{}, err
	}
	action := "cms.content.update"
	if restore {
		action = "cms.content.restore"
	}
	if err := r.enqueueContentAudit(ctx, tx, action, item.ID, module, actor, now); err != nil {
		return content.Item{}, err
	}
	return item, tx.Commit()
}

func writeRestoredPage(ctx context.Context, tx *sql.Tx, id string, input content.WriteInput) error {
	homeSettingsJSON, err := encodeHomeSettings(input)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE hhc_web.page_item SET page_template=$3,indexable=$5,banner_asset_id=NULLIF($6,''),home_settings=$7 WHERE content_id=$1 AND page_key=$2 AND route_path=$4`, id, input.PageKey, input.PageTemplate, input.RoutePath, input.Indexable, input.BannerAssetID, homeSettingsJSON)
	if err != nil {
		return err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return content.ErrInvalid
	}
	return nil
}

func (r *Repository) PublishContent(ctx context.Context, module content.Module, id string, expected int64, actor string, now time.Time) (content.Item, error) {
	if err := r.validateContentAudit(ctx, actor); err != nil {
		return content.Item{}, err
	}
	now = now.Truncate(time.Microsecond)
	if module == content.ModuleNews {
		return r.startNewsPublish(ctx, id, expected, actor, now, false, nil)
	}
	if module == content.ModulePages {
		current, err := r.GetContent(ctx, module, id)
		if err != nil {
			return content.Item{}, err
		}
		if current.PageKey == "about" || (current.PageKey == "home" && current.PageTemplate != "home.v2") {
			return r.publishPageGroup(ctx, id, expected, actor, now)
		}
		if current.PageKey == "home" && current.PageTemplate == "home.v2" {
			return r.startHomePublish(ctx, id, expected, actor, now)
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return content.Item{}, err
	}
	defer tx.Rollback()
	if err := lockContentVersion(ctx, tx, module, id, expected); err != nil {
		return content.Item{}, err
	}
	current, err := loadContent(ctx, tx, module, id)
	if err != nil {
		return content.Item{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='published',version=version+1,first_published_at=COALESCE(first_published_at,$2),published_at=$2,updated_by=$3,updated_at=$2 WHERE id=$1`, id, now, actor); err != nil {
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
		var projection any = public
		routePath := public.Href
		if module == content.ModuleLocations {
			projection = publicLocation(item, translation)
			routePath = "/locations"
		} else if module == content.ModulePages {
			availableLocales := make([]string, 0, len(item.Translations))
			for _, candidate := range item.Translations {
				availableLocales = append(availableLocales, candidate.Locale)
			}
			sortPublicLocales(availableLocales)
			projection = content.PublicEditorialPage{PageKey: item.PageKey, Template: item.PageTemplate, RoutePath: item.RoutePath, Indexable: item.Indexable, Content: translation.BodyJSON, ResolvedLocale: translation.Locale, AvailableLocales: availableLocales, Version: item.Version, PublishedAt: *item.PublishedAt}
			routePath = item.RoutePath
		}
		payload, _ := json.Marshal(projection)
		etag := fmt.Sprintf(`%x`, sha256.Sum256(payload))
		key := fmt.Sprintf("%s:%s:%s", module, translation.Locale, id)
		if module == content.ModulePages {
			key = fmt.Sprintf("page:%s:%s", translation.Locale, item.PageKey)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.public_projection(projection_key,resource_type,resource_id,locale,route_path,version,etag,payload_json,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(projection_key) DO UPDATE SET route_path=EXCLUDED.route_path,version=EXCLUDED.version,etag=EXCLUDED.etag,payload_json=EXCLUDED.payload_json,updated_at=EXCLUDED.updated_at`, key, module, id, translation.Locale, routePath, item.Version, etag, payload, now); err != nil {
			return content.Item{}, err
		}
	}
	if module == content.ModulePages && item.PageKey == "home" && current.BannerPublicGrantID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.page_item SET published_banner_asset_id=NULL,banner_public_grant_id=NULL,published_banner_version=NULL WHERE content_id=$1`, id); err != nil {
			return content.Item{}, err
		}
		if err := enqueueGrantRevoke(ctx, tx, "home", id, item.Version, actor, publication.PublishedAsset{Usage: "banner", AssetID: current.PublishedBannerAssetID, GrantID: current.BannerPublicGrantID}, now); err != nil {
			return content.Item{}, err
		}
		item.PublishedBannerAssetID, item.BannerPublicGrantID, item.PublishedBannerVersion = "", "", 0
	}
	if err := insertRevision(ctx, tx, item, actor, now); err != nil {
		return content.Item{}, err
	}
	if err := r.enqueueContentAudit(ctx, tx, "cms.content.publish", item.ID, module, actor, now); err != nil {
		return content.Item{}, err
	}
	return item, tx.Commit()
}

func (r *Repository) startHomePublish(ctx context.Context, id string, expected int64, actor string, now time.Time) (content.Item, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return content.Item{}, err
	}
	defer tx.Rollback()
	if err := lockContentVersion(ctx, tx, content.ModulePages, id, expected); err != nil {
		return content.Item{}, err
	}
	current, err := loadContent(ctx, tx, content.ModulePages, id)
	if err != nil {
		return content.Item{}, err
	}
	if current.PageKey != "home" || current.PageTemplate != "home.v2" || current.BannerAssetID == "" || requireFiveLocales(current) != nil {
		return content.Item{}, content.ErrNotPublishable
	}
	if err := capturePublishedBaseline(ctx, tx, current, actor, now); err != nil {
		return content.Item{}, err
	}
	next := expected + 1
	manifest, err := collectGroupManifest(ctx, tx, current, next)
	if err != nil {
		return content.Item{}, err
	}
	if err := insertGroupManifest(ctx, tx, manifest, "pending", actor, now); err != nil {
		return content.Item{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='publishing',version=$2,updated_by=$3,updated_at=$4 WHERE id=$1`, id, next, actor, now); err != nil {
		return content.Item{}, err
	}
	payload, _ := json.Marshal(publication.ContentPublishPayload{ActorID: actor, ContentID: id, AssetID: current.BannerAssetID, ManifestSHA256: manifest.SHA256, AggregateVersion: next})
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at)
		VALUES($1,'asset-api','home.publish.ensure_asset','home',$2,$3,$4,$5,'pending',$6,$6,$6)`,
		platform.NewID(), id, next, payload, fmt.Sprintf("home:%s:publish:v%d", id, next), now); err != nil {
		return content.Item{}, err
	}
	item, err := loadContent(ctx, tx, content.ModulePages, id)
	if err != nil {
		return content.Item{}, err
	}
	if err := r.enqueueContentAudit(ctx, tx, "cms.content.publish", item.ID, content.ModulePages, actor, now); err != nil {
		return content.Item{}, err
	}
	return item, tx.Commit()
}

func (r *Repository) UnpublishContent(ctx context.Context, module content.Module, id string, expected int64, actor string, now time.Time) (content.Item, error) {
	if err := r.validateContentAudit(ctx, actor); err != nil {
		return content.Item{}, err
	}
	if module == content.ModuleNews {
		return r.startNewsUnpublish(ctx, id, expected, actor, now)
	}
	if module == content.ModulePages {
		current, err := r.GetContent(ctx, module, id)
		if err != nil {
			return content.Item{}, err
		}
		if current.PageKey == "about" {
			return r.unpublishAboutGroup(ctx, id, expected, actor, now)
		}
		if current.PageKey == "home" {
			return r.unpublishHomeGroup(ctx, id, expected, actor, now)
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return content.Item{}, err
	}
	defer tx.Rollback()
	if err := lockContentVersion(ctx, tx, module, id, expected); err != nil {
		return content.Item{}, err
	}
	current, err := loadContent(ctx, tx, module, id)
	if err != nil {
		return content.Item{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='unpublished',version=version+1,updated_by=$2,updated_at=$3 WHERE id=$1`, id, actor, now); err != nil {
		return content.Item{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.public_projection WHERE resource_type=$1 AND resource_id=$2`, module, id); err != nil {
		return content.Item{}, err
	}
	if module == content.ModulePages && current.PageKey == "home" && current.BannerPublicGrantID != "" {
		if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.page_item SET published_banner_asset_id=NULL,banner_public_grant_id=NULL,published_banner_version=NULL WHERE content_id=$1`, id); err != nil {
			return content.Item{}, err
		}
		if err := enqueueGrantRevoke(ctx, tx, "home", id, expected+1, actor, publication.PublishedAsset{Usage: "banner", AssetID: current.PublishedBannerAssetID, GrantID: current.BannerPublicGrantID}, now); err != nil {
			return content.Item{}, err
		}
	}
	item, err := loadContent(ctx, tx, module, id)
	if err != nil {
		return content.Item{}, err
	}
	if err := insertRevision(ctx, tx, item, actor, now); err != nil {
		return content.Item{}, err
	}
	if err := r.enqueueContentAudit(ctx, tx, "cms.content.unpublish", item.ID, module, actor, now); err != nil {
		return content.Item{}, err
	}
	return item, tx.Commit()
}

func (r *Repository) startNewsPublish(ctx context.Context, id string, expected int64, actor string, now time.Time, endPopup bool, notification *content.StatementNotificationRequest) (content.Item, error) {
	if err := r.validateContentAudit(ctx, actor); err != nil {
		return content.Item{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return content.Item{}, err
	}
	defer tx.Rollback()
	// ponytail: serialize news publication; use narrower locks if publication throughput requires it.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('hhc.statement.popup'))`); err != nil {
		return content.Item{}, err
	}
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

	if endPopup {
		if current.Kind != content.KindStatement || !current.IsPublished {
			return content.Item{}, content.ErrNotPublishable
		}
		if current.Status != content.StatusPublished || current.Version != current.PublishedVersion {
			return content.Item{}, content.ErrDraftChangesPresent
		}
		if current.PopupStartsAt == nil || current.PopupEndsAt == nil || !now.Before(*current.PopupEndsAt) {
			return content.Item{}, content.ErrNotPublishable
		}
		if !now.After(*current.PopupStartsAt) {
			current.PopupStartsAt, current.PopupEndsAt = nil, nil
		} else {
			current.PopupEndsAt = &now
		}
		if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.news_item SET popup_starts_at=$2,popup_ends_at=$3 WHERE entry_id=$1`, id, current.PopupStartsAt, current.PopupEndsAt); err != nil {
			return content.Item{}, err
		}
	}
	if err := prepareNewsSummaries(ctx, tx, id, expected, current.Translations, actor, now, false); err != nil {
		return content.Item{}, err
	}
	var summariesReady bool
	if err := tx.QueryRowContext(ctx, `SELECT NOT EXISTS(SELECT 1 FROM hhc_web.content_translation t LEFT JOIN hhc_web.news_summary_state s ON s.entry_id=t.entry_id AND s.locale=t.locale WHERE t.entry_id=$1 AND (s.status IS DISTINCT FROM 'ready' OR s.source_hash IS NULL))`, id).Scan(&summariesReady); err != nil {
		return content.Item{}, err
	}
	if !summariesReady && current.Kind != content.KindStatement {
		if err := tx.Commit(); err != nil {
			return content.Item{}, err
		}
		return content.Item{}, content.ErrSummaryProcessing
	}
	if err := checkStatementWindow(ctx, tx, current); err != nil {
		return content.Item{}, err
	}
	next := expected + 1
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='publishing',version=$2,updated_by=$3,updated_at=$4 WHERE id=$1`, id, next, actor, now); err != nil {
		return content.Item{}, err
	}
	inlineAssetIDs, err := loadCurrentInlineAssetIDs(ctx, tx, id)
	if err != nil {
		return content.Item{}, err
	}
	payload, _ := json.Marshal(publication.ContentPublishPayload{StatementNotification: notification, ActorID: actor, ContentID: id, AssetID: current.CoverAssetID, HomeAssetID: current.HomeCoverAssetID, InlineAssetIDs: inlineAssetIDs, AggregateVersion: next})
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
	action := "cms.content.publish"
	if endPopup {
		action = "cms.content.update"
	}
	if err := r.enqueueContentAudit(ctx, tx, action, item.ID, content.ModuleNews, actor, now); err != nil {
		return content.Item{}, err
	}
	return item, tx.Commit()
}

func (r *Repository) startNewsUnpublish(ctx context.Context, id string, expected int64, actor string, now time.Time) (content.Item, error) {
	if err := r.validateContentAudit(ctx, actor); err != nil {
		return content.Item{}, err
	}
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
	inlineAssets, err := loadPublishedInlineAssets(ctx, tx, id)
	if err != nil {
		return content.Item{}, err
	}
	assets = append(assets, inlineAssets...)
	payload, _ := json.Marshal(publication.ContentUnpublishPayload{ActorID: actor, ContentID: id, Assets: assets, AggregateVersion: next})
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
	if err := r.enqueueContentAudit(ctx, tx, "cms.content.unpublish", item.ID, content.ModuleNews, actor, now); err != nil {
		return content.Item{}, err
	}
	return item, tx.Commit()
}

func (r *Repository) CompleteContentPublish(ctx context.Context, event publication.Event, assets []publication.PublishedAsset, now time.Time) error {
	if event.EventType == "home.publish.ensure_asset" {
		return r.completeHomePublish(ctx, event, assets, now)
	}
	now = now.Truncate(time.Microsecond)
	var payload publication.ContentPublishPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return err
	}
	actor := payload.ActorID
	if actor == "" {
		actor = event.ActorID
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// ponytail: serialize news publication; use narrower locks if publication throughput requires it.
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext('hhc.statement.popup'))`); err != nil {
		return err
	}
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
	if err := checkStatementWindow(ctx, tx, item); err != nil {
		return err
	}
	item.Status = content.StatusPublished
	if item.FirstPublishedAt == nil {
		first := now
		item.FirstPublishedAt = &first
	}
	item.PublishedAt = &now
	detail, home := publishedAsset(assets, "detail"), publishedAsset(assets, "home")
	if detail.AssetID != payload.AssetID || home.AssetID != payload.HomeAssetID {
		return fmt.Errorf("published asset set does not match event payload")
	}
	inlineURLs := map[string]string{}
	inlinePublished := map[string]publication.PublishedAsset{}
	for _, asset := range assets {
		if asset.Usage == "inline" {
			inlineURLs[asset.AssetID] = asset.PublicURL
			inlinePublished[asset.AssetID] = asset
		}
	}
	if !sameAssetSet(payload.InlineAssetIDs, inlineURLs) {
		return fmt.Errorf("published inline asset set does not match event payload")
	}
	item.CoverURL = detail.PublicURL
	item.HomeCoverURL = home.PublicURL
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.public_projection WHERE resource_type='news' AND resource_id=$1`, item.ID); err != nil {
		return err
	}
	for _, translation := range item.Translations {
		public := publicContent(item, translation)
		if item.Kind == content.KindStatement && len(translation.BodyJSON) != 0 {
			public.BodyJSON, err = content.BuildPublicStatementDocument(translation.BodyJSON, inlineURLs)
			if err != nil {
				return err
			}
		}
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
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='published',first_published_at=COALESCE(first_published_at,$2),published_at=$2,updated_at=$2 WHERE id=$1`, item.ID, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.news_item SET public_grant_id=$2,published_cover_asset_id=$3,home_public_grant_id=$4,published_home_cover_asset_id=$5,published_version=$6 WHERE entry_id=$1`, item.ID, detail.GrantID, detail.AssetID, home.GrantID, home.AssetID, version); err != nil {
		return err
	}
	oldInlineAssets, err := loadPublishedInlineAssets(ctx, tx, item.ID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.content_published_asset WHERE entry_id=$1`, item.ID); err != nil {
		return err
	}
	for _, asset := range assets {
		if asset.Usage != "inline" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.content_published_asset(entry_id,asset_id,grant_id,public_url,published_version) VALUES($1,$2,$3,$4,$5)`, item.ID, asset.AssetID, asset.GrantID, asset.PublicURL, version); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='delivered',claimed_until=NULL,last_error='',updated_at=$2 WHERE id=$1`, event.ID, now); err != nil {
		return err
	}
	if payload.StatementNotification != nil {
		href := "/zh-Hant/news/" + item.Slug
		if item.Kind == content.KindStatement {
			href = "/zh-Hant/statements/" + item.Slug
		}
		if err := enqueueNotificationSubmission(ctx, tx, item.ID, version, payload.ActorID, *payload.StatementNotification, href, now); err != nil {
			return err
		}
	}
	for _, old := range []publication.PublishedAsset{{Usage: "detail", AssetID: oldAssetID, GrantID: oldGrantID}, {Usage: "home", AssetID: oldHomeAssetID, GrantID: oldHomeGrantID}} {
		current := publishedAsset(assets, old.Usage)
		if old.GrantID != "" && old.GrantID != current.GrantID {
			if err := enqueueGrantRevoke(ctx, tx, "news", item.ID, version, actor, old, now); err != nil {
				return err
			}
		}
	}
	for _, old := range oldInlineAssets {
		if current, ok := inlinePublished[old.AssetID]; old.GrantID != "" && (!ok || current.GrantID != old.GrantID) {
			if err := enqueueGrantRevoke(ctx, tx, "news", item.ID, version, actor, old, now); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

func (r *Repository) completeHomePublish(ctx context.Context, event publication.Event, assets []publication.PublishedAsset, now time.Time) error {
	now = now.Truncate(time.Microsecond)
	var eventPayload publication.ContentPublishPayload
	if err := json.Unmarshal(event.Payload, &eventPayload); err != nil {
		return err
	}
	actor := eventPayload.ActorID
	if actor == "" {
		actor = event.ActorID
	}
	banner := publishedAsset(assets, "banner")
	if len(assets) != 1 || banner.AssetID != eventPayload.AssetID || banner.GrantID == "" || banner.PublicURL == "" {
		return fmt.Errorf("published Home Banner does not match event payload")
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
	var status, template, oldAssetID, oldGrantID string
	var version int64
	if err := tx.QueryRowContext(ctx, `
		SELECT e.status,e.version,p.page_template,COALESCE(p.published_banner_asset_id,''),COALESCE(p.banner_public_grant_id,'')
		FROM hhc_web.content_entry e
		JOIN hhc_web.page_item p ON p.content_id=e.id
		WHERE e.id=$1 AND e.module='pages' AND p.page_key='home'
		FOR UPDATE OF e,p`, eventPayload.ContentID).Scan(&status, &version, &template, &oldAssetID, &oldGrantID); err != nil {
		return err
	}
	if status != content.StatusPublishing || version != eventPayload.AggregateVersion || template != "home.v2" {
		return publication.ErrStalePublication
	}
	manifest, err := loadPendingGroupManifest(ctx, tx, eventPayload.ContentID, version, eventPayload.ManifestSHA256)
	if err != nil {
		return err
	}
	item, err := loadContent(ctx, tx, content.ModulePages, eventPayload.ContentID)
	if err != nil {
		return err
	}
	if item.BannerAssetID != eventPayload.AssetID {
		return publication.ErrStalePublication
	}
	children, err := validateGroupManifestChildren(ctx, tx, manifest)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.public_projection WHERE (resource_type='pages' AND resource_id=$1) OR resource_type='videos'`, item.ID); err != nil {
		return err
	}
	availableLocales := make([]string, 0, len(item.Translations))
	for _, translation := range item.Translations {
		availableLocales = append(availableLocales, translation.Locale)
	}
	sortPublicLocales(availableLocales)
	for _, translation := range item.Translations {
		localized, err := content.BuildPublicHomeV2Payload(translation.BodyJSON, item.Links, item.Locations, translation.Locale, banner.PublicURL)
		if err != nil {
			return err
		}
		projection := content.PublicEditorialPage{PageKey: item.PageKey, Template: item.PageTemplate, RoutePath: item.RoutePath, Indexable: item.Indexable, Content: localized, ResolvedLocale: translation.Locale, AvailableLocales: availableLocales, Version: version, PublishedAt: now}
		encoded, err := json.Marshal(projection)
		if err != nil {
			return err
		}
		etag := fmt.Sprintf(`%x`, sha256.Sum256(encoded))
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO hhc_web.public_projection(projection_key,resource_type,resource_id,locale,route_path,version,etag,payload_json,updated_at)
			VALUES($1,'pages',$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT(projection_key) DO UPDATE SET route_path=EXCLUDED.route_path,version=EXCLUDED.version,etag=EXCLUDED.etag,payload_json=EXCLUDED.payload_json,updated_at=EXCLUDED.updated_at`,
			fmt.Sprintf("page:%s:home", translation.Locale), item.ID, translation.Locale, item.RoutePath, version, etag, encoded, now); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='published',first_published_at=COALESCE(first_published_at,$2),published_at=$2,updated_at=$2 WHERE id=$1`, item.ID, now); err != nil {
		return err
	}
	for _, member := range manifest.Items {
		child := children[member.ID]
		switch member.Action {
		case content.GroupActionKeep:
			if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET first_published_at=COALESCE(first_published_at,$2),published_at=$2,updated_at=$2 WHERE id=$1 AND version=$3`, child.ID, now, member.SourceVersion); err != nil {
				return err
			}
		case content.GroupActionPublish:
			if err := updateGroupChildStatus(ctx, tx, child.ID, member.SourceVersion, member.TargetVersion, content.StatusPublished, item.UpdatedBy, now); err != nil {
				return publication.ErrStalePublication
			}
		case content.GroupActionRemove:
			if err := updateGroupChildStatus(ctx, tx, child.ID, member.SourceVersion, member.TargetVersion, content.StatusUnpublished, item.UpdatedBy, now); err != nil {
				return publication.ErrStalePublication
			}
			continue
		}
		child, err = loadContent(ctx, tx, content.ModuleVideos, child.ID)
		if err != nil {
			return err
		}
		for _, translation := range child.Translations {
			if err := writeGroupProjection(ctx, tx, child, translation, now); err != nil {
				return err
			}
		}
		if member.Action == content.GroupActionPublish {
			if err := insertRevision(ctx, tx, child, item.UpdatedBy, now); err != nil {
				return err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.page_item SET published_banner_asset_id=$2,banner_public_grant_id=$3,published_banner_version=$4 WHERE content_id=$1`, item.ID, banner.AssetID, banner.GrantID, version); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.page_publication_manifest SET publication_status='published' WHERE page_id=$1 AND page_version=$2 AND publication_status='pending'`, item.ID, version); err != nil {
		return err
	}
	publishedPage, err := loadContent(ctx, tx, content.ModulePages, item.ID)
	if err != nil {
		return err
	}
	if err := insertRevision(ctx, tx, publishedPage, item.UpdatedBy, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.outbox_event SET status='delivered',claimed_until=NULL,last_error='',updated_at=$2 WHERE id=$1`, event.ID, now); err != nil {
		return err
	}
	if oldGrantID != "" && oldGrantID != banner.GrantID {
		if err := enqueueGrantRevoke(ctx, tx, "home", item.ID, version, actor, publication.PublishedAsset{Usage: "banner", AssetID: oldAssetID, GrantID: oldGrantID}, now); err != nil {
			return err
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.content_published_asset WHERE entry_id=$1`, payload.ContentID); err != nil {
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

func loadCurrentInlineAssetIDs(ctx context.Context, query contentQueryer, id string) ([]string, error) {
	rows, err := query.QueryContext(ctx, `SELECT DISTINCT asset_id FROM hhc_web.content_asset_ref WHERE entry_id=$1 AND purpose='statement_inline' ORDER BY asset_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []string{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func loadPublishedInlineAssets(ctx context.Context, query contentQueryer, id string) ([]publication.PublishedAsset, error) {
	rows, err := query.QueryContext(ctx, `SELECT asset_id,grant_id,public_url FROM hhc_web.content_published_asset WHERE entry_id=$1 ORDER BY asset_id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []publication.PublishedAsset{}
	for rows.Next() {
		var value publication.PublishedAsset
		value.Usage = "inline"
		if err := rows.Scan(&value.AssetID, &value.GrantID, &value.PublicURL); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func sameAssetSet(expected []string, actual map[string]string) bool {
	if len(expected) != len(actual) {
		return false
	}
	for _, assetID := range expected {
		if actual[assetID] == "" {
			return false
		}
	}
	return true
}

func enqueueGrantRevoke(ctx context.Context, tx *sql.Tx, aggregateType, contentID string, version int64, actor string, asset publication.PublishedAsset, now time.Time) error {
	payload, _ := json.Marshal(publication.ContentUnpublishPayload{ActorID: actor, ContentID: contentID, AssetID: asset.AssetID, GrantID: asset.GrantID, AggregateVersion: version})
	_, err := tx.ExecContext(ctx, `
		INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at)
		VALUES($1,'asset-api','asset.grant.revoke',$2,$3,$4,$5,$6,'pending',$7,$7,$7)
		ON CONFLICT(destination,idempotency_key) DO NOTHING`,
		platform.NewID(), aggregateType, contentID, version, payload, fmt.Sprintf("%s:%s:revoke:%s", aggregateType, contentID, asset.GrantID), now)
	return err
}

func (r *Repository) ContentRevisions(ctx context.Context, module content.Module, id string) ([]content.Revision, error) {
	item, err := loadContent(ctx, r.db, module, id)
	if err != nil {
		return nil, err
	}
	if module == content.ModulePages && (item.PageKey == "home" || item.PageKey == "about") {
		return r.pageGroupRevisions(ctx, id)
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
	if err := r.validateContentAudit(ctx, actor); err != nil {
		return err
	}
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
	if status == content.StatusPublishing || status == content.StatusUnpublishing {
		return content.ErrConflict
	}
	hasPublicState, err := hasPublicContentState(ctx, tx, module, id)
	if err != nil {
		return err
	}
	hasManifestState, err := hasPageGroupManifestState(ctx, tx, module, id)
	if err != nil {
		return err
	}
	if _, grouped := content.PageGroupForChild(module); grouped && (hasPublicState || hasManifestState) {
		if err := touchPageGroup(ctx, tx, module, actor, now); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='pending_removal',version=version+1,updated_by=$2,updated_at=$3 WHERE id=$1`, id, actor, now); err != nil {
			return err
		}
		item, err := loadContent(ctx, tx, module, id)
		if err != nil {
			return err
		}
		if err := insertRevision(ctx, tx, item, actor, now); err != nil {
			return err
		}
		if err := r.enqueueContentAudit(ctx, tx, "cms.content.delete", item.ID, module, actor, now); err != nil {
			return err
		}
		return tx.Commit()
	}
	if status == content.StatusPublished || status == content.StatusUnpublishFailed {
		return content.ErrConflict
	}
	if hasPublicState {
		return content.ErrConflict
	}
	if err := touchPageGroup(ctx, tx, module, actor, now); err != nil {
		return err
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
	if err := r.enqueueContentAudit(ctx, tx, "cms.content.delete", id, module, actor, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) validateContentAudit(ctx context.Context, actor string) error {
	if r.auditOutbox == nil {
		return nil
	}
	provenance, ok := auditclient.ProvenanceFromContext(ctx)
	if !ok || provenance.ActorID != actor || !auditclient.ValidActorRequest(provenance.ActorID, provenance.RequestID) {
		return content.ErrAuditUnavailable
	}
	return nil
}

func (r *Repository) enqueueContentAudit(ctx context.Context, tx *sql.Tx, action, resourceID string, module content.Module, actor string, now time.Time) error {
	if r.auditOutbox == nil {
		return nil
	}
	provenance, ok := auditclient.ProvenanceFromContext(ctx)
	if !ok || provenance.ActorID != actor {
		return content.ErrAuditUnavailable
	}
	group := "pages"
	if module == content.ModuleNews {
		group = "news"
	}
	event, err := auditclient.NewEvent(action, resourceID, actor, provenance.RequestID, now, auditclient.Metadata{"module": group})
	if err == nil {
		_, err = r.auditOutbox.EnqueueTx(ctx, tx, event, now)
	}
	if err != nil {
		return content.ErrAuditUnavailable
	}
	return nil
}

func touchPageGroup(ctx context.Context, tx *sql.Tx, module content.Module, actor string, now time.Time) error {
	pageKey, ok := content.PageGroupForChild(module)
	if !ok {
		return nil
	}
	var id, status string
	if err := tx.QueryRowContext(ctx, `
		SELECT e.id,e.status
		FROM hhc_web.content_entry e
		JOIN hhc_web.page_item p ON p.content_id=e.id
		WHERE e.module='pages' AND p.page_key=$1
		FOR UPDATE OF e`, pageKey).Scan(&id, &status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return content.ErrNotFound
		}
		return err
	}
	if status == content.StatusPublishing || status == content.StatusUnpublishing {
		return content.ErrConflict
	}
	_, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='draft',version=version+1,updated_by=$2,updated_at=$3 WHERE id=$1`, id, actor, now)
	return err
}

func hasPageGroupManifestState(ctx context.Context, tx *sql.Tx, module content.Module, id string) (bool, error) {
	if _, ok := content.PageGroupForChild(module); !ok {
		return false, nil
	}
	var exists bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS(
			SELECT 1
			FROM hhc_web.page_publication_manifest
			WHERE manifest_json->'items' @> jsonb_build_array(jsonb_build_object('id',$1::text))
		)`, id).Scan(&exists)
	return exists, err
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
			UNION
			SELECT asset_id FROM hhc_web.content_asset_ref WHERE entry_id=$1
		) assets WHERE COALESCE(asset_id,'')<>'' ORDER BY asset_id`, id)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			rows.Close()
			return nil, err
		}
		seen[value] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	revisions, err := tx.QueryContext(ctx, `SELECT snapshot_json FROM hhc_web.content_revision WHERE entry_id=$1`, id)
	if err != nil {
		return nil, err
	}
	defer revisions.Close()
	for revisions.Next() {
		var raw []byte
		var snapshot struct {
			Kind         string                `json:"kind"`
			Translations []content.Translation `json:"translations"`
		}
		if err := revisions.Scan(&raw); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &snapshot); err != nil {
			return nil, err
		}
		if snapshot.Kind != content.KindStatement {
			continue
		}
		references, err := content.StatementImageRefs(snapshot.Translations)
		if err != nil {
			return nil, err
		}
		for _, reference := range references {
			seen[reference.AssetID] = true
		}
	}
	if err := revisions.Err(); err != nil {
		return nil, err
	}
	values := make([]string, 0, len(seen))
	for assetID := range seen {
		values = append(values, assetID)
	}
	sort.Strings(values)
	return values, nil
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
		payloadValue := map[string]any{"assetId": assetID}
		if p, ok := auditclient.ProvenanceFromContext(ctx); ok && p.ActorType == "user" && auditclient.ValidActorRequest(p.ActorID, p.RequestID) {
			payloadValue["actorId"], payloadValue["requestId"] = p.ActorID, p.RequestID
		}
		payload, _ := json.Marshal(payloadValue)
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
	if err := r.db.QueryRowContext(ctx, `SELECT count(DISTINCT resource_id) FROM hhc_web.public_projection WHERE resource_type=$1 AND locale IN ($2,'zh-Hant') AND ($1 <> 'news' OR COALESCE(payload_json->>'kind','general')='general')`, module, locale).Scan(&total); err != nil {
		return content.PublicPage{}, err
	}
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`
		WITH localized AS (
			SELECT DISTINCT ON (resource_id) resource_id,locale,payload_json,updated_at
			FROM hhc_web.public_projection
			WHERE resource_type=$1 AND locale IN ($2,'zh-Hant') AND ($1 <> 'news' OR COALESCE(payload_json->>'kind','general')='general')
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
	var publishedVersion int64
	err := r.db.QueryRowContext(ctx, `
		WITH selected AS (
			SELECT resource_id,locale,payload_json,etag,version
			FROM hhc_web.public_projection
			WHERE resource_type='news'
				AND locale IN ($1,'zh-Hant')
				AND route_path IN ('/' || $1 || '/news/' || $2,'/zh-Hant/news/' || $2,'/' || $1 || '/statements/' || $2,'/zh-Hant/statements/' || $2)
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
		SELECT selected.resource_id,selected.locale,selected.payload_json,selected.etag,availability.locales,selected.version
		FROM selected
		JOIN availability ON availability.available_resource_id=selected.resource_id`, locale, slug).
		Scan(&id, &resolvedLocale, &payload, &etag, &availableLocales, &publishedVersion)
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
	item.PublishedVersion = publishedVersion
	if item.Kind == content.KindStatement && (publishedVersion < 1 || publishedVersion > 9007199254740991) {
		return content.PublicItem{}, "", content.ErrInvalid
	}
	if err := enrichPublicItem(&item, id, locale, resolvedLocale, availableLocales); err != nil {
		return content.PublicItem{}, "", err
	}
	return item, publicResponseETag(etag, locale, item), nil
}

func (r *Repository) PublicLocations(ctx context.Context, locale string) ([]content.PublicLocation, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT projection.payload_json,availability.locales
		FROM hhc_web.public_projection projection
		JOIN LATERAL (
			SELECT jsonb_agg(sibling.locale ORDER BY sibling.locale) AS locales
			FROM hhc_web.public_projection sibling
			WHERE sibling.resource_type='locations' AND sibling.resource_id=projection.resource_id
		) availability ON true
		WHERE projection.resource_type='locations' AND projection.locale=$1
		ORDER BY (projection.payload_json->>'sortOrder')::integer ASC,projection.payload_json->>'id' ASC`, locale)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []content.PublicLocation{}
	for rows.Next() {
		var value content.PublicLocation
		var payload, availableLocales []byte
		if err := rows.Scan(&payload, &availableLocales); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(availableLocales, &value.AvailableLocales); err != nil {
			return nil, err
		}
		sortPublicLocales(value.AvailableLocales)
		value.ResolvedLocale = locale
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *Repository) PublicEditorialPage(ctx context.Context, key, locale string) (content.PublicEditorialPage, string, error) {
	var payload []byte
	var etag string
	err := r.db.QueryRowContext(ctx, `SELECT payload_json,etag FROM hhc_web.public_projection WHERE projection_key=$1 AND resource_type='pages' AND locale=$2`, "page:"+locale+":"+key, locale).Scan(&payload, &etag)
	if errors.Is(err, sql.ErrNoRows) {
		return content.PublicEditorialPage{}, "", content.ErrNotFound
	}
	if err != nil {
		return content.PublicEditorialPage{}, "", err
	}
	var page content.PublicEditorialPage
	if err := json.Unmarshal(payload, &page); err != nil {
		return content.PublicEditorialPage{}, "", err
	}
	return page, etag, nil
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
	value.RequestedLocale = requestedLocale
	if value.Kind == "" {
		value.Kind = content.KindGeneral
	}
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
	if len(id) != 36 {
		return content.Item{}, content.ErrNotFound
	}
	if _, err := uuid.Parse(id); err != nil {
		return content.Item{}, content.ErrNotFound
	}
	var item content.Item
	err := query.QueryRowContext(ctx, `SELECT id::text,module,status,version,created_by,updated_by,first_published_at,published_at,created_at,updated_at FROM hhc_web.content_entry WHERE id=$1 AND module=$2`, id, module).Scan(&item.ID, &item.Module, &item.Status, &item.Version, &item.CreatedBy, &item.UpdatedBy, &item.FirstPublishedAt, &item.PublishedAt, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return content.Item{}, content.ErrNotFound
	}
	if err != nil {
		return content.Item{}, err
	}
	switch module {
	case content.ModuleNews:
		var publishedVersion sql.NullInt64
		err = query.QueryRowContext(ctx, `SELECT slug,display_date::text,cover_asset_id,home_cover_asset_id,detail_layout,featured,author_name,kind,popup_starts_at,popup_ends_at,public_grant_id,home_public_grant_id,published_cover_asset_id,published_home_cover_asset_id,published_version FROM hhc_web.news_item WHERE entry_id=$1`, id).
			Scan(&item.Slug, &item.DisplayDate, &item.CoverAssetID, &item.HomeCoverAssetID, &item.DetailLayout, &item.Featured, &item.AuthorName, &item.Kind, &item.PopupStartsAt, &item.PopupEndsAt, &item.PublicGrantID, &item.HomePublicGrantID, &item.PublishedCoverID, &item.PublishedHomeCoverID, &publishedVersion)
		if publishedVersion.Valid {
			item.PublishedVersion = publishedVersion.Int64
		}
	case content.ModuleHistory:
		err = query.QueryRowContext(ctx, `SELECT COALESCE(event_date,'') FROM hhc_web.history_event WHERE entry_id=$1`, id).Scan(&item.EventDate)
	case content.ModuleVideos:
		err = query.QueryRowContext(ctx, `SELECT youtube_video_id,home_eligible FROM hhc_web.video_item WHERE entry_id=$1`, id).Scan(&item.YouTubeVideoID, &item.HomeEligible)
	case content.ModuleLocations:
		err = query.QueryRowContext(ctx, `SELECT stable_key,map_href,sort_order FROM hhc_web.location_item WHERE content_id=$1`, id).Scan(&item.LocationKey, &item.MapHref, &item.SortOrder)
	case content.ModulePages:
		var homeSettingsJSON []byte
		err = query.QueryRowContext(ctx, `SELECT page_key,page_template,route_path,indexable,COALESCE(banner_asset_id,''),COALESCE(published_banner_asset_id,''),COALESCE(banner_public_grant_id,''),COALESCE(published_banner_version,0),COALESCE(home_settings,'{}'::jsonb) FROM hhc_web.page_item WHERE content_id=$1`, id).
			Scan(&item.PageKey, &item.PageTemplate, &item.RoutePath, &item.Indexable, &item.BannerAssetID, &item.PublishedBannerAssetID, &item.BannerPublicGrantID, &item.PublishedBannerVersion, &homeSettingsJSON)
		if err == nil {
			err = decodeHomeSettings(homeSettingsJSON, &item)
		}
	}
	if err != nil {
		return content.Item{}, err
	}
	if err := query.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM hhc_web.public_projection WHERE resource_type=$1 AND resource_id=$2),COALESCE(MAX(version),0) FROM hhc_web.public_projection WHERE resource_type=$1 AND resource_id=$2`, module, id).
		Scan(&item.IsPublished, &item.PublishedVersion); err != nil {
		return content.Item{}, err
	}
	if module == content.ModuleNews && item.Kind == content.KindStatement {
		if err := query.QueryRowContext(ctx, `SELECT (payload_json->>'popupStartsAt')::timestamptz,(payload_json->>'popupEndsAt')::timestamptz FROM hhc_web.public_projection WHERE resource_type='news' AND resource_id=$1 AND locale='zh-Hant'`, id).Scan(&item.PublishedPopupStartsAt, &item.PublishedPopupEndsAt); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return content.Item{}, err
		}
	}
	rows, err := query.QueryContext(ctx, `SELECT locale,title,summary,body,date_label,image_alt,body_json,COALESCE(translated_from_source_hash,'') FROM hhc_web.content_translation WHERE entry_id=$1 ORDER BY locale`, id)
	if err != nil {
		return content.Item{}, err
	}
	defer rows.Close()
	item.Translations = []content.Translation{}
	for rows.Next() {
		var value content.Translation
		var bodyJSON []byte
		if err := rows.Scan(&value.Locale, &value.Title, &value.Summary, &value.Body, &value.DateLabel, &value.ImageAlt, &bodyJSON, &value.TranslatedFromSourceHash); err != nil {
			return content.Item{}, err
		}
		value.BodyJSON = bodyJSON
		item.Translations = append(item.Translations, value)
	}
	if err := rows.Err(); err != nil {
		return content.Item{}, err
	}
	if err := rows.Close(); err != nil {
		return content.Item{}, err
	}
	if module == content.ModuleNews && item.Kind != content.KindStatement {
		for i := range item.Translations {
			if err := query.QueryRowContext(ctx, `SELECT COALESCE((SELECT status FROM hhc_web.news_summary_state WHERE entry_id=$1 AND locale=$2),'pending')`, id, item.Translations[i].Locale).Scan(&item.Translations[i].SummaryStatus); err != nil {
				return content.Item{}, err
			}
		}
	}
	if item.Kind == content.KindStatement {
		for i := range item.Translations {
			item.Translations[i].SummaryStatus = "ready"
		}
	}
	return item, nil
}

func contentOrdering(module content.Module, sort, direction string) (string, string) {
	column, join := "e.updated_at", ""
	switch {
	case sort == "title":
		column = "(SELECT title FROM hhc_web.content_translation WHERE entry_id=e.id ORDER BY (locale='zh-Hant') DESC,locale LIMIT 1)"
	case sort == "languages":
		column = "(SELECT count(*) FROM hhc_web.content_translation WHERE entry_id=e.id)"
	case sort == "status":
		column = "e.status"
	case module == content.ModuleNews && sort == "displayDate":
		column, join = "n.display_date", "JOIN hhc_web.news_item n ON n.entry_id=e.id"
	case module == content.ModuleHistory && sort == "eventDate":
		column, join = "h.event_date", "JOIN hhc_web.history_event h ON h.entry_id=e.id"
	case module == content.ModuleVideos && sort == "youtubeVideoId":
		column, join = "v.youtube_video_id", "JOIN hhc_web.video_item v ON v.entry_id=e.id"
	case module == content.ModuleVideos && sort == "homeEligible":
		column, join = "v.home_eligible", "JOIN hhc_web.video_item v ON v.entry_id=e.id"
	}
	if direction != "asc" {
		direction = "desc"
	}
	nulls := ""
	if sort != "languages" && sort != "updatedAt" {
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
		if input.Kind == "" {
			input.Kind = content.KindGeneral
		}
		if verb == "INSERT" {
			_, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.news_item(entry_id,slug,display_date,cover_asset_id,home_cover_asset_id,detail_layout,featured,author_name,kind,popup_starts_at,popup_ends_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, input.Slug, input.DisplayDate, input.CoverAssetID, input.HomeCoverAssetID, input.DetailLayout, input.Featured, input.AuthorName, input.Kind, input.PopupStartsAt, input.PopupEndsAt)
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE hhc_web.news_item SET slug=$2,display_date=$3,cover_asset_id=$4,home_cover_asset_id=$5,detail_layout=$6,featured=$7,author_name=$8,kind=$9,popup_starts_at=$10,popup_ends_at=$11 WHERE entry_id=$1`, id, input.Slug, input.DisplayDate, input.CoverAssetID, input.HomeCoverAssetID, input.DetailLayout, input.Featured, input.AuthorName, input.Kind, input.PopupStartsAt, input.PopupEndsAt)
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
	case content.ModuleLocations:
		if verb == "INSERT" {
			_, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.location_item(content_id,stable_key,map_href,sort_order) VALUES($1,$2,$3,$4)`, id, input.LocationKey, input.MapHref, input.SortOrder)
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE hhc_web.location_item SET map_href=$3,sort_order=$4 WHERE content_id=$1 AND stable_key=$2`, id, input.LocationKey, input.MapHref, input.SortOrder)
		if err != nil {
			return err
		}
		if rows, _ := result.RowsAffected(); rows != 1 {
			return content.ErrInvalid
		}
		return nil
	case content.ModulePages:
		homeSettingsJSON, err := encodeHomeSettings(input)
		if err != nil {
			return err
		}
		if verb == "INSERT" {
			_, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.page_item(content_id,page_key,page_template,route_path,indexable,banner_asset_id,home_settings) VALUES($1,$2,$3,$4,$5,NULLIF($6,''),$7)`, id, input.PageKey, input.PageTemplate, input.RoutePath, input.Indexable, input.BannerAssetID, homeSettingsJSON)
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE hhc_web.page_item SET indexable=$5,banner_asset_id=NULLIF($6,''),home_settings=$7 WHERE content_id=$1 AND page_key=$2 AND page_template=$3 AND route_path=$4`, id, input.PageKey, input.PageTemplate, input.RoutePath, input.Indexable, input.BannerAssetID, homeSettingsJSON)
		if err != nil {
			return err
		}
		if rows, _ := result.RowsAffected(); rows != 1 {
			return content.ErrInvalid
		}
		return nil
	}
	return content.ErrInvalid
}

type storedHomeSettings struct {
	Links     content.HomeLinks      `json:"links"`
	Locations []content.HomeLocation `json:"locations"`
}

func encodeHomeSettings(input content.WriteInput) (any, error) {
	if input.PageTemplate != "home.v2" {
		return nil, nil
	}
	return json.Marshal(storedHomeSettings{Links: input.Links, Locations: input.Locations})
}

func decodeHomeSettings(payload []byte, item *content.Item) error {
	if len(payload) == 0 || string(payload) == "{}" {
		return nil
	}
	var settings storedHomeSettings
	if err := json.Unmarshal(payload, &settings); err != nil {
		return err
	}
	item.Links = settings.Links
	item.Locations = settings.Locations
	return nil
}

func replaceTranslations(ctx context.Context, tx *sql.Tx, id string, values []content.Translation) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.content_translation WHERE entry_id=$1`, id); err != nil {
		return err
	}
	for _, value := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.content_translation(entry_id,locale,title,summary,body,date_label,image_alt,body_json,translated_from_source_hash) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, value.Locale, value.Title, value.Summary, value.Body, value.DateLabel, value.ImageAlt, nullableJSON(value.BodyJSON), nullableText(value.TranslatedFromSourceHash)); err != nil {
			return err
		}
	}
	return nil
}

func replaceContentAssetRefs(ctx context.Context, tx *sql.Tx, id string, translations []content.Translation) error {
	references, err := content.StatementImageRefs(translations)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.content_asset_ref WHERE entry_id=$1`, id); err != nil {
		return err
	}
	for _, reference := range references {
		if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.content_asset_ref(entry_id,locale,block_id,asset_id,purpose) VALUES($1,$2,$3,$4,'statement_inline')`, id, reference.Locale, reference.BlockID, reference.AssetID); err != nil {
			return err
		}
	}
	return nil
}

func nullableJSON(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return []byte(value)
}
func nullableText(value string) any {
	if value == "" {
		return nil
	}
	return value
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
	value := content.PublicItem{PublishedVersion: item.Version, Kind: item.Kind, PopupStartsAt: item.PopupStartsAt, PopupEndsAt: item.PopupEndsAt, ID: item.ID, Title: translation.Title, Summary: translation.Summary, Body: translation.Body, DateLabel: translation.DateLabel, DisplayDate: item.DisplayDate, EventDate: item.EventDate, ImageAlt: translation.ImageAlt, YouTubeVideoID: item.YouTubeVideoID, Featured: item.Featured, HomeEligible: item.HomeEligible, DetailLayout: item.DetailLayout, AuthorName: item.AuthorName, FirstPublishedAt: item.FirstPublishedAt, LastPublishedAt: item.PublishedAt}
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
		if item.Kind == content.KindStatement {
			value.Href = "/" + translation.Locale + "/statements/" + item.Slug
		}
	case content.ModuleVideos:
		value.ImageURL = "https://i.ytimg.com/vi/" + item.YouTubeVideoID + "/hqdefault.jpg"
		value.Href = "https://www.youtube.com/watch?v=" + item.YouTubeVideoID
	}
	return value
}

func publicLocation(item content.Item, translation content.Translation) content.PublicLocation {
	return content.PublicLocation{ID: item.LocationKey, Name: translation.Title, Address: translation.Body, MapHref: item.MapHref, SortOrder: item.SortOrder}
}
