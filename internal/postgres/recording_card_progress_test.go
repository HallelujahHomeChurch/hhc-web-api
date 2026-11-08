package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCardProgressVisibilityVersionAccountAndCompletion(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	ctx := context.Background()
	now := time.Now().UTC()
	id, user := platform.NewID(), platform.NewID()
	const pkg = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_, err := db.Exec(`INSERT INTO hhc_web.member_recording(id,title_override,create_title,uploaded_at,expires_at,draft_expires_at,status,package_id,ready_at,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1,'Video','Video',$2,$2::timestamptz+interval '720 hours',$2,'published',$3,$2,$4,'test','test',$2,$2)`, id, now, pkg, platform.NewID())
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, id)
	empty, err := repo.GetCardProgress(ctx, user, []string{id}, now)
	if err != nil || empty[id].MediaVersion != "vod:"+pkg || empty[id].Progress != nil {
		t.Fatalf("new card version=%+v err=%v", empty, err)
	}
	claim := recordings.ProgressClaim{SessionID: platform.NewID(), ClaimID: platform.NewID(), MediaVersion: "vod:" + pkg, StartKind: "resume"}
	owner, err := repo.ClaimPlaybackProgress(ctx, id, user, claim, 100, now)
	if err != nil {
		t.Fatal(err)
	}
	update := recordings.ProgressUpdate{SessionID: claim.SessionID, Generation: owner.Generation, MediaVersion: claim.MediaVersion, Sequence: 1, PositionSeconds: 42, State: "paused"}
	if _, err = repo.UpdatePlaybackProgress(ctx, id, user, update, now); err != nil {
		t.Fatal(err)
	}
	read := func(want bool, completed bool) {
		t.Helper()
		got, err := repo.GetCardProgress(ctx, user, []string{id, platform.NewID()}, now)
		if err != nil {
			t.Fatal(err)
		}
		p := got[id].Progress
		ok := p != nil
		if ok != want || ok && (p.PositionSeconds != 42 || p.Completed != completed || p.MediaVersion != claim.MediaVersion) {
			t.Fatalf("card=%+v found=%v", p, ok)
		}
	}
	read(true, false)
	if got, err := repo.GetCardProgress(ctx, platform.NewID(), []string{id}, now); err != nil || (got[id].MediaVersion != claim.MediaVersion || got[id].Progress != nil) {
		t.Fatalf("account leak: %+v %v", got, err)
	}
	for _, change := range []struct{ sql, reset string }{
		{`UPDATE hhc_web.member_recording SET hidden=true WHERE id=$1`, `UPDATE hhc_web.member_recording SET hidden=false WHERE id=$1`},
		{`UPDATE hhc_web.member_recording SET status='draft' WHERE id=$1`, `UPDATE hhc_web.member_recording SET status='published' WHERE id=$1`},
		{`UPDATE hhc_web.member_recording SET package_id='bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' WHERE id=$1`, `UPDATE hhc_web.member_recording SET package_id='aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' WHERE id=$1`},
		{`UPDATE hhc_web.member_recording_progress SET expires_at=now()-interval '1 minute' WHERE recording_id=$1`, `UPDATE hhc_web.member_recording_progress SET expires_at=now()+interval '1 hour' WHERE recording_id=$1`},
	} {
		if _, err = db.Exec(change.sql, id); err != nil {
			t.Fatal(err)
		}
		read(false, false)
		if _, err = db.Exec(change.reset, id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`UPDATE hhc_web.member_recording_progress SET completed=true WHERE recording_id=$1`, id); err != nil {
		t.Fatal(err)
	}
	read(true, true)
	t.Cleanup(func() {
		db.Exec(`DELETE FROM hhc_web.bulletin_watermark_subject WHERE subject_hash=$1`, watermarkSubjectHash(user))
	})
	if _, _, err = (&Repository{db: db}).CleanupWatermarkAccount(ctx, user, platform.NewID()); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.GetCardProgress(ctx, user, []string{id}, now); !errors.Is(err, bulletinwatermark.ErrRestricted) {
		t.Fatalf("restriction bypassed: %v", err)
	}
}

type cardQueryCounter struct{ queries int }

