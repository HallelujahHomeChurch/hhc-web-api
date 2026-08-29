package homev2migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/migrations"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/sitesettings"
	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestBuildPlanDeterministicallyConvertsPublishedSources(t *testing.T) {
	report, err := BuildPlan(validSnapshot(t))
	if err != nil {
		t.Fatal(err)
	}
	if report.Updates != 1 || report.Inserts != 0 || report.Deletes != 0 || report.Warnings != 0 || report.Conflicts != 0 || report.BannerState != "empty" || report.LocationCount != 2 || !hexSHA(report.SourceSHA256) || !hexSHA(report.PlanSHA256) {
		t.Fatalf("report=%#v", report)
	}
	if report.Sources.Home.Version != 6 || report.Sources.SiteSettings.Version != 4 || len(report.Sources.Locations) != 2 || len(report.LocaleSHA256) != 5 {
		t.Fatalf("sources=%#v hashes=%#v", report.Sources, report.LocaleSHA256)
	}
	var converted struct {
		SchemaVersion int                    `json:"schemaVersion"`
		Template      string                 `json:"template"`
		Data          content.HomePageDataV2 `json:"data"`
	}
	if err := json.Unmarshal(report.target.Translations[0].BodyJSON, &converted); err != nil {
		t.Fatal(err)
	}
	if converted.SchemaVersion != 2 || converted.Template != "home.v2" || converted.Data.HeroTitle != "Home zh-Hant" || converted.Data.HeroSubtitle != "Welcome" || converted.Data.KingdomJoyDescription != "Music" || converted.Data.AboutDescription != "About us" {
		t.Fatalf("converted=%#v", converted)
	}
	if report.target.PageKey != "home" || report.target.PageTemplate != "home.v2" || report.target.RoutePath != "/" || report.target.BannerAssetID != "" || report.target.Locations[0].Key != "taipei" || report.target.Locations[1].Key != "kaohsiung" {
		t.Fatalf("target=%#v", report.target)
	}
	again, err := BuildPlan(validSnapshot(t))
	if err != nil || again.PlanSHA256 != report.PlanSHA256 || again.SourceSHA256 != report.SourceSHA256 {
		t.Fatalf("again=%#v err=%v", again, err)
	}
}

func TestBuildPlanRejectsContractDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Snapshot)
	}{
		{"missing Home locale", func(value *Snapshot) { value.Home.Translations = value.Home.Translations[:4] }},
		{"missing Location locale", func(value *Snapshot) { value.Locations[0].Translations = value.Locations[0].Translations[:4] }},
		{"duplicate Location key", func(value *Snapshot) { value.Locations[1].Key = value.Locations[0].Key }},
		{"duplicate Location order", func(value *Snapshot) { value.Locations[1].SortOrder = value.Locations[0].SortOrder }},
		{"unsafe link", func(value *Snapshot) { value.SiteSettings.Links.ChurchFacebook = "http://facebook.com/hhc" }},
		{"unsafe map", func(value *Snapshot) { value.Locations[0].MapHref = "https://localhost/map" }},
		{"meeting time", func(value *Snapshot) {
			var payload map[string]any
			_ = json.Unmarshal(value.Home.Translations[0].Body, &payload)
			payload["data"].(map[string]any)["meetingTime"] = "10:00"
			value.Home.Translations[0].Body, _ = json.Marshal(payload)
		}},
		{"stale Home", func(value *Snapshot) { value.Home.CurrentVersion++ }},
		{"stale Location", func(value *Snapshot) { value.Locations[0].CurrentVersion++ }},
		{"non Home source", func(value *Snapshot) { value.Home.ID = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := validSnapshot(t)
			test.mutate(&value)
			if _, err := BuildPlan(value); err == nil {
				t.Fatal("invalid source was accepted")
			}
		})
	}
}

