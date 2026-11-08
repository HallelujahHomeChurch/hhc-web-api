package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

func dismissalStatement(t *testing.T, db *sql.DB) content.Item {
	t.Helper()
	now := time.Date(2091, 1, 1, 0, 0, 0, 0, time.UTC)
	end := now.Add(time.Hour)
	item, err := New(db).CreateContent(context.Background(), content.ModuleNews, content.WriteInput{Kind: content.KindStatement, Slug: "dismissal-" + platform.NewID(), DisplayDate: "2026-10-09", PopupStartsAt: &now, PopupEndsAt: &end, Translations: []content.Translation{{Locale: "zh-Hant", Title: "Statement", Body: "Published body"}}}, "actor", platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM hhc_web.public_projection WHERE resource_id=$1`, item.ID)
		db.Exec(`DELETE FROM hhc_web.content_entry WHERE id=$1`, item.ID)
	})
	payload, err := json.Marshal(content.PublicItem{ID: item.ID, Kind: content.KindStatement, Title: "Published body", Body: "Published body", PopupStartsAt: &now, PopupEndsAt: &end, Href: "/zh-Hant/statements/" + item.Slug})
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO hhc_web.public_projection(projection_key,resource_type,resource_id,locale,route_path,version,etag,payload_json,updated_at) VALUES($1,'news',$2,'zh-Hant',$3,7,'dismissal-test',$4,$5)`, "news:zh-Hant:"+item.ID, item.ID, "/zh-Hant/statements/"+item.Slug, payload, now)
	if err != nil {
		t.Fatal(err)
	}
	return item
}

func TestActiveStatementUsesPublishedProjectionVersion(t *testing.T) {
	db := accessDB(t)
	item := dismissalStatement(t, db)
	ctx := context.Background()
	for _, status := range []string{"draft", "publishing", "published"} {
		if _, err := db.Exec(`UPDATE hhc_web.content_entry SET version=9,status=$2 WHERE id=$1`, item.ID, status); err != nil {
			t.Fatal(err)
		}
		got, err := New(db).ActiveStatement(ctx, "ja", *item.PopupStartsAt)
		if err != nil || got.Statement == nil || got.Statement.PublishedVersion != 7 || got.Statement.Body != "Published body" {
			t.Fatalf("status=%s statement=%+v err=%v", status, got.Statement, err)
		}
	}
	if _, err := db.Exec(`UPDATE hhc_web.public_projection SET version=10 WHERE resource_id=$1`, item.ID); err != nil {
		t.Fatal(err)
	}
	got, err := New(db).ActiveStatement(ctx, "zh-Hant", *item.PopupStartsAt)
	if err != nil || got.Statement == nil || got.Statement.PublishedVersion != 10 {
		t.Fatalf("published=%+v err=%v", got.Statement, err)
	}
}

func TestActiveStatementLegacyPayloadGetsRowVersion(t *testing.T) {
	db := accessDB(t)
	item := dismissalStatement(t, db)
	for _, locale := range []string{"zh-Hant", "en", "ja"} {
		got, _, err := New(db).PublicNews(context.Background(), locale, item.Slug)
		if err != nil || got.PublishedVersion != 7 {
			t.Fatalf("locale=%s version=%d err=%v", locale, got.PublishedVersion, err)
		}
	}
}