func (c *cardQueryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "SELECT r.id::text,r.package_id") {
		c.queries++
	}
	return ctx
}
func (c *cardQueryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestCardProgressUsesOneCollectionQueryForTenAndHundred(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	user := platform.NewID()
	now := time.Now().UTC()
	rows, err := db.Query(`INSERT INTO hhc_web.member_recording(id,title_override,create_title,uploaded_at,expires_at,draft_expires_at,status,package_id,ready_at,idempotency_key,created_by,updated_by,created_at,updated_at)
 SELECT gen_random_uuid(),'Card','Card',now(),now()+interval '1 hour',now(),'published',md5(gen_random_uuid()::text),now(),gen_random_uuid()::text,'test','test',now(),now() FROM generate_series(1,100) RETURNING id::text`)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Exec(`DELETE FROM hhc_web.member_recording_progress WHERE recording_id=ANY($1::uuid[])`, ids)
		db.Exec(`DELETE FROM hhc_web.member_recording WHERE id=ANY($1::uuid[])`, ids)
	})
	_, err = db.Exec(`INSERT INTO hhc_web.member_recording_progress(account_id,recording_id,media_version,mode,duration_seconds,position_seconds,owner_session_id,owner_generation,claim_id,last_sequence,claimed_at,updated_at,expires_at)
 SELECT $1,id,'vod:'||package_id,'vod',100,42,gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),1,now()-interval '1 minute',now(),now()+interval '1 hour' FROM hhc_web.member_recording WHERE id=ANY($2::uuid[])`, user, ids)
	if err != nil {
		t.Fatal(err)
	}
	config, err := pgx.ParseConfig(os.Getenv("HHW_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	counter := &cardQueryCounter{}
	config.Tracer = counter
	traced := stdlib.OpenDB(*config)
	defer traced.Close()
	repo := NewRecordingRepository(traced)
	for _, count := range []int{0, 10, 100} {
		counter.queries = 0
		got, err := repo.GetCardProgress(ctx, user, ids[:count], now)
		wantQueries := 1
		if count == 0 {
			wantQueries = 0
		}
		if err != nil || len(got) != count || counter.queries != wantQueries {
			t.Fatalf("cards=%d got=%d queries=%d err=%v", count, len(got), counter.queries, err)
		}
	}
}

func TestCardPlaybackVersionSurvivesMissingHistoryWithoutClaiming(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db)
	now := time.Now().UTC()
	id, user := platform.NewID(), platform.NewID()
	const pkg = "cccccccccccccccccccccccccccccccc"
	_, err := db.Exec(`INSERT INTO hhc_web.member_recording(id,title_override,create_title,uploaded_at,expires_at,draft_expires_at,status,package_id,ready_at,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1,'New','New',$2,$2::timestamptz+interval '1 hour',$2,'published',$3,$2,$4,'test','test',$2,$2)`, id, now, pkg, platform.NewID())
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, id)
	read := func() {
		t.Helper()
		values, err := repo.GetCardProgress(context.Background(), user, []string{id}, now)
		if err != nil || values[id].MediaVersion != "vod:"+pkg || values[id].Progress != nil {
			t.Fatalf("card=%+v err=%v", values, err)
		}
	}
	read()
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.member_recording_progress WHERE recording_id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatalf("read claimed owner: %d %v", count, err)
	}
	_, err = db.Exec(`INSERT INTO hhc_web.member_recording_progress(account_id,recording_id,media_version,mode,capture_id,duration_seconds,position_seconds,owner_session_id,owner_generation,claim_id,last_sequence,claimed_at,updated_at,expires_at) VALUES($1,$2,$3,'live','cccccccccccccccccccccccccccccccc',NULL,80,gen_random_uuid(),gen_random_uuid(),gen_random_uuid(),1,$4,$4,$4::timestamptz+interval '1 hour')`, user, id, "live:"+pkg, now)
	if err != nil {
		t.Fatal(err)
	}
	read()
	var version string
	if err := db.QueryRow(`SELECT media_version FROM hhc_web.member_recording_progress WHERE recording_id=$1`, id).Scan(&version); err != nil || version != "live:"+pkg {
		t.Fatalf("read rewrote history: %s %v", version, err)
	}
}

func TestCardPlaybackBroadcastVersionWithoutHistoryHonorsAuthority(t *testing.T) {
	repo, b, ctx, now := subjectLiveFixture(t)
	id, user, pkg := b.View.RecordingID, platform.NewID(), b.View.Binding.CaptureID
	start, end, duration := 4, 10, 120.0
	b.View.Phase, b.MemberState, b.RangeApplied = "archived", "vod", 5
	b.View.Boundaries = recordings.BroadcastBoundaries{Revision: 5, StartSequence: &start, EndSequenceExclusive: &end}
	b.Projection = &assetclient.BroadcastProjection{Epoch: b.Epoch, RangeRevision: 5, State: "ready", StartSequence: &start, EndSequenceExclusive: &end, DurationSeconds: &duration}
	if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording SET status='published',package_id=$2,uploaded_at=$3,ready_at=$3,expires_at=$3::timestamptz+interval '1 hour' WHERE id=$1`, id, pkg, now); err != nil {
		t.Fatal(err)
	}
	check := func(version string) {
		t.Helper()
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET payload_json=$2 WHERE recording_id=$1`, id, raw); err != nil {
			t.Fatal(err)
		}
		values, err := repo.GetCardProgress(ctx, user, []string{id}, now)
		if err != nil || values[id].MediaVersion != version || values[id].Progress != nil {
			t.Fatalf("version=%s card=%+v err=%v", version, values, err)
		}
	}
	check("vod:" + pkg + ":5")
	b.RangePending = true
	check("")
	b.RangePending = false
	b.Revoked = true
	check("")
	b.Revoked = false
	b.RangeApplied = 6
	check("")
	b.View.Boundaries.Revision = 6
	b.Projection.RangeRevision = 6
	check("vod:" + pkg + ":6")
}
