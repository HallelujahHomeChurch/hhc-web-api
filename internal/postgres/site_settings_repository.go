package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/sitesettings"
)

type SiteSettingsRepository struct {
	db *sql.DB
}

func NewSiteSettingsRepository(db *sql.DB) *SiteSettingsRepository {
	return &SiteSettingsRepository{db: db}
}

func (r *SiteSettingsRepository) Get(ctx context.Context) (sitesettings.Settings, error) {
	return loadSiteSettings(ctx, r.db)
}

func (r *SiteSettingsRepository) Save(ctx context.Context, input sitesettings.WriteInput, expected int64, actor string, now time.Time) (sitesettings.Settings, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return sitesettings.Settings{}, err
	}
	defer tx.Rollback()
	if err := lockSiteSettings(ctx, tx, expected); err != nil {
		return sitesettings.Settings{}, err
	}
	if err := replaceSiteSettings(ctx, tx, input); err != nil {
		return sitesettings.Settings{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.site_setting_set SET status='draft',version=version+1,external_links_json=$2,updated_by=$3,updated_at=$4 WHERE id=$1`, sitesettings.SingletonID, mustJSON(input.Links), actor, now); err != nil {
		return sitesettings.Settings{}, err
	}
	value, err := loadSiteSettings(ctx, tx)
	if err != nil {
		return sitesettings.Settings{}, err
	}
	if err := insertSiteSettingsRevision(ctx, tx, value, "draft_saved", actor, now); err != nil {
		return sitesettings.Settings{}, err
	}
	return value, tx.Commit()
}

func (r *SiteSettingsRepository) Publish(ctx context.Context, expected int64, actor string, now time.Time) (sitesettings.Settings, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return sitesettings.Settings{}, err
	}
	defer tx.Rollback()
	if err := lockSiteSettings(ctx, tx, expected); err != nil {
		return sitesettings.Settings{}, err
	}
	current, err := loadSiteSettings(ctx, tx)
	if err != nil {
		return sitesettings.Settings{}, err
	}
	if _, ok := sitesettings.NormalizeWriteInput(sitesettings.WriteInput{Locales: current.Locales, Links: current.Links}); !ok {
		return sitesettings.Settings{}, sitesettings.ErrNotPublishable
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.site_setting_set SET status='published',version=version+1,published_by=$2,published_at=$3,updated_by=$2,updated_at=$3 WHERE id=$1`, sitesettings.SingletonID, actor, now); err != nil {
		return sitesettings.Settings{}, err
	}
	value, err := loadSiteSettings(ctx, tx)
	if err != nil {
		return sitesettings.Settings{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.public_projection WHERE resource_type='site_layout' OR projection_key LIKE 'site_layout:%'`); err != nil {
		return sitesettings.Settings{}, err
	}
	for _, locale := range value.Locales {
		payload, err := siteLayoutProjection(value, locale)
		if err != nil {
			return sitesettings.Settings{}, err
		}
		etag := fmt.Sprintf("%x", sha256.Sum256(payload))
		if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.public_projection(projection_key,resource_type,resource_id,locale,route_path,version,etag,payload_json,updated_at) VALUES($1,'site_layout',NULL,$2,'/site-layout',$3,$4,$5,$6)`, "site_layout:"+locale.Locale, locale.Locale, value.Version, etag, payload, now); err != nil {
			return sitesettings.Settings{}, err
		}
	}
	if err := insertSiteSettingsRevision(ctx, tx, value, "published", actor, now); err != nil {
		return sitesettings.Settings{}, err
	}
	return value, tx.Commit()
}

func (r *SiteSettingsRepository) Unpublish(ctx context.Context, expected int64, actor string, now time.Time) (sitesettings.Settings, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return sitesettings.Settings{}, err
	}
	defer tx.Rollback()
	if err := lockSiteSettings(ctx, tx, expected); err != nil {
		return sitesettings.Settings{}, err
	}
	current, err := loadSiteSettings(ctx, tx)
	if err != nil {
		return sitesettings.Settings{}, err
	}
	if current.Status != sitesettings.StatusPublished {
		return sitesettings.Settings{}, sitesettings.ErrNotPublishable
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.site_setting_set SET status='unpublished',version=version+1,updated_by=$2,updated_at=$3 WHERE id=$1`, sitesettings.SingletonID, actor, now); err != nil {
		return sitesettings.Settings{}, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.public_projection WHERE resource_type='site_layout' OR projection_key LIKE 'site_layout:%'`); err != nil {
		return sitesettings.Settings{}, err
	}
	value, err := loadSiteSettings(ctx, tx)
	if err != nil {
		return sitesettings.Settings{}, err
	}
	if err := insertSiteSettingsRevision(ctx, tx, value, "unpublished", actor, now); err != nil {
		return sitesettings.Settings{}, err
	}
	return value, tx.Commit()
}