func TestPlanAndApplyCreateOneIdempotentHomeV2Draft(t *testing.T) {
	databaseURL := os.Getenv("HHW_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("HHW_TEST_DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := migrations.Run(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE hhc_web.site_setting_revision,hhc_web.site_setting_locale,hhc_web.site_setting_set,hhc_web.public_projection,hhc_web.content_revision,hhc_web.content_translation,hhc_web.content_entry CASCADE`); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	homeID := "00000000-0000-0000-0000-000000000101"
	if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.content_entry(id,module,status,version,idempotency_key,created_by,updated_by,published_at,created_at,updated_at) VALUES($1,'pages','published',6,'home','seed','seed',$2,$2,$2)`, homeID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.page_item(content_id,page_key,page_template,route_path,indexable) VALUES($1,'home','home.v1','/',true)`, homeID); err != nil {
		t.Fatal(err)
	}
	for _, locale := range locales {
		body := homeV1Payload(locale)
		if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.content_translation(entry_id,locale,title,summary,body_json) VALUES($1,$2,$3,'Welcome',$4)`, homeID, locale, "Home "+locale, body); err != nil {
			t.Fatal(err)
		}
		projection, _ := json.Marshal(content.PublicEditorialPage{PageKey: "home", Template: "home.v1", RoutePath: "/", Indexable: true, Content: body, ResolvedLocale: locale, AvailableLocales: locales[:], Version: 6, PublishedAt: now})
		if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.public_projection(projection_key,resource_type,resource_id,locale,route_path,version,etag,payload_json,updated_at) VALUES($1,'pages',$2,$3,'/',6,'etag',$4,$5)`, "page:"+locale+":home", homeID, locale, projection, now); err != nil {
			t.Fatal(err)
		}
	}
	links := content.HomeLinks{ChurchYouTube: "https://youtube.com/@hhc", ChurchFacebook: "https://facebook.com/hhc", MusicYouTube: "https://youtube.com/@music"}
	linksJSON, _ := json.Marshal(links)
	if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.site_setting_set(id,status,version,external_links_json,created_by,updated_by,published_at,created_at,updated_at) VALUES('default','published',4,$1,'seed','seed',$2,$2,$2)`, linksJSON, now); err != nil {
		t.Fatal(err)
	}
	for _, locale := range locales {
		layout, _ := json.Marshal(sitesettings.PublicLayout{Locale: locale, Links: sitesettings.ExternalLinks{ChurchYouTube: links.ChurchYouTube, ChurchFacebook: links.ChurchFacebook, MusicYouTube: links.MusicYouTube}, Version: 4, PublishedAt: &now})
		if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.public_projection(projection_key,resource_type,locale,route_path,version,etag,payload_json,updated_at) VALUES($1,'site_layout',$2,'/site-layout',4,'etag',$3,$4)`, "site_layout:"+locale, locale, layout, now); err != nil {
			t.Fatal(err)
		}
	}
	for locationIndex, key := range []string{"taipei", "kaohsiung"} {
		id := fmt.Sprintf("00000000-0000-0000-0000-%012d", 201+locationIndex)
		sortOrder := (locationIndex + 1) * 10
		if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.content_entry(id,module,status,version,idempotency_key,created_by,updated_by,published_at,created_at,updated_at) VALUES($1,'locations','published',3,$2,'seed','seed',$3,$3,$3)`, id, "location:"+key, now); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.location_item(content_id,stable_key,map_href,sort_order) VALUES($1,$2,$3,$4)`, id, key, "https://maps.example.com/"+key, sortOrder); err != nil {
			t.Fatal(err)
		}
		for _, locale := range locales {
			public := content.PublicLocation{ID: key, Name: key + " " + locale, Address: key + " address " + locale, MapHref: "https://maps.example.com/" + key, SortOrder: sortOrder}
			encoded, _ := json.Marshal(public)
			if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.public_projection(projection_key,resource_type,resource_id,locale,route_path,version,etag,payload_json,updated_at) VALUES($1,'locations',$2,$3,'/locations',3,'etag',$4,$5)`, "locations:"+locale+":"+id, id, locale, encoded, now); err != nil {
				t.Fatal(err)
			}
		}
	}
	service := New(db)
	plan, err := service.Plan(ctx)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := service.Apply(ctx, plan.SourceSHA256, plan.PlanSHA256, "migration")
	if err != nil || !applied.Applied {
		t.Fatalf("applied=%#v err=%v", applied, err)
	}
	var template, status string
	var version int64
	if err := db.QueryRowContext(ctx, `SELECT p.page_template,e.status,e.version FROM hhc_web.content_entry e JOIN hhc_web.page_item p ON p.content_id=e.id WHERE e.id=$1`, homeID).Scan(&template, &status, &version); err != nil || template != "home.v2" || status != "draft" || version != 7 {
		t.Fatalf("template=%s status=%s version=%d err=%v", template, status, version, err)
	}
	var liveV1, draftV2, revisions int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.public_projection WHERE resource_id=$1 AND payload_json->>'template'='home.v1'`, homeID).Scan(&liveV1); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.content_translation WHERE entry_id=$1 AND body_json->>'template'='home.v2'`, homeID).Scan(&draftV2); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.content_revision WHERE entry_id=$1 AND version=7`, homeID).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if liveV1 != 5 || draftV2 != 5 || revisions != 1 {
		t.Fatalf("liveV1=%d draftV2=%d revisions=%d", liveV1, draftV2, revisions)
	}
	again, err := service.Apply(ctx, plan.SourceSHA256, plan.PlanSHA256, "migration")
	if err != nil || again.Applied {
		t.Fatalf("again=%#v err=%v", again, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT version FROM hhc_web.content_entry WHERE id=$1`, homeID).Scan(&version); err != nil || version != 7 {
		t.Fatalf("version=%d err=%v", version, err)
	}
}

