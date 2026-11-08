package migrations

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestOnlineBulletinSchemaPreservesEditionAndImmutableHistory(t *testing.T) {
	if _, err := files.ReadFile("sql/069_online_bulletins.sql"); err != nil {
		t.Fatal(err)
	}
	if _, err := files.ReadFile("sql/074_bulletin_reader_private_data.sql"); err != nil {
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
	if err = Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(query, args...); err != nil {
			t.Fatal(err)
		}
	}
	reject := func(query string, args ...any) {
		t.Helper()
		exec("SAVEPOINT reject")
		if _, err := tx.Exec(query, args...); err == nil {
			t.Fatalf("constraint did not reject: %s", query)
		}
		exec("ROLLBACK TO SAVEPOINT reject")
	}
	issue := "b06f90ad-9e5f-4faa-b683-1c1347b2680b"
	general := "b2d67726-3c3d-43ca-b8dc-555e4475bb01"
	children := "b2d67726-3c3d-43ca-b8dc-555e4475bb02"
	exec(`INSERT INTO hhc_web.bulletin_issue(id,issue_date,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1::uuid,'2099-12-30',$1::text,'test','test',now(),now())`, issue)
	for _, series := range []string{"general", "children"} {
		exec(`INSERT INTO hhc_web.bulletin_version(id,issue_id,series,locale,title,pdf_asset_id,pdf_file_name,created_by,updated_by,created_at,updated_at) VALUES(gen_random_uuid(),$1,$2,'zh-Hant','test','source','source.pdf','test','test',now(),now())`, issue, series)
	}
	for i, doc := range []string{general, children} {
		series := []string{"general", "children"}[i]
		exec(`INSERT INTO hhc_web.online_bulletin_document(id,issue_id,series,content_locale) VALUES($1,$2,$3,'zh-Hant')`, doc, issue, series)
	}
	reject(`INSERT INTO hhc_web.online_bulletin_document(id,issue_id,series,content_locale) VALUES(gen_random_uuid(),$1,'general','zh-Hant')`, issue)
	checksum := strings.Repeat("a", 64)
	insertRevision := `INSERT INTO hhc_web.online_bulletin_revision(document_id,issue_id,series,content_locale,revision,document_json,source_asset_checksum,template_version,extractor_version,content_hash,renderer_version,renderer_artifact_sha256,created_by) VALUES($1,$2,$3,'zh-Hant',$4,'{}',$5,'v1','poppler-25.03.0',$5,'v1',$5,'test')`
	exec(insertRevision, general, issue, "general", 1, checksum)
	exec(insertRevision, children, issue, "children", 1, checksum)
	reject(insertRevision, general, issue, "children", 2, checksum)
	reject(insertRevision, general, issue, "general", 1, checksum)
	reject(`UPDATE hhc_web.online_bulletin_revision SET document_json='{"tampered":true}' WHERE document_id=$1`, general)
	exec(`UPDATE hhc_web.online_bulletin_document SET local_revision=1,base_revision=1 WHERE id=$1`, general)
	reject(`UPDATE hhc_web.online_bulletin_document SET incoming_revision=2 WHERE id=$1`, general)
	reject(`DELETE FROM hhc_web.bulletin_version WHERE issue_id=$1 AND series='general'`, issue)
	reject(`DELETE FROM hhc_web.bulletin_issue WHERE id=$1`, issue)
	insertJob := `INSERT INTO hhc_web.online_bulletin_extraction_job(id,document_id,issue_id,series,content_locale,source_asset_id,source_asset_checksum,template_version,extractor_version) VALUES(gen_random_uuid(),$1,$2,$3,'zh-Hant','source',$4,'v1',$5)`
	exec(insertJob, general, issue, "general", checksum, "poppler-25.03.0")
	exec(insertJob, children, issue, "children", checksum, "poppler-25.03.0")
	reject(insertJob, general, issue, "general", checksum, "poppler-25.03.0")
	exec(insertJob, general, issue, "general", checksum, "poppler-26.05.0")
	reject(`UPDATE hhc_web.online_bulletin_extraction_job SET status='running' WHERE document_id=$1`, general)
	reject(`UPDATE hhc_web.online_bulletin_document SET published_revision=1 WHERE id=$1`, general)
	exec(`INSERT INTO hhc_web.online_bulletin_publication(document_id,revision,canonical_metadata,created_by) VALUES($1,1,'{}','test')`, general)
	exec(`UPDATE hhc_web.online_bulletin_document SET published_revision=1 WHERE id=$1`, general)
	reject(`INSERT INTO hhc_web.online_bulletin_mapping(document_id,from_revision,to_revision,from_sentence_id,status,to_sentence_ids) VALUES($1,1,2,'sentence','unchanged','["target"]')`, general)
	exec(insertRevision, general, issue, "general", 2, checksum)
	exec(`INSERT INTO hhc_web.online_bulletin_publication(document_id,revision,canonical_metadata,created_by) VALUES($1,2,'{}','test')`, general)
	for _, status := range []string{"unchanged", "moved", "split", "merged", "removed", "requires_review"} {
		targets := `["target"]`
		if status == "removed" {
			targets = `[]`
		}
		if status == "split" {
			targets = `["target","second"]`
		}
		exec(`INSERT INTO hhc_web.online_bulletin_mapping(document_id,from_revision,to_revision,from_sentence_id,status,to_sentence_ids) VALUES($1,1,2,$2,$2,$3::jsonb)`, general, status, targets)
	}
	reject(`UPDATE hhc_web.online_bulletin_mapping SET status='removed' WHERE document_id=$1`, general)
	reject(`UPDATE hhc_web.online_bulletin_document SET published_revision=2 WHERE id=$1`, general)
	account := "4d2ac87a-b34d-4449-ad2e-aac7938e5fb5"
	other := "4d2ac87a-b34d-4449-ad2e-aac7938e5fb6"
	for _, owner := range []string{account, other} {
		exec(`INSERT INTO hhc_web.bulletin_reader_migration_state(account_id,document_id,applied_revision) VALUES($1,$2,1)`, owner, general)
	}
	insertHighlight := `INSERT INTO hhc_web.bulletin_reader_highlight(account_id,document_id,sentence_id,color,quote,active,version,updated_at) VALUES($1,$2,'sentence',$3,'quote',true,1,now())`
	exec(insertHighlight, account, general, "yellow")
	exec(insertHighlight, other, general, "blue")
	reject(insertHighlight, account, general, "red")
	reject(insertHighlight, account, children, "red")
	reject(`UPDATE hhc_web.bulletin_reader_highlight SET color='green' WHERE account_id=$1`, account)
	insertBookmark := `INSERT INTO hhc_web.bulletin_reader_bookmark(account_id,document_id,id,name,sentence_id,scalar_offset,page_id,document_revision,version,created_at,updated_at) VALUES($1,$2,gen_random_uuid(),$3,'sentence',0,'page',1,1,now(),now())`
	exec(insertBookmark, account, general, strings.Repeat("😀", 80))
	reject(insertBookmark, account, children, "foreign parent")
	reject(insertBookmark, account, general, strings.Repeat("😀", 81))
	reject(`UPDATE hhc_web.bulletin_reader_bookmark SET scalar_offset=-1 WHERE account_id=$1`, account)
	reject(`UPDATE hhc_web.bulletin_reader_bookmark SET deleted=true WHERE account_id=$1`, account)
	insertNote := `INSERT INTO hhc_web.bulletin_reader_note(account_id,document_id,id,body,sentence_ids,quote,version,created_at,updated_at) VALUES($1,$2,gen_random_uuid(),$3,'["sentence"]','quote',1,now(),now())`
	exec(insertNote, account, general, "私人筆記")
	exec(insertNote, account, general, "另一份筆記")
	reject(insertNote, account, general, strings.Repeat("字", 10001))
	reject(`UPDATE hhc_web.bulletin_reader_note SET sentence_ids=(SELECT jsonb_agg('sentence'::text) FROM generate_series(1,501)) WHERE account_id=$1`, account)
	exec(`INSERT INTO hhc_web.bulletin_reader_progress(account_id,document_id,page_id,component_id,sentence_id,recorded_at,updated_at) VALUES($1,$2,'page','body','sentence',now(),now())`, account, general)
	exec(`INSERT INTO hhc_web.bulletin_reader_processed_mutation(account_id,mutation_id,document_id,request_hash,result_json,created_at,expires_at) VALUES($1,gen_random_uuid(),$2,$3,'{}',now(),now()+interval '2184 hours')`, account, general, checksum)
	reject(`UPDATE hhc_web.bulletin_reader_processed_mutation SET expires_at=created_at+interval '90 days' WHERE account_id=$1`, account)
	var constraints int
	if err := tx.QueryRow(`SELECT count(*) FROM pg_constraint WHERE contype='f' AND conrelid::regclass::text LIKE 'hhc_web.bulletin_reader_%' AND confrelid::regclass::text NOT LIKE 'hhc_web.%'`).Scan(&constraints); err != nil || constraints != 0 {
		t.Fatal("cross-database account FK", constraints, err)
	}
	var expiryIndex bool
	if err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname='hhc_web' AND tablename='bulletin_reader_processed_mutation' AND indexdef LIKE '%(expires_at)%')`).Scan(&expiryIndex); err != nil || !expiryIndex {
		t.Fatal("missing bounded-prune index", err)
	}
	reject(`UPDATE hhc_web.bulletin_reader_migration_state SET applied_revision=99 WHERE account_id=$1`, account)
	reject(`DELETE FROM hhc_web.online_bulletin_publication WHERE document_id=$1 AND revision=1`, general)
}
