package homev2migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/sitesettings"
)

var locales = [...]string{"zh-Hant", "zh-Hans", "en", "ja", "ko"}

type TranslationSource struct {
	Locale string
	Body   json.RawMessage
}

type HomeSource struct {
	ID, CurrentTemplate, CurrentStatus string
	Version, CurrentVersion            int64
	Indexable                          bool
	Translations                       []TranslationSource
	CreatedBy, UpdatedBy               string
	CreatedAt, UpdatedAt               time.Time
	PublishedAt                        *time.Time
}

type SiteSettingsSource struct {
	ID      string
	Version int64
	Links   content.HomeLinks
}

type LocationSource struct {
	ID, Key, MapHref        string
	Version, CurrentVersion int64
	CurrentStatus           string
	SortOrder               int
	Translations            []content.HomeLocationTranslation
}

type Snapshot struct {
	Home         HomeSource
	SiteSettings SiteSettingsSource
	Locations    []LocationSource
}

type VersionRef struct {
	ID      string `json:"id"`
	Version int64  `json:"version"`
}

type LocationVersion struct {
	ID        string `json:"id"`
	Key       string `json:"key"`
	Version   int64  `json:"version"`
	SortOrder int    `json:"sortOrder"`
}

type SourceEvidence struct {
	Home         VersionRef        `json:"home"`
	SiteSettings VersionRef        `json:"siteSettings"`
	Locations    []LocationVersion `json:"locations"`
}

type LocaleHash struct {
	Locale string `json:"locale"`
	SHA256 string `json:"sha256"`
}

type Report struct {
	Mode          string            `json:"mode"`
	Sources       SourceEvidence    `json:"sources"`
	SourceSHA256  string            `json:"sourceSHA256"`
	LocaleSHA256  []LocaleHash      `json:"localeSHA256"`
	Links         content.HomeLinks `json:"links"`
	LocationCount int               `json:"locationCount"`
	BannerState   string            `json:"bannerState"`
	Updates       int               `json:"updates"`
	Inserts       int               `json:"inserts"`
	Deletes       int               `json:"deletes"`
	Warnings      int               `json:"warnings"`
	Conflicts     int               `json:"conflicts"`
	PlanSHA256    string            `json:"planSHA256"`
	Applied       bool              `json:"applied"`
	target        content.WriteInput
}

type Service struct{ db *sql.DB }

func New(db *sql.DB) *Service { return &Service{db: db} }

func (s *Service) Plan(ctx context.Context) (Report, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return Report{}, err
	}
	defer tx.Rollback()
	snapshot, err := loadSnapshot(ctx, tx, false)
	if err != nil {
		return Report{}, err
	}
	report, err := BuildPlan(snapshot)
	if err != nil {
		return Report{}, err
	}
	return report, tx.Commit()
}

func (s *Service) Apply(ctx context.Context, expectedSourceSHA, expectedPlanSHA, actor string) (Report, error) {
	if !hexSHA(expectedSourceSHA) || !hexSHA(expectedPlanSHA) || strings.TrimSpace(actor) == "" {
		return Report{}, errors.New("reviewed source and plan SHA-256 values are required")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return Report{}, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, advisoryLockID()); err != nil {
		return Report{}, fmt.Errorf("acquire Home v2 migration lock: %w", err)
	}
	snapshot, err := loadSnapshot(ctx, tx, true)
	if err != nil {
		return Report{}, err
	}
	report, err := BuildPlan(snapshot)
	if err != nil {
		return Report{}, err
	}
	if report.SourceSHA256 != expectedSourceSHA || report.PlanSHA256 != expectedPlanSHA {
		return Report{}, errors.New("reviewed Home v2 migration evidence is stale")
	}
	applied, err := applyDraft(ctx, tx, snapshot, report.target, actor)
	if err != nil {
		return Report{}, err
	}
	report.Mode, report.Applied = "apply", applied
	return report, tx.Commit()
}