func validSnapshot(t *testing.T) Snapshot {
	t.Helper()
	homeTranslations := make([]TranslationSource, 0, len(locales))
	for _, locale := range locales {
		homeTranslations = append(homeTranslations, TranslationSource{Locale: locale, Body: homeV1Payload(locale)})
	}
	location := func(id, key string, order int64) LocationSource {
		translations := make([]content.HomeLocationTranslation, 0, len(locales))
		for _, locale := range locales {
			translations = append(translations, content.HomeLocationTranslation{Locale: locale, Name: key + " " + locale, Address: key + " address " + locale})
		}
		return LocationSource{ID: id, Key: key, MapHref: "https://maps.example.com/" + key, Version: 3, CurrentVersion: 3, CurrentStatus: content.StatusPublished, SortOrder: int(order), Translations: translations}
	}
	return Snapshot{
		Home:         HomeSource{ID: "home-id", Version: 6, CurrentVersion: 6, CurrentTemplate: "home.v1", CurrentStatus: content.StatusPublished, Indexable: true, Translations: homeTranslations},
		SiteSettings: SiteSettingsSource{ID: sitesettings.SingletonID, Version: 4, Links: content.HomeLinks{ChurchYouTube: "https://youtube.com/@hhc", ChurchFacebook: "https://facebook.com/hhc", MusicYouTube: "https://youtube.com/@music"}},
		Locations:    []LocationSource{location("location-2", "kaohsiung", 20), location("location-1", "taipei", 10)},
	}
}

func homeV1Payload(locale string) json.RawMessage {
	value := map[string]any{"schemaVersion": 1, "template": "home.v1", "data": map[string]any{
		"heroTitle": "Home " + locale, "heroSubtitle": "Welcome", "newsTitle": "News", "moreNews": "More", "weeklyTitle": "Weekly", "downloadWeekly": "Download", "videosTitle": "Videos", "videosSubtitle": "Music", "watchMore": "Watch", "aboutTitle": "About", "aboutBody": "About us", "aboutCta": "Meet us", "locationsTitle": "Locations", "mapLink": "Map",
	}}
	encoded, _ := json.Marshal(value)
	return encoded
}
