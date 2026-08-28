package contentseed

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
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
	return apply(ctx, db, manifest, manifestSHA, actor, defaultApplyPlan, applyRecord)
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

func unreleasedTargetError(kind string) error {
	switch kind {
	case "location", "site_layout", "page":
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