func BuildPlan(snapshot Snapshot) (Report, error) {
	if snapshot.Home.ID == "" || snapshot.Home.Version < 1 || snapshot.SiteSettings.ID != sitesettings.SingletonID || snapshot.SiteSettings.Version < 1 {
		return Report{}, errors.New("canonical published Home and Site Settings sources are required")
	}
	if snapshot.Home.CurrentTemplate == "home.v1" {
		if snapshot.Home.CurrentStatus != content.StatusPublished || snapshot.Home.CurrentVersion != snapshot.Home.Version {
			return Report{}, errors.New("Home v1 source is stale or noncanonical")
		}
	} else if snapshot.Home.CurrentTemplate != "home.v2" || snapshot.Home.CurrentStatus != content.StatusDraft || snapshot.Home.CurrentVersion != snapshot.Home.Version+1 {
		return Report{}, errors.New("Home source is not the canonical v1 or idempotent v2 draft")
	}
	if len(snapshot.Home.Translations) != len(locales) || !safeLinks(snapshot.SiteSettings.Links) {
		return Report{}, errors.New("Home locales or Site Settings links are invalid")
	}
	translations := make([]content.Translation, len(locales))
	localeHashes := make([]LocaleHash, len(locales))
	for index, locale := range locales {
		source := snapshot.Home.Translations[index]
		if source.Locale != locale || content.ValidatePagePayload("home", source.Body) != nil {
			return Report{}, errors.New("published Home must contain exact v1 translations in canonical locale order")
		}
		var envelope struct {
			SchemaVersion int                  `json:"schemaVersion"`
			Template      string               `json:"template"`
			Data          content.HomePageData `json:"data"`
		}
		if strictDecode(source.Body, &envelope) != nil || envelope.SchemaVersion != 1 || envelope.Template != "home.v1" {
			return Report{}, errors.New("published Home v1 payload is invalid")
		}
		v2 := struct {
			SchemaVersion int                    `json:"schemaVersion"`
			Template      string                 `json:"template"`
			Data          content.HomePageDataV2 `json:"data"`
		}{2, "home.v2", content.HomePageDataV2{HeroTitle: envelope.Data.HeroTitle, HeroSubtitle: envelope.Data.HeroSubtitle, KingdomJoyDescription: envelope.Data.VideosSubtitle, AboutDescription: envelope.Data.AboutBody}}
		body, _ := json.Marshal(v2)
		if content.ValidatePagePayload("home", body) != nil {
			return Report{}, errors.New("converted Home v2 payload is invalid")
		}
		title, summary, _ := content.PagePayloadMetadata("home", body)
		translations[index] = content.Translation{Locale: locale, Title: title, Summary: summary, BodyJSON: body}
		localeHashes[index] = LocaleHash{Locale: locale, SHA256: sha(body)}
	}
	locations := append([]LocationSource(nil), snapshot.Locations...)
	sort.Slice(locations, func(i, j int) bool { return locations[i].SortOrder < locations[j].SortOrder })
	homeLocations := make([]content.HomeLocation, len(locations))
	locationVersions := make([]LocationVersion, len(locations))
	keys, orders := map[string]bool{}, map[int]bool{}
	for index, source := range locations {
		if source.ID == "" || source.Version < 1 || source.CurrentVersion != source.Version || source.CurrentStatus != content.StatusPublished || keys[source.Key] || orders[source.SortOrder] || len(source.Translations) != len(locales) {
			return Report{}, errors.New("published Location sources are stale, duplicate, or incomplete")
		}
		keys[source.Key], orders[source.SortOrder] = true, true
		translationsForLocation := make([]content.Translation, len(locales))
		for translationIndex, locale := range locales {
			translation := source.Translations[translationIndex]
			if translation.Locale != locale {
				return Report{}, errors.New("published Location locales are incomplete")
			}
			translationsForLocation[translationIndex] = content.Translation{Locale: locale, Title: translation.Name, Body: translation.Address}
		}
		if !content.ValidateLocation(content.WriteInput{LocationKey: source.Key, MapHref: source.MapHref, SortOrder: source.SortOrder, Translations: translationsForLocation}) {
			return Report{}, errors.New("published Location is invalid")
		}
		homeLocations[index] = content.HomeLocation{Key: source.Key, MapHref: source.MapHref, SortOrder: source.SortOrder, Translations: append([]content.HomeLocationTranslation(nil), source.Translations...)}
		locationVersions[index] = LocationVersion{ID: source.ID, Key: source.Key, Version: source.Version, SortOrder: source.SortOrder}
	}
	evidence := SourceEvidence{Home: VersionRef{ID: snapshot.Home.ID, Version: snapshot.Home.Version}, SiteSettings: VersionRef{ID: snapshot.SiteSettings.ID, Version: snapshot.SiteSettings.Version}, Locations: locationVersions}
	report := Report{Mode: "plan", Sources: evidence, SourceSHA256: hashJSON(evidence), LocaleSHA256: localeHashes, Links: snapshot.SiteSettings.Links, LocationCount: len(homeLocations), BannerState: "empty", Updates: 1}
	report.target = content.WriteInput{PageKey: "home", PageTemplate: "home.v2", RoutePath: "/", Indexable: snapshot.Home.Indexable, Links: snapshot.SiteSettings.Links, Locations: homeLocations, Translations: translations}
	report.PlanSHA256 = hashJSON(struct {
		Sources       SourceEvidence    `json:"sources"`
		SourceSHA256  string            `json:"sourceSHA256"`
		LocaleSHA256  []LocaleHash      `json:"localeSHA256"`
		Links         content.HomeLinks `json:"links"`
		LocationCount int               `json:"locationCount"`
		BannerState   string            `json:"bannerState"`
		Updates       int               `json:"updates"`
		Inserts       int               `json:"inserts"`
		Deletes       int               `json:"deletes"`
		Warnings      int               `json:"warnings"`
		Conflicts     int               `json:"conflicts"`
	}{report.Sources, report.SourceSHA256, report.LocaleSHA256, report.Links, report.LocationCount, report.BannerState, report.Updates, 0, 0, 0, 0})
	return report, nil
}

