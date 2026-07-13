package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

func (r *Repository) CreateContent(ctx context.Context, module content.Module, input content.WriteInput, actor, key string, now time.Time) (content.Item, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return content.Item{}, err
	}
	defer tx.Rollback()
	id := platform.NewID()
	result, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.content_entry(id,module,status,version,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1,$2,'draft',1,$3,$4,$4,$5,$5) ON CONFLICT(idempotency_key) DO NOTHING`, id, module, key, actor, now)
	if err != nil {
		return content.Item{}, mapConflict(err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		if err := tx.QueryRowContext(ctx, `SELECT id::text,module FROM hhc_web.content_entry WHERE idempotency_key=$1`, key).Scan(&id, &module); err != nil {
			return content.Item{}, err
		}
		item, err := loadContent(ctx, tx, module, id)
		if err != nil {
			return content.Item{}, err
		}
		return item, tx.Commit()
	}
	if err := writeTypedContent(ctx, tx, module, id, input, true); err != nil {
		return content.Item{}, mapConflict(err)
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

func (r *Repository) ListContent(ctx context.Context, module content.Module, page, size int, status string) (content.Page, error) {
	where := ` WHERE e.module=$1`
	args := []any{module}
	if status != "" {
		args = append(args, status)
		where += fmt.Sprintf(" AND e.status=$%d", len(args))
	}
	var total int64
	if err := r.db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.content_entry e`+where, args...).Scan(&total); err != nil {
		return content.Page{}, err
	}
	order, join := contentOrdering(module)
	args = append(args, size, (page-1)*size)
	rows, err := r.db.QueryContext(ctx, fmt.Sprintf(`SELECT e.id::text FROM hhc_web.content_entry e %s %s ORDER BY %s LIMIT $%d OFFSET $%d`, join, where, order, len(args)-1, len(args)), args...)
	if err != nil {
		return content.Page{}, err
	}
	defer rows.Close()
	items := []content.Item{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return content.Page{}, err
		}
		item, err := loadContent(ctx, r.db, module, id)
		if err != nil {
			return content.Page{}, err
		}
		items = append(items, item)
	}
	return content.Page{Items: items, Page: page, PageSize: size, Total: total}, rows.Err()
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
		return content.Item{}, mapConflict(err)
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

