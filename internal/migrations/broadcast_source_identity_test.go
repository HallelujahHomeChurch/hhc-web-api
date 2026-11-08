package migrations

import (
	"database/sql"
	"os"
	"strings"
	"testing"
)

func TestNamedSourceMigrationPreservesExistingKeyAndTarget(t *testing.T) {
	dsn := os.Getenv("HHW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HHW_TEST_DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(q string) {
		t.Helper()
		if _, err := tx.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TEMP TABLE member_recording(id uuid PRIMARY KEY);
 CREATE TEMP TABLE member_recording_broadcast(recording_id uuid PRIMARY KEY,source_managed boolean NOT NULL);
 INSERT INTO member_recording VALUES('018f0000-0000-4000-8000-000000000001');
 INSERT INTO member_recording_broadcast VALUES('018f0000-0000-4000-8000-000000000001',true);`)
	old, err := files.ReadFile("sql/091_broadcast_source.sql")
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(string(old), "CREATE TABLE hhc_web.member_recording_broadcast_source (")
	end := strings.Index(string(old), "-- No cascade:")
	if start < 0 || end < start {
		t.Fatal("legacy source DDL not found")
	}
	exec(strings.ReplaceAll(string(old)[start:end], "hhc_web.", "pg_temp."))
	seed, err := files.ReadFile("sql/092_broadcast_ingest_signing.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(strings.ReplaceAll(string(seed), "hhc_web.", "pg_temp."))
	exec(`UPDATE pg_temp.member_recording_broadcast_source SET revision=11,key_hash=repeat('a',64),suffix='AAAA',target_recording_id='018f0000-0000-4000-8000-000000000001'`)
	var before string
	if err := tx.QueryRow(`SELECT ingest_signing_secret FROM pg_temp.member_recording_broadcast_source WHERE singleton`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	migration, err := files.ReadFile("sql/093_broadcast_source_identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	exec(strings.ReplaceAll(string(migration), "hhc_web.", "pg_temp."))
	var preserved bool
	if err := tx.QueryRow(`SELECT s.revision=11 AND s.key_hash=repeat('a',64) AND s.suffix='AAAA'
 AND s.ingest_signing_secret=$1 AND s.target_recording_id=b.recording_id AND s.id=b.source_id
 FROM pg_temp.member_recording_broadcast_source s JOIN pg_temp.member_recording_broadcast b ON b.source_id=s.id WHERE s.singleton`, before).Scan(&preserved); err != nil || !preserved {
		t.Fatal("source identity migration changed legacy state", err)
	}
	exec(`INSERT INTO pg_temp.member_recording_broadcast_source(singleton,name) VALUES(NULL,'Second source')`)
	var count int
	if err := tx.QueryRow(`SELECT count(*) FROM pg_temp.member_recording_broadcast_source WHERE singleton`).Scan(&count); err != nil || count != 1 {
		t.Fatal("legacy adapter selector changed", err)
	}
}
