package contentseed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/sitesettings"
)

type Action string

const (
	ActionInsert   Action = "insert"
	ActionSkip     Action = "skip"
	ActionConflict Action = "conflict"
)

type PlannedRecord struct {
	Kind         string `json:"kind"`
	SourceKey    string `json:"sourceKey"`
	RecordSHA256 string `json:"recordSHA256"`
	Action       Action `json:"action"`
	targetID     string
}

type PlanReport struct {
	Records       []PlannedRecord `json:"records"`
	InsertCount   int             `json:"insertCount"`
	SkipCount     int             `json:"skipCount"`
	ConflictCount int             `json:"conflictCount"`
}

type ModuleInventory struct {
	Count        int    `json:"count"`
	KeySetSHA256 string `json:"keySetSHA256"`
}

type InventoryReport struct {
	Bulletins ModuleInventory `json:"bulletins"`
	News      ModuleInventory `json:"news"`
	History   ModuleInventory `json:"history"`
	Videos    ModuleInventory `json:"videos"`
}

type plannerKind struct {
	decode       func(json.RawMessage) (any, error)
	sourceKey    func(any) string
	lookupTarget func(context.Context, seedQuerier, string) (string, bool, error)
}

type locationSeedTranslation struct {
	Locale  string `json:"locale"`
	Name    string `json:"name"`
	Address string `json:"address"`
}

type locationSeedPayload struct {
	StableKey    string                    `json:"stableKey"`
	MapHref      string                    `json:"mapHref"`
	SortOrder    int                       `json:"sortOrder"`
	Translations []locationSeedTranslation `json:"translations"`
}

type siteLayoutSeedPayload struct {
	Locales []sitesettings.LocaleSettings `json:"locales"`
	Links   sitesettings.ExternalLinks    `json:"links"`
}

type pageSeedTranslation struct {
	Locale   string          `json:"locale"`
	BodyJSON json.RawMessage `json:"bodyJson"`
}

type pageSeedPayload struct {
	PageKey      string                `json:"pageKey"`
	PageTemplate string                `json:"pageTemplate"`
	RoutePath    string                `json:"routePath"`
	Indexable    *bool                 `json:"indexable"`
	Translations []pageSeedTranslation `json:"translations"`
}

var plannerKinds = map[string]plannerKind{
	"location": {
		decode:    decodeLocationSeedPayload,
		sourceKey: func(value any) string { return "location:" + value.(locationSeedPayload).StableKey },
		lookupTarget: func(ctx context.Context, db seedQuerier, sourceKey string) (string, bool, error) {
			var targetID string
			err := db.QueryRowContext(ctx, `SELECT content_id::text FROM hhc_web.location_item WHERE stable_key=$1`, strings.TrimPrefix(sourceKey, "location:")).Scan(&targetID)
			if errors.Is(err, sql.ErrNoRows) {
				return "", false, nil
			}
			return targetID, err == nil, err
		},
	},
	"site_layout": {
		decode:    decodeSiteLayoutSeedPayload,
		sourceKey: func(any) string { return "site-layout:" + sitesettings.SingletonID },
		lookupTarget: func(ctx context.Context, db seedQuerier, _ string) (string, bool, error) {
			var targetID string
			err := db.QueryRowContext(ctx, `SELECT id FROM hhc_web.site_setting_set WHERE id=$1`, sitesettings.SingletonID).Scan(&targetID)
			if errors.Is(err, sql.ErrNoRows) {
				return "", false, nil
			}
			return targetID, err == nil, err
		},
	},
	"page": {
		decode:    decodePageSeedPayload,
		sourceKey: func(value any) string { return "page:" + value.(pageSeedPayload).PageKey },
		lookupTarget: func(ctx context.Context, db seedQuerier, sourceKey string) (string, bool, error) {
			var targetID string
			err := db.QueryRowContext(ctx, `SELECT content_id::text FROM hhc_web.page_item WHERE page_key=$1`, strings.TrimPrefix(sourceKey, "page:")).Scan(&targetID)
			if errors.Is(err, sql.ErrNoRows) {
				return "", false, nil
			}
			return targetID, err == nil, err
		},
	},
}

type seedQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func Plan(ctx context.Context, db *sql.DB, manifest Manifest) (PlanReport, error) {
	return plan(ctx, db, manifest, plannerKinds)
}

func plan(ctx context.Context, db seedQuerier, manifest Manifest, kinds map[string]plannerKind) (PlanReport, error) {
	report := PlanReport{Records: make([]PlannedRecord, 0, len(manifest.Records))}
	for _, record := range manifest.Records {
		kind, ok := kinds[record.Kind]
		if !ok {
			return PlanReport{}, unreleasedTargetError(record.Kind)
		}
		payload, err := kind.decode(record.Payload)
		if err != nil {
			return PlanReport{}, fmt.Errorf("validate %s/%s payload: %w", record.Kind, record.SourceKey, err)
		}
		if kind.sourceKey != nil && record.SourceKey != kind.sourceKey(payload) {
			return PlanReport{}, fmt.Errorf("validate %s/%s payload: sourceKey must equal %q", record.Kind, record.SourceKey, kind.sourceKey(payload))
		}
		recordHash, err := canonicalSHA256(payload)
		if err != nil {
			return PlanReport{}, fmt.Errorf("canonicalize %s/%s payload: %w", record.Kind, record.SourceKey, err)
		}
		targetID, targetExists, err := kind.lookupTarget(ctx, db, record.SourceKey)
		if err != nil {
			return PlanReport{}, fmt.Errorf("look up %s/%s target: %w", record.Kind, record.SourceKey, err)
		}
		action := ActionInsert
		if targetExists {
			var matchingProvenance bool
			err := db.QueryRowContext(ctx, `SELECT EXISTS(
				SELECT 1
				FROM hhc_web.content_seed_source source
				JOIN hhc_web.content_seed_run run ON run.id=source.seed_run_id
				WHERE run.status='succeeded'
				  AND source.target_kind=$1
				  AND source.source_key=$2
				  AND source.record_sha256=$3
				  AND source.target_id=$4
			)`, record.Kind, record.SourceKey, recordHash, targetID).Scan(&matchingProvenance)
			if err != nil {
				return PlanReport{}, fmt.Errorf("look up %s/%s provenance: %w", record.Kind, record.SourceKey, err)
			}
			if matchingProvenance {
				action = ActionSkip
			} else {
				action = ActionConflict
			}
		}
		report.Records = append(report.Records, PlannedRecord{Kind: record.Kind, SourceKey: record.SourceKey, RecordSHA256: recordHash, Action: action, targetID: targetID})
		switch action {
		case ActionInsert:
			report.InsertCount++
		case ActionSkip:
			report.SkipCount++
		case ActionConflict:
			report.ConflictCount++
		}
	}
	return report, nil
}

func decodeLocationSeedPayload(raw json.RawMessage) (any, error) {
	var payload locationSeedPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("payload contains multiple JSON values")
		}
		return nil, err
	}
	input := payload.writeInput()
	locales := [...]string{"zh-Hant", "zh-Hans", "en", "ja", "ko"}
	if len(payload.Translations) != len(locales) || !content.ValidateLocation(input) {
		return nil, errors.New("location payload is invalid")
	}
	for i, locale := range locales {
		if payload.Translations[i].Locale != locale {
			return nil, errors.New("location translations must contain zh-Hant, zh-Hans, en, ja, and ko in order")
		}
	}
	return payload, nil
}

func (payload locationSeedPayload) writeInput() content.WriteInput {
	translations := make([]content.Translation, len(payload.Translations))
	for i, translation := range payload.Translations {
		translations[i] = content.Translation{Locale: translation.Locale, Title: translation.Name, Body: translation.Address}
	}
	return content.WriteInput{LocationKey: payload.StableKey, MapHref: payload.MapHref, SortOrder: payload.SortOrder, Translations: translations}
}

func decodeSiteLayoutSeedPayload(raw json.RawMessage) (any, error) {
	var payload siteLayoutSeedPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("payload contains multiple JSON values")
		}
		return nil, err
	}
	normalized, ok := sitesettings.NormalizeWriteInput(sitesettings.WriteInput{Locales: payload.Locales, Links: payload.Links})
	if !ok {
		return nil, errors.New("site layout payload is invalid")
	}
	return siteLayoutSeedPayload{Locales: normalized.Locales, Links: normalized.Links}, nil
}

