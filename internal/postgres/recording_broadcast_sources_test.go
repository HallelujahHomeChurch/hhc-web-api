package postgres

import (
	"context"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

func TestBroadcastNamedSourceMigrationKeepsLegacySelector(t *testing.T) {
	db := accessDB(t)
	var legacyID, oldHash, oldSeed string
	if err := db.QueryRow(`SELECT id::text,key_hash,ingest_signing_secret FROM hhc_web.member_recording_broadcast_source WHERE singleton`).Scan(&legacyID, &oldHash, &oldSeed); err != nil {
		t.Fatal(err)
	}
	id := platform.NewID()
	if _, err := db.Exec(`INSERT INTO hhc_web.member_recording_broadcast_source(id,singleton,name) VALUES($1,NULL,'Second key')`, id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec(`DELETE FROM hhc_web.member_recording_broadcast_source WHERE id=$1`, id) })
	var gotID, hash, seed string
	if err := db.QueryRow(`SELECT id::text,key_hash,ingest_signing_secret FROM hhc_web.member_recording_broadcast_source WHERE singleton`).Scan(&gotID, &hash, &seed); err != nil {
		t.Fatal(err)
	}
	if gotID != legacyID || hash != oldHash || seed != oldSeed {
		t.Fatal("named source changed legacy identity or credentials")
	}
	if _, err := db.Exec(`INSERT INTO hhc_web.member_recording_broadcast_source(singleton,name) VALUES(true,'duplicate legacy')`); err == nil {
		t.Fatal("allowed two legacy selectors")
	}
}

func TestBroadcastCredentialsFollowBoundSourceNotLatestTarget(t *testing.T) {
	repo, b, _, _ := broadcastFixture(t)
	id := platform.NewID()
	if _, err := repo.db.Exec(`INSERT INTO hhc_web.member_recording_broadcast_source(id,singleton,name,key_hash,ingest_signing_secret) VALUES($1,NULL,'Bound source',repeat('a',64),repeat('b',64))`, id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET source_id=NULL WHERE recording_id=$1`, b.View.RecordingID)
		repo.db.Exec(`DELETE FROM hhc_web.member_recording_broadcast_source WHERE id=$1`, id)
	})
	if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET source_managed=true,source_id=$1 WHERE recording_id=$2`, id, b.View.RecordingID); err != nil {
		t.Fatal(err)
	}
	hash, seed, err := repo.BroadcastIngestCredentials(context.Background(), b.View.RecordingID)
	if err != nil || hash != repeatHex('a') || seed != repeatHex('b') {
		t.Fatal("credentials did not follow original source", err)
	}
}
func repeatHex(c byte) string {
	b := make([]byte, 64)
	for i := range b {
		b[i] = c
	}
	return string(b)
}

func TestLegacySourceCannotClaimAnotherNamedSourceDraft(t *testing.T) {
	repo, b, ctx, now, svc, actor, key := sourceFixture(t)
	id := platform.NewID()
	if _, err := repo.db.Exec(`INSERT INTO hhc_web.member_recording_broadcast_source(id,singleton,name) VALUES($1,NULL,'Other source')`, id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET source_id=NULL WHERE recording_id=$1`, b.View.RecordingID)
		repo.db.Exec(`DELETE FROM hhc_web.member_recording_broadcast_source WHERE id=$1`, id)
	})
	if _, err := repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast SET source_managed=true,source_id=$1 WHERE recording_id=$2`, id, b.View.RecordingID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetBroadcastSourceStreamKey(ctx, actor, recordings.BroadcastSourceStreamKeyInput{OperationKey: "legacy-key", ExpectedSourceRevision: 1, StreamKey: key}); err != nil {
		t.Fatal(err)
	}
	_, err := repo.SetBroadcastSourceTarget(ctx, actor, recordings.BroadcastSourceTargetInput{OperationKey: "steal", ExpectedSourceRevision: 2, RecordingID: b.View.RecordingID, ExpectedRevision: 1}, now)
	if !errors.Is(err, recordings.ErrBroadcastSourceNotPrepared) {
		t.Fatal("legacy adapter claimed another source", err)
	}
}