func (r *SiteSettingsRepository) Revisions(ctx context.Context) ([]sitesettings.Revision, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT revision,revision_type,snapshot_json,created_by,created_at FROM hhc_web.site_setting_revision WHERE setting_set_id=$1 ORDER BY revision DESC`, sitesettings.SingletonID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []sitesettings.Revision{}
	for rows.Next() {
		var value sitesettings.Revision
		var snapshot []byte
		if err := rows.Scan(&value.Revision, &value.RevisionType, &snapshot, &value.CreatedBy, &value.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(snapshot, &value.Snapshot); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

func (r *SiteSettingsRepository) Restore(ctx context.Context, revision, expected int64, actor string, now time.Time) (sitesettings.Settings, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return sitesettings.Settings{}, err
	}
	defer tx.Rollback()
	if err := lockSiteSettings(ctx, tx, expected); err != nil {
		return sitesettings.Settings{}, err
	}
	var snapshot []byte
	if err := tx.QueryRowContext(ctx, `SELECT snapshot_json FROM hhc_web.site_setting_revision WHERE setting_set_id=$1 AND revision=$2`, sitesettings.SingletonID, revision).Scan(&snapshot); errors.Is(err, sql.ErrNoRows) {
		return sitesettings.Settings{}, sitesettings.ErrNotFound
	} else if err != nil {
		return sitesettings.Settings{}, err
	}
	var target sitesettings.Settings
	if err := json.Unmarshal(snapshot, &target); err != nil {
		return sitesettings.Settings{}, err
	}
	input, ok := sitesettings.NormalizeWriteInput(sitesettings.WriteInput{Locales: target.Locales, Links: target.Links})
	if !ok {
		return sitesettings.Settings{}, sitesettings.ErrInvalid
	}
	if err := replaceSiteSettings(ctx, tx, input); err != nil {
		return sitesettings.Settings{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.site_setting_set SET status='draft',version=version+1,external_links_json=$2,updated_by=$3,updated_at=$4 WHERE id=$1`, sitesettings.SingletonID, mustJSON(input.Links), actor, now); err != nil {
		return sitesettings.Settings{}, err
	}
	value, err := loadSiteSettings(ctx, tx)
	if err != nil {
		return sitesettings.Settings{}, err
	}
	if err := insertSiteSettingsRevision(ctx, tx, value, "restored_to_draft", actor, now); err != nil {
		return sitesettings.Settings{}, err
	}
	return value, tx.Commit()
}

func lockSiteSettings(ctx context.Context, tx *sql.Tx, expected int64) error {
	var current int64
	if err := tx.QueryRowContext(ctx, `SELECT version FROM hhc_web.site_setting_set WHERE id=$1 FOR UPDATE`, sitesettings.SingletonID).Scan(&current); errors.Is(err, sql.ErrNoRows) {
		return sitesettings.ErrNotFound
	} else if err != nil {
		return err
	}
	if current != expected {
		return sitesettings.ErrPrecondition
	}
	return nil
}