func decodePageSeedPayload(raw json.RawMessage) (any, error) {
	var payload pageSeedPayload
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = errors.New("payload contains multiple JSON values")
		}
		return nil, err
	}
	if content.ValidatePageDefinition(payload.PageKey, payload.PageTemplate, payload.RoutePath) != nil || payload.Indexable == nil || len(payload.Translations) != 5 {
		return nil, errors.New("page payload is invalid")
	}
	locales := [...]string{"zh-Hant", "zh-Hans", "en", "ja", "ko"}
	for index, translation := range payload.Translations {
		if translation.Locale != locales[index] || content.ValidatePagePayload(payload.PageKey, translation.BodyJSON) != nil {
			return nil, errors.New("page payload is invalid")
		}
		var canonical any
		if err := json.Unmarshal(translation.BodyJSON, &canonical); err != nil {
			return nil, errors.New("page payload is invalid")
		}
		payload.Translations[index].BodyJSON, _ = json.Marshal(canonical)
	}
	return payload, nil
}

func (payload pageSeedPayload) writeInput() content.WriteInput {
	translations := make([]content.Translation, len(payload.Translations))
	for index, translation := range payload.Translations {
		title, summary, _ := content.PagePayloadMetadata(payload.PageKey, translation.BodyJSON)
		translations[index] = content.Translation{Locale: translation.Locale, Title: title, Summary: summary, BodyJSON: translation.BodyJSON}
	}
	return content.WriteInput{PageKey: payload.PageKey, PageTemplate: payload.PageTemplate, RoutePath: payload.RoutePath, Indexable: *payload.Indexable, Translations: translations}
}

func canonicalSHA256(payload any) (string, error) {
	canonical, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(canonical)
	return hex.EncodeToString(hash[:]), nil
}

func Inventory(ctx context.Context, db *sql.DB) (InventoryReport, error) {
	modules := []struct {
		name  string
		query string
		set   func(*InventoryReport, ModuleInventory)
	}{
		{name: "bulletins", query: `SELECT DISTINCT bulletin.issue_number::text
			FROM hhc_web.bulletin_issue bulletin
			JOIN hhc_web.public_projection projection
			  ON projection.resource_type='bulletin_issue' AND projection.resource_id=bulletin.id
			WHERE bulletin.issue_number IS NOT NULL`, set: func(report *InventoryReport, value ModuleInventory) { report.Bulletins = value }},
		{name: "news", query: `SELECT DISTINCT news.slug
			FROM hhc_web.news_item news
			JOIN hhc_web.public_projection projection
			  ON projection.resource_type='news' AND projection.resource_id=news.entry_id`, set: func(report *InventoryReport, value ModuleInventory) { report.News = value }},
		{name: "history", query: `SELECT DISTINCT history.sort_order::text
			FROM hhc_web.history_event history
			JOIN hhc_web.public_projection projection
			  ON projection.resource_type='history' AND projection.resource_id=history.entry_id`, set: func(report *InventoryReport, value ModuleInventory) { report.History = value }},
		{name: "videos", query: `SELECT DISTINCT video.youtube_video_id
			FROM hhc_web.video_item video
			JOIN hhc_web.public_projection projection
			  ON projection.resource_type='videos' AND projection.resource_id=video.entry_id`, set: func(report *InventoryReport, value ModuleInventory) { report.Videos = value }},
	}
	var report InventoryReport
	for _, module := range modules {
		inventory, err := inventoryModule(ctx, db, module.name, module.query)
		if err != nil {
			return InventoryReport{}, fmt.Errorf("inventory %s: %w", module.name, err)
		}
		module.set(&report, inventory)
	}
	return report, nil
}

func inventoryModule(ctx context.Context, db *sql.DB, name, query string) (ModuleInventory, error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return ModuleInventory{}, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return ModuleInventory{}, err
		}
		keys = append(keys, key)
	}
	if err := rows.Err(); err != nil {
		return ModuleInventory{}, err
	}
	sort.Strings(keys)
	hash := sha256.New()
	fmt.Fprintln(hash, name)
	for _, key := range keys {
		fmt.Fprintln(hash, key)
	}
	return ModuleInventory{Count: len(keys), KeySetSHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}