func TestStatementDismissalOwnerVersionAndCleanup(t *testing.T) {
	db := accessDB(t)
	item := dismissalStatement(t, db)
	r := New(db)
	ctx := context.Background()
	owner, other := platform.NewID(), platform.NewID()
	ref := content.StatementRef{StatementID: item.ID, PublishedVersion: 7}
	for i := 0; i < 2; i++ {
		got, err := r.DismissStatement(ctx, owner, ref)
		if err != nil || !got.Dismissed {
			t.Fatalf("dismiss=%+v err=%v", got, err)
		}
	}
	for _, subject := range []string{owner, other} {
		got, err := r.GetStatementDismissal(ctx, subject, ref)
		if err != nil || got.Dismissed != (subject == owner) {
			t.Fatalf("owner=%s got=%+v err=%v", subject, got, err)
		}
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.statement_dismissal WHERE statement_id=$1`, item.ID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("count=%d err=%v", count, err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.public_projection SET version=8 WHERE resource_id=$1`, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.DismissStatement(ctx, owner, ref); !errors.Is(err, content.ErrStatementVersionChanged) {
		t.Fatalf("stale PUT err=%v", err)
	}
	if _, err := r.GetStatementDismissal(ctx, owner, ref); !errors.Is(err, content.ErrStatementVersionChanged) {
		t.Fatalf("stale GET err=%v", err)
	}
	ref.PublishedVersion = 8
	if got, err := r.GetStatementDismissal(ctx, owner, ref); err != nil || got.Dismissed {
		t.Fatalf("new version=%+v err=%v", got, err)
	}
	if _, err := r.DismissStatement(ctx, other, ref); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.CleanupWatermarkAccount(ctx, owner, "dismissal-cleanup:"+owner); err != nil {
		t.Fatal(err)
	}
	if _, err := r.DismissStatement(ctx, owner, ref); !errors.Is(err, content.ErrStatementDismissalRestricted) {
		t.Fatalf("recreated erased preference: %v", err)
	}
	got, err := r.GetStatementDismissal(ctx, other, ref)
	if err != nil || !got.Dismissed {
		t.Fatalf("other cleanup=%+v %v", got, err)
	}
}

func TestStatementDismissalPublicationRace(t *testing.T) {
	db := accessDB(t)
	item := dismissalStatement(t, db)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE hhc_web.content_entry SET version=8 WHERE id=$1`, item.ID); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := New(db).DismissStatement(ctx, platform.NewID(), content.StatementRef{StatementID: item.ID, PublishedVersion: 7})
		done <- err
	}()
	waitStatementBlocked(t, db, ctx)
	if _, err = tx.ExecContext(ctx, `DELETE FROM hhc_web.public_projection WHERE resource_id=$1`, item.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hhc_web.public_projection(projection_key,resource_type,resource_id,locale,route_path,version,etag,payload_json,updated_at) VALUES($1,'news',$2,'zh-Hant',$3,8,'v8','{"kind":"statement"}',now())`, "news:zh-Hant:"+item.ID, item.ID, "/zh-Hant/statements/"+item.Slug); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, content.ErrStatementVersionChanged) {
		t.Fatalf("publication race=%v", err)
	}
}

func waitStatementBlocked(t *testing.T, db *sql.DB, ctx context.Context) {
	t.Helper()
	for {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND cardinality(pg_blocking_pids(pid))>0 AND pid<>pg_backend_pid()`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("statement did not wait on transaction")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestStatementDismissalCleanupRaceAndPagination(t *testing.T) {
	db := accessDB(t)
	item := dismissalStatement(t, db)
	owner := platform.NewID()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r := New(db)
	for _, version := range []int64{7, 8, 9} {
		if _, err := db.Exec(`INSERT INTO hhc_web.statement_dismissal(account_id,statement_id,published_version) VALUES($1,$2,$3)`, owner, item.ID, version); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err = lockWatermarkSubject(ctx, tx, owner); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := r.DismissStatement(ctx, owner, content.StatementRef{StatementID: item.ID, PublishedVersion: 7})
		done <- err
	}()
	waitStatementBlocked(t, db, ctx)
	if _, _, err = cleanupWatermarkAccount(ctx, tx, owner, "concurrent-dismissal:"+owner, watermarkCleanupSubjectRef(owner)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err = <-done; !errors.Is(err, content.ErrStatementDismissalRestricted) {
		t.Fatalf("cleanup race=%v", err)
	}
	var n int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.statement_dismissal WHERE account_id=$1`, owner).Scan(&n); err != nil || n != 0 {
		t.Fatalf("erased count=%d err=%v", n, err)
	}
	other := platform.NewID()
	if _, err = r.DismissStatement(ctx, other, content.StatementRef{StatementID: item.ID, PublishedVersion: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM hhc_web.content_entry WHERE id=$1`, item.ID); err != nil {
		t.Fatal(err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.statement_dismissal WHERE statement_id=$1`, item.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("cascade count=%d err=%v", n, err)
	}
}
