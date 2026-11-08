package migrations

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Exercise the historical upgrade through the unchanged production runner.
//
//go:embed sql/0*.sql sql/100_broadcast_stream_key_storage.sql
var preDSRContractFiles embed.FS

func dsrContractDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("HHW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HHW_TEST_DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	var name string
	if err := db.QueryRow(`SELECT current_database()`).Scan(&name); err != nil || name != "hhc_web_test" {
		t.Fatalf("retirement fixtures require disposable hhc_web_test: %q %v", name, err)
	}
	reset := `DROP VIEW IF EXISTS public.unexpected_watermark_view; DROP FUNCTION IF EXISTS public.unexpected_watermark_reader(); DROP SCHEMA IF EXISTS hhc_web CASCADE; DROP TABLE IF EXISTS public.schema_migrations`
	dsrContractExec(t, db, reset)
	t.Cleanup(func() { _, _ = db.Exec(reset) })
	return db
}

func dsrContractExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func dsrContractHistorical(t *testing.T, db *sql.DB) {
	t.Helper()
	current := files
	files = preDSRContractFiles
	err := Run(t.Context(), db)
	files = current
	if err != nil {
		t.Fatal(err)
	}
}

func dsrContractHash(t *testing.T, db *sql.DB, query string) string {
	t.Helper()
	var hash string
	if err := db.QueryRow(query).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	return hash
}

func dsrContractDataHash(t *testing.T, db *sql.DB, table string, fields string) string {
	t.Helper()
	return dsrContractHash(t, db, fmt.Sprintf(`SELECT encode(sha256(convert_to(COALESCE(string_agg(to_jsonb(row)::text,E'\n' ORDER BY to_jsonb(row)::text),''),'UTF8')),'hex') FROM (SELECT %s FROM hhc_web.%s) row`, fields, table))
}

func dsrContractSeedPreserved(t *testing.T, db *sql.DB) {
	t.Helper()
	dsrContractExec(t, db, `INSERT INTO hhc_web.bulletin_watermark_subject(subject_hash,erased,updated_at)
 SELECT 'preserved-'||n,true,'2026-10-01T00:00:00Z'::timestamptz FROM generate_series(1,5) n;
 INSERT INTO hhc_web.bulletin_watermark_dsr_action(key_hash,subject_hash,action,record_count,created_at,request_id)
 SELECT 'erase-'||n,'preserved-'||n,'erase',n,'2026-10-01T00:00:00Z',('10000000-0000-4000-8000-'||lpad(n::text,12,'0'))::uuid FROM generate_series(1,2) n;
 INSERT INTO hhc_web.account_cleanup_operation(idempotency_key,subject_ref,record_count,remaining_count,created_at,updated_at)
 VALUES('ordinary-cleanup','ordinary-subject',9,0,'2026-10-01T00:00:00Z','2026-10-01T00:00:00Z');
 INSERT INTO hhc_web.cms_audit_event(id,action,resource_type,resource_id,actor,payload_json,created_at)
 VALUES('10000000-0000-4000-8000-000000000005','ordinary.audit','content','10000000-0000-4000-8000-000000000006','ordinary-actor','{"preserved":true}','2026-10-01T00:00:00Z')`)
}

func dsrContractAbsent(t *testing.T, db *sql.DB) {
	t.Helper()
	var absent bool
	err := db.QueryRow(`SELECT to_regclass('hhc_web.bulletin_watermark_restriction_withdrawal') IS NULL
 AND to_regprocedure('hhc_web.invalidate_watermark_restriction_origin()') IS NULL
 AND NOT EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='hhc_web.bulletin_watermark_subject'::regclass AND attname='origin_tracking_complete' AND NOT attisdropped)
 AND NOT EXISTS(SELECT 1 FROM pg_trigger WHERE tgrelid='hhc_web.bulletin_watermark_dsr_action'::regclass AND tgname IN ('watermark_restriction_origin_insert','watermark_restriction_origin_delete'))
 AND EXISTS(SELECT 1 FROM public.schema_migrations WHERE version='sql/101_retire_legacy_dsr_schema.sql')`).Scan(&absent)
	if err != nil || !absent {
		t.Fatalf("retired DSR objects absent=%t: %v", absent, err)
	}
}

