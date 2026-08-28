package contentseed

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
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
	lookupTarget func(context.Context, seedQuerier, string) (string, bool, error)
}

var plannerKinds = map[string]plannerKind{}

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
			return PlanReport{}, fmt.Errorf("planning for record kind %q is not released", record.Kind)
		}
		payload, err := kind.decode(record.Payload)
		if err != nil {
			return PlanReport{}, fmt.Errorf("validate %s/%s payload: %w", record.Kind, record.SourceKey, err)
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
		{name: "bulletins", query: `SELECT issue_number::text FROM hhc_web.bulletin_issue WHERE issue_number IS NOT NULL`, set: func(report *InventoryReport, value ModuleInventory) { report.Bulletins = value }},
		{name: "news", query: `SELECT slug FROM hhc_web.news_item`, set: func(report *InventoryReport, value ModuleInventory) { report.News = value }},
		{name: "history", query: `SELECT sort_order::text FROM hhc_web.history_event`, set: func(report *InventoryReport, value ModuleInventory) { report.History = value }},
		{name: "videos", query: `SELECT youtube_video_id FROM hhc_web.video_item`, set: func(report *InventoryReport, value ModuleInventory) { report.Videos = value }},
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