func loadSnapshot(ctx context.Context, tx *sql.Tx, lock bool) (Snapshot, error) {
	lockClause := ""
	if lock {
		lockClause = " FOR UPDATE OF e,p"
	}
	var snapshot Snapshot
	err := tx.QueryRowContext(ctx, `SELECT e.id::text,e.status,e.version,p.page_template,p.indexable,e.created_by,e.updated_by,e.created_at,e.updated_at,e.published_at
		FROM hhc_web.content_entry e JOIN hhc_web.page_item p ON p.content_id=e.id
		WHERE e.module='pages' AND p.page_key='home' AND p.route_path='/'`+lockClause).
		Scan(&snapshot.Home.ID, &snapshot.Home.CurrentStatus, &snapshot.Home.CurrentVersion, &snapshot.Home.CurrentTemplate, &snapshot.Home.Indexable, &snapshot.Home.CreatedBy, &snapshot.Home.UpdatedBy, &snapshot.Home.CreatedAt, &snapshot.Home.UpdatedAt, &snapshot.Home.PublishedAt)
	if err != nil {
		return Snapshot{}, fmt.Errorf("load fixed Home: %w", err)
	}
	homeRows, err := tx.QueryContext(ctx, `SELECT locale,version,payload_json FROM hhc_web.public_projection WHERE resource_type='pages' AND resource_id=$1 AND projection_key LIKE 'page:%:home' ORDER BY CASE locale WHEN 'zh-Hant' THEN 1 WHEN 'zh-Hans' THEN 2 WHEN 'en' THEN 3 WHEN 'ja' THEN 4 WHEN 'ko' THEN 5 ELSE 6 END`, snapshot.Home.ID)
	if err != nil {
		return Snapshot{}, err
	}
	defer homeRows.Close()
	for homeRows.Next() {
		var locale string
		var version int64
		var raw []byte
		if err := homeRows.Scan(&locale, &version, &raw); err != nil {
			return Snapshot{}, err
		}
		var page content.PublicEditorialPage
		if strictDecode(raw, &page) != nil || page.PageKey != "home" || page.Template != "home.v1" || page.ResolvedLocale != locale || page.Version != version {
			return Snapshot{}, errors.New("published Home projection is noncanonical")
		}
		if snapshot.Home.Version == 0 {
			snapshot.Home.Version = version
		} else if snapshot.Home.Version != version {
			return Snapshot{}, errors.New("published Home versions differ by locale")
		}
		snapshot.Home.Translations = append(snapshot.Home.Translations, TranslationSource{Locale: locale, Body: page.Content})
	}
	if err := homeRows.Err(); err != nil {
		return Snapshot{}, err
	}
	var siteStatus string
	if err := tx.QueryRowContext(ctx, `SELECT id,status,version FROM hhc_web.site_setting_set WHERE id='default'`+func() string {
		if lock {
			return " FOR SHARE"
		}
		return ""
	}()).Scan(&snapshot.SiteSettings.ID, &siteStatus, &snapshot.SiteSettings.Version); err != nil {
		return Snapshot{}, err
	}
	if siteStatus != sitesettings.StatusPublished {
		return Snapshot{}, errors.New("Site Settings source is not published")
	}
	siteRows, err := tx.QueryContext(ctx, `SELECT locale,payload_json FROM hhc_web.public_projection WHERE resource_type='site_layout' AND version=$1 ORDER BY CASE locale WHEN 'zh-Hant' THEN 1 WHEN 'zh-Hans' THEN 2 WHEN 'en' THEN 3 WHEN 'ja' THEN 4 WHEN 'ko' THEN 5 ELSE 6 END`, snapshot.SiteSettings.Version)
	if err != nil {
		return Snapshot{}, err
	}
	defer siteRows.Close()
	siteLocaleCount := 0
	for siteRows.Next() {
		var locale string
		var siteRaw []byte
		if err := siteRows.Scan(&locale, &siteRaw); err != nil {
			return Snapshot{}, err
		}
		var layout sitesettings.PublicLayout
		if siteLocaleCount >= len(locales) || strictDecode(siteRaw, &layout) != nil || locale != locales[siteLocaleCount] || layout.Locale != locale || layout.Version != snapshot.SiteSettings.Version {
			return Snapshot{}, errors.New("published Site Settings projection is noncanonical")
		}
		links := content.HomeLinks{ChurchYouTube: layout.Links.ChurchYouTube, ChurchFacebook: layout.Links.ChurchFacebook, MusicYouTube: layout.Links.MusicYouTube}
		if siteLocaleCount == 0 {
			snapshot.SiteSettings.Links = links
		} else if snapshot.SiteSettings.Links != links {
			return Snapshot{}, errors.New("published Site Settings links differ by locale")
		}
		siteLocaleCount++
	}
	if err := siteRows.Err(); err != nil {
		return Snapshot{}, err
	}
	if siteLocaleCount != len(locales) {
		return Snapshot{}, errors.New("published Site Settings locales are incomplete")
	}
	locationLock := ""
	if lock {
		locationLock = " FOR SHARE OF e,l"
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.id::text,e.status,e.version,l.stable_key,l.map_href,l.sort_order,p.locale,p.version,p.payload_json
		FROM hhc_web.content_entry e JOIN hhc_web.location_item l ON l.content_id=e.id JOIN hhc_web.public_projection p ON p.resource_type='locations' AND p.resource_id=e.id
		ORDER BY l.sort_order,l.stable_key,CASE p.locale WHEN 'zh-Hant' THEN 1 WHEN 'zh-Hans' THEN 2 WHEN 'en' THEN 3 WHEN 'ja' THEN 4 WHEN 'ko' THEN 5 ELSE 6 END`+locationLock)
	if err != nil {
		return Snapshot{}, err
	}
	defer rows.Close()
	byID := map[string]int{}
	for rows.Next() {
		var id, status, key, mapHref, locale string
		var currentVersion, sortOrder, version int64
		var raw []byte
		if err := rows.Scan(&id, &status, &currentVersion, &key, &mapHref, &sortOrder, &locale, &version, &raw); err != nil {
			return Snapshot{}, err
		}
		var public content.PublicLocation
		if strictDecode(raw, &public) != nil || public.ID != key || public.SortOrder != int(sortOrder) {
			return Snapshot{}, errors.New("published Location projection is noncanonical")
		}
		index, ok := byID[id]
		if !ok {
			index = len(snapshot.Locations)
			byID[id] = index
			snapshot.Locations = append(snapshot.Locations, LocationSource{ID: id, Key: key, MapHref: mapHref, SortOrder: int(sortOrder), Version: version, CurrentVersion: currentVersion, CurrentStatus: status})
		}
		source := &snapshot.Locations[index]
		if source.Version != version || source.Key != key || source.SortOrder != int(sortOrder) {
			return Snapshot{}, errors.New("published Location versions differ by locale")
		}
		source.Translations = append(source.Translations, content.HomeLocationTranslation{Locale: locale, Name: public.Name, Address: public.Address})
	}
	return snapshot, rows.Err()
}

func applyDraft(ctx context.Context, tx *sql.Tx, snapshot Snapshot, target content.WriteInput, actor string) (bool, error) {
	if snapshot.Home.CurrentTemplate == "home.v2" {
		var settings []byte
		if err := tx.QueryRowContext(ctx, `SELECT home_settings FROM hhc_web.page_item WHERE content_id=$1`, snapshot.Home.ID).Scan(&settings); err != nil {
			return false, err
		}
		var current struct {
			Links     content.HomeLinks      `json:"links"`
			Locations []content.HomeLocation `json:"locations"`
		}
		if json.Unmarshal(settings, &current) != nil || current.Links != target.Links || !equalJSON(current.Locations, target.Locations) {
			return false, errors.New("existing Home v2 draft conflicts with reviewed migration")
		}
		rows, err := tx.QueryContext(ctx, `SELECT locale,body_json FROM hhc_web.content_translation WHERE entry_id=$1 ORDER BY CASE locale WHEN 'zh-Hant' THEN 1 WHEN 'zh-Hans' THEN 2 WHEN 'en' THEN 3 WHEN 'ja' THEN 4 WHEN 'ko' THEN 5 ELSE 6 END`, snapshot.Home.ID)
		if err != nil {
			return false, err
		}
		defer rows.Close()
		index := 0
		for rows.Next() {
			var locale string
			var body []byte
			if err := rows.Scan(&locale, &body); err != nil {
				return false, err
			}
			if index >= len(target.Translations) || locale != target.Translations[index].Locale || !equalJSON(json.RawMessage(body), target.Translations[index].BodyJSON) {
				return false, errors.New("existing Home v2 translations conflict with reviewed migration")
			}
			index++
		}
		if err := rows.Err(); err != nil || index != len(target.Translations) {
			return false, errors.New("existing Home v2 translations conflict with reviewed migration")
		}
		return false, nil
	}
	settings, _ := json.Marshal(struct {
		Links     content.HomeLinks      `json:"links"`
		Locations []content.HomeLocation `json:"locations"`
	}{target.Links, target.Locations})
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := tx.ExecContext(ctx, `UPDATE hhc_web.page_item SET page_template='home.v2',banner_asset_id=NULL,home_settings=$2 WHERE content_id=$1`, snapshot.Home.ID, settings); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM hhc_web.content_translation WHERE entry_id=$1`, snapshot.Home.ID); err != nil {
		return false, err
	}
	for _, translation := range target.Translations {
		if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.content_translation(entry_id,locale,title,summary,body,date_label,image_alt,body_json) VALUES($1,$2,$3,$4,'','','',$5)`, snapshot.Home.ID, translation.Locale, translation.Title, translation.Summary, translation.BodyJSON); err != nil {
			return false, err
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='draft',version=version+1,updated_by=$2,updated_at=$3 WHERE id=$1 AND version=$4`, snapshot.Home.ID, actor, now, snapshot.Home.CurrentVersion)
	if err != nil {
		return false, err
	}
	if rows, _ := result.RowsAffected(); rows != 1 {
		return false, errors.New("Home source version changed during migration")
	}
	item := content.Item{ID: snapshot.Home.ID, Module: content.ModulePages, Status: content.StatusDraft, Version: snapshot.Home.CurrentVersion + 1, PageKey: "home", PageTemplate: "home.v2", RoutePath: "/", Indexable: snapshot.Home.Indexable, Links: target.Links, Locations: target.Locations, Translations: target.Translations, IsPublished: true, PublishedVersion: snapshot.Home.Version, CreatedBy: snapshot.Home.CreatedBy, UpdatedBy: actor, PublishedAt: snapshot.Home.PublishedAt, CreatedAt: snapshot.Home.CreatedAt, UpdatedAt: now}
	revision, _ := json.Marshal(item)
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.content_revision(entry_id,version,snapshot_json,created_by,created_at) VALUES($1,$2,$3,$4,$5)`, item.ID, item.Version, revision, actor, now); err != nil {
		return false, err
	}
	return true, nil
}

func strictDecode(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("multiple JSON values")
	}
	return nil
}
func safeLinks(links content.HomeLinks) bool {
	return sitesettings.ValidExternalURL(links.ChurchYouTube) && sitesettings.ValidExternalURL(links.ChurchFacebook) && sitesettings.ValidExternalURL(links.MusicYouTube)
}
func sha(value []byte) string   { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }
func hashJSON(value any) string { encoded, _ := json.Marshal(value); return sha(encoded) }
func equalJSON(a, b any) bool {
	left, leftErr := json.Marshal(a)
	right, rightErr := json.Marshal(b)
	var leftValue, rightValue any
	if leftErr != nil || rightErr != nil || json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return hashJSON(leftValue) == hashJSON(rightValue)
}
func hexSHA(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func advisoryLockID() int64 {
	sum := sha256.Sum256([]byte("hhc-web-api:home-v2-migration"))
	return int64(binary.BigEndian.Uint64(sum[:8]))
}