func TestRetiredDSRSchemaFreshBootstrapAndRepeat(t *testing.T) {
	db := dsrContractDB(t)
	for range 2 {
		if err := Run(t.Context(), db); err != nil {
			t.Fatal(err)
		}
		dsrContractAbsent(t, db)
	}
}

func TestRetiredDSRSchemaUpgradePreservesErasureAndOrdinaryCleanup(t *testing.T) {
	db := dsrContractDB(t)
	dsrContractHistorical(t, db)
	dsrContractSeedPreserved(t, db)
	tables := map[string]string{
		"bulletin_watermark_subject":    "subject_hash,erased,updated_at",
		"bulletin_watermark_dsr_action": "*",
		"account_cleanup_operation":     "*",
		"cms_audit_event":               "*",
	}
	before := map[string]string{}
	for table, fields := range tables {
		before[table] = dsrContractDataHash(t, db, table, fields)
	}
	// Only the legacy triggers change on the mixed receipt table; all columns,
	// defaults, constraints and indexes must survive byte-for-byte.
	schemaQuery := `WITH targets AS (SELECT 'hhc_web.bulletin_watermark_dsr_action'::regclass AS oid), objects AS (
 SELECT 'column:'||a.attname||':'||format_type(a.atttypid,a.atttypmod)||':'||a.attnotnull::text||':'||COALESCE(pg_get_expr(d.adbin,d.adrelid),'') value FROM targets t JOIN pg_attribute a ON a.attrelid=t.oid AND a.attnum>0 AND NOT a.attisdropped LEFT JOIN pg_attrdef d ON d.adrelid=t.oid AND d.adnum=a.attnum
 UNION ALL SELECT 'constraint:'||c.conname||':'||pg_get_constraintdef(c.oid) FROM targets t JOIN pg_constraint c ON c.conrelid=t.oid
 UNION ALL SELECT 'index:'||pg_get_indexdef(i.indexrelid) FROM targets t JOIN pg_index i ON i.indrelid=t.oid)
 SELECT encode(sha256(convert_to(string_agg(value,E'\n' ORDER BY value),'UTF8')),'hex') FROM objects`
	schemaBefore := dsrContractHash(t, db, schemaQuery)
	if err := Run(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	dsrContractAbsent(t, db)
	for table, fields := range tables {
		if got := dsrContractDataHash(t, db, table, fields); got != before[table] {
			t.Fatalf("preserved %s data changed", table)
		}
	}
	if dsrContractHash(t, db, schemaQuery) != schemaBefore {
		t.Fatal("mixed receipt metadata/indexes changed")
	}
}

func TestRetiredDSRSchemaRejectsDriftAndRollsBack(t *testing.T) {
	for _, test := range []struct{ name, sql string }{
		{"nonerased fence", `INSERT INTO hhc_web.bulletin_watermark_subject(subject_hash,erased) VALUES('unexpected',false)`},
		{"restriction receipt", `INSERT INTO hhc_web.bulletin_watermark_dsr_action(key_hash,subject_hash,action,record_count) VALUES('unexpected','unexpected','restrict_processing',1)`},
		{"withdrawn restriction receipt", `INSERT INTO hhc_web.bulletin_watermark_dsr_action(key_hash,subject_hash,action,record_count,request_id,withdrawn_at) VALUES('unexpected','unexpected','restrict_processing',1,'10000000-0000-4000-8000-000000000003',now())`},
		{"withdrawal", `INSERT INTO hhc_web.bulletin_watermark_restriction_withdrawal VALUES('unexpected','fingerprint','10000000-0000-4000-8000-000000000003','10000000-0000-4000-8000-000000000004','unexpected','{}',now())`},
		{"extra index", `CREATE INDEX unexpected_origin_idx ON hhc_web.bulletin_watermark_subject(origin_tracking_complete)`},
		{"column constraint", `ALTER TABLE hhc_web.bulletin_watermark_subject ADD CONSTRAINT unexpected_origin_check CHECK(origin_tracking_complete OR erased)`},
		{"extended statistics", `CREATE STATISTICS unexpected_origin_stats ON origin_tracking_complete,erased FROM hhc_web.bulletin_watermark_subject`},
		{"expression statistics", `CREATE STATISTICS unexpected_origin_stats ON (NOT origin_tracking_complete),erased FROM hhc_web.bulletin_watermark_subject`},
		{"withdrawal statistics", `CREATE STATISTICS unexpected_withdrawal_stats ON subject_hash,request_id FROM hhc_web.bulletin_watermark_restriction_withdrawal`},
		{"view", `CREATE VIEW public.unexpected_watermark_view AS SELECT origin_tracking_complete FROM hhc_web.bulletin_watermark_subject`},
		{"disabled trigger", `ALTER TABLE hhc_web.bulletin_watermark_dsr_action DISABLE TRIGGER watermark_restriction_origin_insert`},
		{"changed function", `CREATE OR REPLACE FUNCTION hhc_web.invalidate_watermark_restriction_origin() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN RETURN NEW; END$$`},
		{"changed function attributes", `ALTER FUNCTION hhc_web.invalidate_watermark_restriction_origin() SECURITY DEFINER`},
		{"overload", `CREATE FUNCTION hhc_web.invalidate_watermark_restriction_origin(uuid) RETURNS bigint LANGUAGE plpgsql AS $$BEGIN RETURN (SELECT count(*) FROM hhc_web.bulletin_watermark_restriction_withdrawal); END$$`},
		{"uppercase reader", `CREATE FUNCTION public.unexpected_watermark_reader() RETURNS bigint LANGUAGE plpgsql AS $$BEGIN RETURN (SELECT count(*) FROM HHC_WEB.BULLETIN_WATERMARK_RESTRICTION_WITHDRAWAL); END$$`},
		{"column reader", `CREATE FUNCTION public.unexpected_watermark_reader() RETURNS boolean LANGUAGE plpgsql AS $$BEGIN RETURN (SELECT ORIGIN_TRACKING_COMPLETE FROM hhc_web.bulletin_watermark_subject LIMIT 1); END$$`},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := dsrContractDB(t)
			dsrContractHistorical(t, db)
			dsrContractSeedPreserved(t, db)
			dsrContractExec(t, db, test.sql)
			fields := map[string]string{"bulletin_watermark_dsr_action": "*", "bulletin_watermark_subject": "subject_hash,erased,updated_at", "account_cleanup_operation": "*", "cms_audit_event": "*"}
			before := map[string]string{}
			for table, columns := range fields {
				before[table] = dsrContractDataHash(t, db, table, columns)
			}
			statisticsQuery := `SELECT COALESCE(string_agg(pg_get_statisticsobjdef(oid),E'\n' ORDER BY oid),'') FROM pg_statistic_ext WHERE stxrelid IN ('hhc_web.bulletin_watermark_subject'::regclass,'hhc_web.bulletin_watermark_restriction_withdrawal'::regclass)`
			statisticsBefore := dsrContractHash(t, db, statisticsQuery)
			if err := Run(t.Context(), db); err == nil {
				t.Fatal("unexpected drift accepted")
			}
			var rolledBack bool
			err := db.QueryRow(`SELECT to_regclass('hhc_web.bulletin_watermark_restriction_withdrawal') IS NOT NULL
 AND to_regprocedure('hhc_web.invalidate_watermark_restriction_origin()') IS NOT NULL
 AND EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='hhc_web.bulletin_watermark_subject'::regclass AND attname='origin_tracking_complete' AND NOT attisdropped)
 AND NOT EXISTS(SELECT 1 FROM public.schema_migrations WHERE version='sql/101_retire_legacy_dsr_schema.sql')`).Scan(&rolledBack)
			if err != nil || !rolledBack {
				t.Fatalf("drift rollback/preservation=%t: %v", rolledBack, err)
			}
			for table, columns := range fields {
				if dsrContractDataHash(t, db, table, columns) != before[table] {
					t.Fatalf("preserved %s changed after refused contraction", table)
				}
			}
			if dsrContractHash(t, db, statisticsQuery) != statisticsBefore {
				t.Fatal("unreviewed statistics changed after refused contraction")
			}
		})
	}
}

func TestRetiredDSRSchemaBoundsLockWait(t *testing.T) {
	db := dsrContractDB(t)
	dsrContractHistorical(t, db)
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`LOCK TABLE hhc_web.bulletin_watermark_subject IN ACCESS SHARE MODE`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	start := time.Now()
	err = Run(ctx, db)
	if err == nil || !strings.Contains(err.Error(), "lock timeout") || time.Since(start) > 6*time.Second {
		t.Fatalf("lock bound error=%v duration=%s", err, time.Since(start))
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := Run(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	dsrContractAbsent(t, db)
}