func replaceSiteSettings(ctx context.Context, tx *sql.Tx, input sitesettings.WriteInput) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.site_setting_locale WHERE setting_set_id=$1`, sitesettings.SingletonID); err != nil {
		return err
	}
	for _, locale := range input.Locales {
		if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.site_setting_locale(setting_set_id,locale,site_name,english_name,copyright_holder,all_rights_reserved,seo_title_suffix,seo_description_fallback,header_items_json,legal_items_json) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			sitesettings.SingletonID, locale.Locale, locale.SiteName, locale.EnglishName, locale.CopyrightHolder, locale.AllRightsReserved, locale.SEOTitleSuffix, locale.SEODescriptionFallback, mustJSON(locale.Header), mustJSON(locale.Legal)); err != nil {
			return err
		}
	}
	return nil
}

func loadSiteSettings(ctx context.Context, query bulletinQueryer) (sitesettings.Settings, error) {
	var value sitesettings.Settings
	var links []byte
	var published sql.NullTime
	err := query.QueryRowContext(ctx, `SELECT id,status,version,external_links_json,created_by,updated_by,COALESCE(published_by,''),published_at,created_at,updated_at FROM hhc_web.site_setting_set WHERE id=$1`, sitesettings.SingletonID).
		Scan(&value.ID, &value.Status, &value.Version, &links, &value.CreatedBy, &value.UpdatedBy, &value.PublishedBy, &published, &value.CreatedAt, &value.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return sitesettings.Settings{}, sitesettings.ErrNotFound
	}
	if err != nil {
		return sitesettings.Settings{}, err
	}
	if err := json.Unmarshal(links, &value.Links); err != nil {
		return sitesettings.Settings{}, err
	}
	if published.Valid {
		value.PublishedAt = &published.Time
	}
	rows, err := query.QueryContext(ctx, `SELECT locale,site_name,english_name,copyright_holder,all_rights_reserved,seo_title_suffix,seo_description_fallback,header_items_json,legal_items_json FROM hhc_web.site_setting_locale WHERE setting_set_id=$1 ORDER BY CASE locale WHEN 'zh-Hant' THEN 1 WHEN 'zh-Hans' THEN 2 WHEN 'en' THEN 3 WHEN 'ja' THEN 4 WHEN 'ko' THEN 5 END`, sitesettings.SingletonID)
	if err != nil {
		return sitesettings.Settings{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var locale sitesettings.LocaleSettings
		var header, legal []byte
		if err := rows.Scan(&locale.Locale, &locale.SiteName, &locale.EnglishName, &locale.CopyrightHolder, &locale.AllRightsReserved, &locale.SEOTitleSuffix, &locale.SEODescriptionFallback, &header, &legal); err != nil {
			return sitesettings.Settings{}, err
		}
		if err := json.Unmarshal(header, &locale.Header); err != nil {
			return sitesettings.Settings{}, err
		}
		if err := json.Unmarshal(legal, &locale.Legal); err != nil {
			return sitesettings.Settings{}, err
		}
		value.Locales = append(value.Locales, locale)
	}
	return value, rows.Err()
}

func insertSiteSettingsRevision(ctx context.Context, tx *sql.Tx, value sitesettings.Settings, revisionType, actor string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.site_setting_revision(id,setting_set_id,revision,revision_type,snapshot_json,created_by,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, platform.NewID(), sitesettings.SingletonID, value.Version, revisionType, mustJSON(value), actor, now)
	return err
}

func siteLayoutProjection(value sitesettings.Settings, locale sitesettings.LocaleSettings) ([]byte, error) {
	replaceLocale := func(items []sitesettings.NavItem) []sitesettings.NavItem {
		result := append([]sitesettings.NavItem(nil), items...)
		for index := range result {
			result[index].Href = strings.ReplaceAll(result[index].Href, "{locale}", locale.Locale)
		}
		return result
	}
	return json.Marshal(struct {
		Locale                 string                     `json:"locale"`
		SiteName               string                     `json:"siteName"`
		EnglishName            string                     `json:"englishName"`
		CopyrightHolder        string                     `json:"copyrightHolder"`
		AllRightsReserved      string                     `json:"allRightsReserved"`
		SEOTitleSuffix         string                     `json:"seoTitleSuffix"`
		SEODescriptionFallback string                     `json:"seoDescriptionFallback"`
		Header                 []sitesettings.NavItem     `json:"header"`
		Legal                  []sitesettings.NavItem     `json:"legal"`
		Links                  sitesettings.ExternalLinks `json:"links"`
		Version                int64                      `json:"version"`
		PublishedAt            *time.Time                 `json:"publishedAt"`
	}{locale.Locale, locale.SiteName, locale.EnglishName, locale.CopyrightHolder, locale.AllRightsReserved, locale.SEOTitleSuffix, locale.SEODescriptionFallback, replaceLocale(locale.Header), replaceLocale(locale.Legal), value.Links, value.Version, value.PublishedAt})
}

func mustJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}
