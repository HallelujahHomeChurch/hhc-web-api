package postgres

import (
	"context"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"strings"
	"testing"
	"time"
)

func TestRecordingKeysetPageIgnoresNewHeadAndUnpublishedItems(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	at := now.Add(-time.Hour)
	ids := []string{"00000000-0000-4000-8000-000000000003", "00000000-0000-4000-8000-000000000002", "00000000-0000-4000-8000-000000000001"}
	insert := func(id string, upload time.Time, pkg string) {
		t.Helper()
		_, err := db.Exec(`INSERT INTO hhc_web.member_recording(id,title_override,create_title,uploaded_at,expires_at,draft_expires_at,status,package_id,ready_at,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1,'Video','Video',$2,$2::timestamptz+interval '720 hours',$2,'published',$3,$2,$4,'test','test',$2,$2)`, id, upload, pkg, platform.NewID())
		if err != nil {
			t.Fatal(err)
		}
		recordingCleanup(t, db, id)
	}
	insert(ids[0], at, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	insert(ids[1], at, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	insert(ids[2], at.Add(-time.Minute), "cccccccccccccccccccccccccccccccc")
	page, err := repo.ListVisiblePage(ctx, now, nil, "", 1, "")
	if err != nil || len(page) != 1 || page[0].ID != ids[0] {
		t.Fatalf("first page=%+v %v", page, err)
	}
	head := platform.NewID()
	insert(head, now.Add(-time.Minute), "dddddddddddddddddddddddddddddddd")
	page, err = repo.ListVisiblePage(ctx, now, &at, ids[0], 2, "")
	if err != nil || len(page) != 2 || page[0].ID != ids[1] || page[1].ID != ids[2] {
		t.Fatalf("tie continuation=%+v %v", page, err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.member_recording SET status='draft' WHERE id=$1`, ids[1]); err != nil {
		t.Fatal(err)
	}
	page, err = repo.ListVisiblePage(ctx, now, &at, ids[0], 2, "")
	if err != nil || len(page) != 1 || page[0].ID != ids[2] {
		t.Fatalf("resumed page=%+v %v", page, err)
	}
}

func TestRecordingSearchFiltersBeforeLimitAndEscapesLiteralWildcards(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	insert := func(title, description, status string, upload, expires time.Time) string {
		t.Helper()
		id := platform.NewID()
		_, err := db.Exec(`INSERT INTO hhc_web.member_recording(id,title_override,create_title,description,uploaded_at,expires_at,draft_expires_at,status,package_id,ready_at,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1,$2,$2,$3,$4,$5,$5,$6,$8,$4,$7,'test','test',$4,$4)`, id, title, description, upload, expires, status, platform.NewID(), strings.ReplaceAll(id, "-", ""))
		if err != nil {
			t.Fatal(err)
		}
		recordingCleanup(t, db, id)
		return id
	}
	for i := 0; i < 13; i++ {
		insert(fmt.Sprintf("Other %d", i), "", "published", now.Add(-time.Duration(i)*time.Minute), now.Add(time.Hour))
	}
	wanted := insert("主日聚會 100%", "FAITH a_b literal \\ path", "published", now.Add(-time.Hour), now.Add(time.Hour))
	insert("主日聚會 100%", "FAITH a_b", "draft", now, now.Add(time.Hour))
	insert("主日聚會 100%", "FAITH a_b", "published", now.Add(-2*time.Hour), now.Add(-time.Minute))
	for _, tc := range []struct {
		q    string
		want bool
	}{
		{"faith 主日", true}, {"100% a_b", true}, {`\`, true}, {"missing", false}, {"1000", false}, {"axb", false}, {"%_' OR 1=1 --", false},
	} {
		q, err := recordings.NormalizeSearch(tc.q)
		if err != nil {
			t.Fatal(err)
		}
		page, err := repo.ListVisiblePage(ctx, now, nil, "", 1, q)
		if err != nil || (len(page) == 1) != tc.want || tc.want && page[0].ID != wanted {
			t.Fatalf("query %q got %+v err=%v", tc.q, page, err)
		}
	}
}