func (r *Repository) PublishContent(ctx context.Context, module content.Module, id string, expected int64, actor, publicGrantID string, now time.Time) (content.Item, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return content.Item{}, err
	}
	defer tx.Rollback()
	if err := lockContentVersion(ctx, tx, module, id, expected); err != nil {
		return content.Item{}, err
	}
	if module == content.ModuleNews {
		if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.news_item SET public_grant_id=$2 WHERE entry_id=$1`, id, publicGrantID); err != nil {
			return content.Item{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='published',version=version+1,published_at=$2,updated_by=$3,updated_at=$2 WHERE id=$1`, id, now, actor); err != nil {
		return content.Item{}, err
	}
	item, err := loadContent(ctx, tx, module, id)
	if err != nil {
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
	if module == content.ModuleNews {
		if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.news_item SET public_grant_id='' WHERE entry_id=$1`, id); err != nil {
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
	return item, tx.Commit()
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

func (r *Repository) RestoreContent(ctx context.Context, module content.Module, id string, revision, expected int64, actor string, now time.Time) (content.Item, error) {
	var payload []byte
	if err := r.db.QueryRowContext(ctx, `SELECT snapshot_json FROM hhc_web.content_revision WHERE entry_id=$1 AND version=$2`, id, revision).Scan(&payload); errors.Is(err, sql.ErrNoRows) {
		return content.Item{}, content.ErrNotFound
	} else if err != nil {
		return content.Item{}, err
	}
	var snapshot content.Item
	if err := json.Unmarshal(payload, &snapshot); err != nil {
		return content.Item{}, err
	}
	input := content.WriteInput{Slug: snapshot.Slug, DisplayDate: snapshot.DisplayDate, SortOrder: snapshot.SortOrder, YouTubeVideoID: snapshot.YouTubeVideoID, CoverAssetID: snapshot.CoverAssetID, Featured: snapshot.Featured, HomeEligible: snapshot.HomeEligible, Translations: snapshot.Translations}
	return r.UpdateContent(ctx, module, id, expected, input, actor, now)
}

func (r *Repository) PublicContent(ctx context.Context, module content.Module, locale string, limit int) ([]content.PublicItem, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT payload_json FROM hhc_web.public_projection WHERE resource_type=$1 AND locale=$2`, module, locale)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []content.PublicItem{}
	for rows.Next() {
		var payload []byte
		var value content.PublicItem
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(payload, &value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	switch module {
	case content.ModuleNews:
		sort.Slice(values, func(i, j int) bool { return values[i].DisplayDate > values[j].DisplayDate })
	case content.ModuleHistory:
		sort.Slice(values, func(i, j int) bool { return values[i].SortOrder < values[j].SortOrder })
	case content.ModuleVideos:
		day := time.Now().UTC().Format("2006-01-02")
		sort.Slice(values, func(i, j int) bool {
			a := sha256.Sum256([]byte(day + locale + values[i].ID))
			b := sha256.Sum256([]byte(day + locale + values[j].ID))
			return string(a[:]) < string(b[:])
		})
	}
	if len(values) > limit {
		values = values[:limit]
	}
	return values, nil
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
		err = query.QueryRowContext(ctx, `SELECT slug,display_date::text,cover_asset_id,featured,public_grant_id FROM hhc_web.news_item WHERE entry_id=$1`, id).Scan(&item.Slug, &item.DisplayDate, &item.CoverAssetID, &item.Featured, &item.PublicGrantID)
	case content.ModuleHistory:
		err = query.QueryRowContext(ctx, `SELECT sort_order FROM hhc_web.history_event WHERE entry_id=$1`, id).Scan(&item.SortOrder)
	case content.ModuleVideos:
		err = query.QueryRowContext(ctx, `SELECT youtube_video_id,home_eligible FROM hhc_web.video_item WHERE entry_id=$1`, id).Scan(&item.YouTubeVideoID, &item.HomeEligible)
	}
	if err != nil {
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

func contentOrdering(module content.Module) (string, string) {
	switch module {
	case content.ModuleNews:
		return "n.display_date DESC", "JOIN hhc_web.news_item n ON n.entry_id=e.id"
	case content.ModuleHistory:
		return "h.sort_order", "JOIN hhc_web.history_event h ON h.entry_id=e.id"
	default:
		return "e.updated_at DESC", ""
	}
}
func writeTypedContent(ctx context.Context, tx *sql.Tx, module content.Module, id string, input content.WriteInput, insert bool) error {
	verb := "UPDATE"
	if insert {
		verb = "INSERT"
	}
	switch module {
	case content.ModuleNews:
		if verb == "INSERT" {
			_, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.news_item(entry_id,slug,display_date,cover_asset_id,featured) VALUES($1,$2,$3,$4,$5)`, id, input.Slug, input.DisplayDate, input.CoverAssetID, input.Featured)
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE hhc_web.news_item SET slug=$2,display_date=$3,cover_asset_id=$4,featured=$5 WHERE entry_id=$1`, id, input.Slug, input.DisplayDate, input.CoverAssetID, input.Featured)
		return err
	case content.ModuleHistory:
		if verb == "INSERT" {
			_, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.history_event(entry_id,sort_order) VALUES($1,$2)`, id, input.SortOrder)
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE hhc_web.history_event SET sort_order=$2 WHERE entry_id=$1`, id, input.SortOrder)
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
	err := tx.QueryRowContext(ctx, `SELECT version FROM hhc_web.content_entry WHERE id=$1 AND module=$2 FOR UPDATE`, id, module).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		return content.ErrNotFound
	}
	if err != nil {
		return err
	}
	if current != expected {
		return content.ErrPrecondition
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
	value := content.PublicItem{ID: item.ID, Title: translation.Title, Summary: translation.Summary, Body: translation.Body, DateLabel: translation.DateLabel, DisplayDate: item.DisplayDate, ImageAlt: translation.ImageAlt, YouTubeVideoID: item.YouTubeVideoID, SortOrder: item.SortOrder, Featured: item.Featured, HomeEligible: item.HomeEligible}
	switch item.Module {
	case content.ModuleNews:
		value.ImageURL = "/api/assets/public/" + item.CoverAssetID + "/large"
		value.Href = "/" + translation.Locale + "/news/" + item.Slug
	case content.ModuleVideos:
		value.ImageURL = "https://img.youtube.com/vi/" + item.YouTubeVideoID + "/maxresdefault.jpg"
		value.Href = "https://www.youtube.com/watch?v=" + item.YouTubeVideoID
	}
	return value
}
