package postgres

import (
	"bytes"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"testing"
)

func TestStudioKeyEncryptionAndLegacyCAS(t *testing.T) {
	repo, _, ctx, _, svc, actor, key := sourceFixture(t)
	cipher, e := recordings.NewStreamKeyCipher("v1", map[string][]byte{"v1": bytes.Repeat([]byte{7}, 32)})
	if e != nil {
		t.Fatal(e)
	}
	svc.WithStudioWriters(true).WithStreamKeyCipher(cipher)
	source, e := svc.MutateStudioSource(ctx, "", actor, "create", recordings.StudioSourceInput{BroadcastMutationInput: recordings.BroadcastMutationInput{OperationKey: platform.NewID()}, Name: "Test", StreamKey: key})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		repo.db.Exec(`DELETE FROM hhc_web.member_recording_studio_operation WHERE source_id=$1`, source.ID)
		repo.db.Exec(`DELETE FROM hhc_web.member_recording_broadcast_source WHERE id=$1`, source.ID)
	})
	hash, sealed, e := repo.ReadStreamKey(ctx, source.ID)
	if e != nil || sealed.KeyID != "v1" || bytes.Contains(sealed.Ciphertext, []byte(key)) {
		t.Fatal("plaintext storage or missing envelope")
	}
	plain, e := svc.RevealStudioStreamKey(ctx, source.ID, actor)
	if e != nil || plain != key {
		t.Fatal("reveal failed", e)
	}
	if _, e = repo.db.Exec(`UPDATE hhc_web.member_recording_broadcast_source SET key_ciphertext=NULL,key_encryption_id='' WHERE id=$1`, source.ID); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.RevealStudioStreamKey(ctx, source.ID, actor); e == nil {
		t.Fatal("legacy hash unexpectedly revealed")
	}
	if e = repo.RetainStreamKey(ctx, "wrong-hash", sealed); e != nil {
		t.Fatal(e)
	}
	_, empty, _ := repo.ReadStreamKey(ctx, source.ID)
	if len(empty.Ciphertext) != 0 {
		t.Fatal("stale hash wrote envelope")
	}
	if e = repo.RetainStreamKey(ctx, hash, sealed); e != nil {
		t.Fatal(e)
	}
	plain, e = svc.RevealStudioStreamKey(ctx, source.ID, actor)
	if e != nil || plain != key {
		t.Fatal("legacy backfill failed", e)
	}
	sources, e := repo.ListStudioSources(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range sources {
		if s.ID == source.ID && s.Revision != source.Revision {
			t.Fatal("backfill changed revision")
		}
	}
}
