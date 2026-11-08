package migrations

import (
	"database/sql"
	"os"
	"strings"
	"testing"
)

func TestRenumberedBulletinSettingsMigrationPreservesAppliedSettings(t *testing.T) {
	data, err := files.ReadFile("sql/088_bulletin_template_settings.sql")
	if err != nil {
		t.Fatal(err)
	}
	dsn := os.Getenv("HHW_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HHW_TEST_DATABASE_URL is not configured")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, applied := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "previously-applied"}[applied], func(t *testing.T) {
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			exec := func(query string) {
				t.Helper()
				if _, err := tx.Exec(query); err != nil {
					t.Fatal(err)
				}
			}
			exec(`CREATE TEMP TABLE schema_migrations(version text PRIMARY KEY); CREATE TEMP TABLE online_bulletin_extraction_job(id int)`)
			// Execute the real migration in a transaction-local namespace only.
			script := strings.ReplaceAll(strings.ReplaceAll(string(data), "hhc_web.", "pg_temp."), "public.schema_migrations", "pg_temp.schema_migrations")
			exec(script)
			want := int64(1)
			if applied {
				exec(`UPDATE pg_temp.bulletin_template_settings SET version=27; INSERT INTO pg_temp.schema_migrations(version) VALUES('sql/087_bulletin_template_settings.sql')`)
				exec(script)
				want = 27
			}
			var version int64
			var mission string
			if err := tx.QueryRow(`SELECT version,text_json->>'visionMission' FROM pg_temp.bulletin_template_settings`).Scan(&version, &mission); err != nil || version != want || mission != "遍地華人興起、福音傳到地極" {
				t.Fatalf("settings changed: version=%d mission=%s err=%v", version, mission, err)
			}
			var count int
			if err := tx.QueryRow(`SELECT count(*) FROM pg_temp.bulletin_template_settings_revision`).Scan(&count); err != nil || count != 1 {
				t.Fatal("immutable history overwritten", count, err)
			}
		})
	}
}
