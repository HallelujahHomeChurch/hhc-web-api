package contentseed

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/sitesettings"
)

type Report struct {
	Mode           string `json:"mode"`
	SeedVersion    string `json:"seedVersion"`
	ManifestSHA256 string `json:"manifestSHA256"`
	Inserts        int    `json:"inserts"`
	Skips          int    `json:"skips"`
	Updates        int    `json:"updates"`
	Deletes        int    `json:"deletes"`
	Warnings       int    `json:"warnings"`
	Conflicts      int    `json:"conflicts"`
}

type applyPlan struct {
	Report
	Records []PlannedRecord
}

type applyPlanner func(context.Context, seedQuerier, Manifest) (applyPlan, error)
type recordApplier func(context.Context, *sql.Tx, Record) (string, error)

func Apply(ctx context.Context, db *sql.DB, manifest Manifest, manifestSHA, actor string) (Report, error) {
	return apply(ctx, db, manifest, manifestSHA, actor, defaultApplyPlan, func(ctx context.Context, tx *sql.Tx, record Record) (string, error) {
		return applySeedRecord(ctx, tx, record, actor)
	})
}

func apply(ctx context.Context, db *sql.DB, manifest Manifest, manifestSHA, actor string, planner applyPlanner, applyOne recordApplier) (report Report, err error) {
	report = Report{Mode: "apply", SeedVersion: manifest.SeedVersion, ManifestSHA256: manifestSHA}
	conn, err := db.Conn(ctx)
	if err != nil {
		return report, err
	}
	defer conn.Close()
	lockID := advisoryLockID(manifest.SeedVersion, manifestSHA)
	if _, err = conn.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		return report, fmt.Errorf("acquire content seed lock: %w", err)
	}
	defer func() {
		if _, unlockErr := conn.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, lockID); unlockErr != nil {
			err = errors.Join(err, fmt.Errorf("release content seed lock: %w", unlockErr))
			_ = conn.Raw(func(any) error { return driver.ErrBadConn })
		}
	}()

	var priorSHA string
	prior := Report{Mode: "apply", SeedVersion: manifest.SeedVersion, ManifestSHA256: manifestSHA}
	err = conn.QueryRowContext(ctx, `SELECT manifest_sha256, inserted_count, skipped_count, warning_count, conflict_count
		FROM hhc_web.content_seed_run
		WHERE seed_version=$1 AND status='succeeded'`, manifest.SeedVersion).Scan(&priorSHA, &prior.Inserts, &prior.Skips, &prior.Warnings, &prior.Conflicts)
	if err == nil {
		if priorSHA != manifestSHA {
			return report, fmt.Errorf("seed version %q already succeeded with a different manifest SHA", manifest.SeedVersion)
		}
		return prior, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return report, fmt.Errorf("look up successful content seed run: %w", err)
	}

	planned, err := planner(ctx, conn, manifest)
	if err != nil {
		return report, err
	}
	report.Inserts = planned.Inserts
	report.Skips = planned.Skips
	report.Warnings = planned.Warnings
	report.Conflicts = planned.Conflicts
	if report.Warnings != 0 || report.Conflicts != 0 {
		return report, fmt.Errorf("apply preflight has %d warnings and %d conflicts", report.Warnings, report.Conflicts)
	}
	if len(planned.Records) != len(manifest.Records) {
		return report, errors.New("apply plan record count does not match manifest")
	}

	runID, err := randomID("seed-run-")
	if err != nil {
		return report, err
	}
	if _, err = conn.ExecContext(ctx, `INSERT INTO hhc_web.content_seed_run(
		id,seed_version,source_repo,source_commit,manifest_sha256,mode,status,created_by,started_at
	) VALUES($1,$2,$3,$4,$5,'apply','started',$6,now())`, runID, manifest.SeedVersion, manifest.SourceRepo, manifest.SourceCommit, manifestSHA, actor); err != nil {
		return report, fmt.Errorf("create content seed run: %w", err)
	}

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return report, failAttempt(ctx, conn, runID, err)
	}
	sourceHashes := make(map[string]string, len(manifest.Sources))
	for _, source := range manifest.Sources {
		sourceHashes[source.Path] = source.SHA256
	}
	for i, record := range manifest.Records {
		plannedRecord := planned.Records[i]
		targetID := plannedRecord.targetID
		status := "skipped"
		switch plannedRecord.Action {
		case ActionInsert:
			status = "inserted"
			targetID, err = applyOne(ctx, tx, record)
		case ActionSkip:
		default:
			err = fmt.Errorf("unexpected apply action %q", plannedRecord.Action)
		}
		if err != nil {
			return report, rollbackAndFail(ctx, conn, tx, runID, fmt.Errorf("apply %s/%s: %w", record.Kind, record.SourceKey, err))
		}
		for _, sourcePath := range record.SourcePaths {
			sourceID, idErr := randomID("seed-source-")
			if idErr != nil {
				return report, rollbackAndFail(ctx, conn, tx, runID, idErr)
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.content_seed_source(
				id,seed_run_id,source_path,source_key,source_sha256,record_sha256,target_kind,target_id,status,created_at
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,now())`, sourceID, runID, sourcePath, record.SourceKey, sourceHashes[sourcePath], plannedRecord.RecordSHA256, record.Kind, targetID, status)
			if err != nil {
				return report, rollbackAndFail(ctx, conn, tx, runID, fmt.Errorf("record %s/%s provenance: %w", record.Kind, record.SourceKey, err))
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.content_seed_run
		SET status='succeeded',inserted_count=$1,skipped_count=$2,warning_count=$3,conflict_count=$4,finished_at=now()
		WHERE id=$5 AND status='started'`, report.Inserts, report.Skips, report.Warnings, report.Conflicts, runID); err != nil {
		return report, rollbackAndFail(ctx, conn, tx, runID, fmt.Errorf("mark content seed run succeeded: %w", err))
	}
	if err = tx.Commit(); err != nil {
		return report, failAttempt(ctx, conn, runID, fmt.Errorf("commit content seed run: %w", err))
	}
	return report, nil
}

func defaultApplyPlan(ctx context.Context, db seedQuerier, manifest Manifest) (applyPlan, error) {
	planned, err := plan(ctx, db, manifest, plannerKinds)
	if err != nil {
		return applyPlan{}, err
	}
	return applyPlan{Report: Report{Inserts: planned.InsertCount, Skips: planned.SkipCount, Conflicts: planned.ConflictCount}, Records: planned.Records}, nil
}

func applyRecord(_ context.Context, _ *sql.Tx, record Record) (string, error) {
	return "", unreleasedTargetError(record.Kind)
}

func applySeedRecord(ctx context.Context, tx *sql.Tx, record Record, actor string) (string, error) {
	if record.Kind == "site_layout" {
		return applySiteLayoutSeedRecord(ctx, tx, record, actor)
	}
	if record.Kind == "page" {
		return applyPageSeedRecord(ctx, tx, record, actor)
	}
	if record.Kind != "location" {
		return "", unreleasedTargetError(record.Kind)
	}
	decoded, err := decodeLocationSeedPayload(record.Payload)
	if err != nil {
		return "", err
	}
	payload := decoded.(locationSeedPayload)
	input := payload.writeInput()
	fingerprint, err := canonicalSHA256(payload)
	if err != nil {
		return "", err
	}
	id := platform.NewID()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.content_entry(
		id,module,status,version,idempotency_key,idempotency_fingerprint,created_by,updated_by,created_at,updated_at
	) VALUES($1,$2,'draft',1,$3,$4,$5,$5,$6,$6)`, id, content.ModuleLocations, record.SourceKey, fingerprint, actor, now); err != nil {
		return "", fmt.Errorf("insert content entry: %w", err)
	}
	for _, translation := range input.Translations {
		if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.content_translation(entry_id,locale,title,summary,body,date_label,image_alt)
			VALUES($1,$2,$3,'',$4,'','')`, id, translation.Locale, translation.Title, translation.Body); err != nil {
			return "", fmt.Errorf("insert %s translation: %w", translation.Locale, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.location_item(content_id,stable_key,map_href,sort_order) VALUES($1,$2,$3,$4)`, id, input.LocationKey, input.MapHref, input.SortOrder); err != nil {
		return "", fmt.Errorf("insert location item: %w", err)
	}
	snapshot, err := json.Marshal(content.Item{
		ID: id, Module: content.ModuleLocations, Status: content.StatusDraft, Version: 1,
		LocationKey: input.LocationKey, MapHref: input.MapHref, SortOrder: input.SortOrder, Translations: input.Translations,
		CreatedBy: actor, UpdatedBy: actor, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.content_revision(entry_id,version,snapshot_json,created_by,created_at) VALUES($1,1,$2,$3,$4)`, id, snapshot, actor, now); err != nil {
		return "", fmt.Errorf("insert seeded revision: %w", err)
	}
	return id, nil
}

func applyPageSeedRecord(ctx context.Context, tx *sql.Tx, record Record, actor string) (string, error) {
	decoded, err := decodePageSeedPayload(record.Payload)
	if err != nil {
		return "", err
	}
	payload := decoded.(pageSeedPayload)
	input := payload.writeInput()
	fingerprint, err := canonicalSHA256(payload)
	if err != nil {
		return "", err
	}
	id := platform.NewID()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.content_entry(
		id,module,status,version,idempotency_key,idempotency_fingerprint,created_by,updated_by,created_at,updated_at
	) VALUES($1,$2,'draft',1,$3,$4,$5,$5,$6,$6)`, id, content.ModulePages, record.SourceKey, fingerprint, actor, now); err != nil {
		return "", fmt.Errorf("insert content entry: %w", err)
	}
	for _, translation := range input.Translations {
		if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.content_translation(entry_id,locale,title,summary,body,date_label,image_alt,body_json)
			VALUES($1,$2,$3,$4,'','','',$5)`, id, translation.Locale, translation.Title, translation.Summary, []byte(translation.BodyJSON)); err != nil {
			return "", fmt.Errorf("insert %s translation: %w", translation.Locale, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.page_item(content_id,page_key,page_template,route_path,indexable) VALUES($1,$2,$3,$4,$5)`, id, input.PageKey, input.PageTemplate, input.RoutePath, input.Indexable); err != nil {
		return "", fmt.Errorf("insert page item: %w", err)
	}
	snapshot, err := json.Marshal(content.Item{
		ID: id, Module: content.ModulePages, Status: content.StatusDraft, Version: 1,
		PageKey: input.PageKey, PageTemplate: input.PageTemplate, RoutePath: input.RoutePath, Indexable: input.Indexable, Translations: input.Translations,
		CreatedBy: actor, UpdatedBy: actor, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.content_revision(entry_id,version,snapshot_json,created_by,created_at) VALUES($1,1,$2,$3,$4)`, id, snapshot, actor, now); err != nil {
		return "", fmt.Errorf("insert seeded revision: %w", err)
	}
	return id, nil
}

func applySiteLayoutSeedRecord(ctx context.Context, tx *sql.Tx, record Record, actor string) (string, error) {
	decoded, err := decodeSiteLayoutSeedPayload(record.Payload)
	if err != nil {
		return "", err
	}
	payload := decoded.(siteLayoutSeedPayload)
	now := time.Now().UTC().Truncate(time.Microsecond)
	links, err := json.Marshal(payload.Links)
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.site_setting_set(
		id,status,version,external_links_json,created_by,updated_by,created_at,updated_at
	) VALUES($1,'draft',1,$2,$3,$3,$4,$4)`, sitesettings.SingletonID, links, actor, now); err != nil {
		return "", fmt.Errorf("insert site setting: %w", err)
	}
	for _, locale := range payload.Locales {
		header, err := json.Marshal(locale.Header)
		if err != nil {
			return "", err
		}
		legal, err := json.Marshal(locale.Legal)
		if err != nil {
			return "", err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.site_setting_locale(
			setting_set_id,locale,site_name,english_name,copyright_holder,all_rights_reserved,seo_title_suffix,seo_description_fallback,header_items_json,legal_items_json
		) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, sitesettings.SingletonID, locale.Locale, locale.SiteName, locale.EnglishName, locale.CopyrightHolder, locale.AllRightsReserved, locale.SEOTitleSuffix, locale.SEODescriptionFallback, header, legal); err != nil {
			return "", fmt.Errorf("insert %s site locale: %w", locale.Locale, err)
		}
	}
	snapshot, err := json.Marshal(sitesettings.Settings{
		ID: sitesettings.SingletonID, Status: sitesettings.StatusDraft, Version: 1, Locales: payload.Locales, Links: payload.Links,
		CreatedBy: actor, UpdatedBy: actor, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO hhc_web.site_setting_revision(
		id,setting_set_id,revision,revision_type,snapshot_json,created_by,created_at
	) VALUES($1,$2,1,'seeded',$3,$4,$5)`, platform.NewID(), sitesettings.SingletonID, snapshot, actor, now); err != nil {
		return "", fmt.Errorf("insert seeded site setting revision: %w", err)
	}
	return sitesettings.SingletonID, nil
}

func unreleasedTargetError(kind string) error {
	switch kind {
	case "site_layout":
		return fmt.Errorf("target kind %q is not released", kind)
	default:
		return fmt.Errorf("unsupported target kind %q", kind)
	}
}

func rollbackAndFail(ctx context.Context, conn *sql.Conn, tx *sql.Tx, runID string, applyErr error) error {
	if rollbackErr := tx.Rollback(); rollbackErr != nil {
		applyErr = errors.Join(applyErr, fmt.Errorf("roll back content seed transaction: %w", rollbackErr))
	}
	return failAttempt(ctx, conn, runID, applyErr)
}

func failAttempt(ctx context.Context, conn *sql.Conn, runID string, applyErr error) error {
	_, err := conn.ExecContext(context.WithoutCancel(ctx), `UPDATE hhc_web.content_seed_run SET status='failed',finished_at=now() WHERE id=$1 AND status='started'`, runID)
	if err != nil {
		return errors.Join(applyErr, fmt.Errorf("mark content seed run failed: %w", err))
	}
	return applyErr
}

func advisoryLockID(seedVersion, manifestSHA string) int64 {
	hash := sha256.Sum256([]byte(seedVersion + "\x00" + manifestSHA))
	return int64(binary.BigEndian.Uint64(hash[:8]))
}

func randomID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate content seed ID: %w", err)
	}
	return prefix + hex.EncodeToString(value[:]), nil
}
