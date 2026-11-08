package postgres

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestCoverSelectionReceiptAuditAndReferenceRecovery(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db).WithAudit(NewAuditOutbox(db))
	actor := platform.NewID()
	credential := platform.NewID()
	ctx := auditclient.WithPrincipalProvenance(context.Background(), "service", actor, "cover-test-"+platform.NewID(), credential)
	now := time.Now().UTC().Truncate(time.Microsecond)
	value, err := repo.Create(ctx, "Cover recording", actor, platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, value.ID)
	pkg := strings.ReplaceAll(platform.NewID(), "-", "")
	if err := repo.AddPackage(ctx, value.ID, pkg, now); err != nil {
		t.Fatal(err)
	}
	value, err = repo.RecordPackageReady(ctx, value.ID, pkg, now)
	if err != nil {
		t.Fatal(err)
	}
	expiry := *value.ExpiresAt
	prepare := func(key, cover string, expected int64) recordings.CoverOperation {
		op, err := repo.PrepareCoverOperation(ctx, recordings.CoverOperation{ID: platform.NewID(), RecordingID: value.ID, PackageID: pkg, CoverID: cover, Expected: expected, Selection: recordings.CoverSelection{Mode: "custom", UploadID: cover}, Actor: actor, Key: key}, now)
		if err != nil {
			t.Fatal(err)
		}
		return op
	}
	op := prepare("cover-first", "cover-1", value.Version)
	var savedCredential string
	if err := db.QueryRow(`SELECT credential_id FROM hhc_web.member_recording_cover_operation WHERE id=$1`, op.ID).Scan(&savedCredential); err != nil || savedCredential != credential {
		t.Fatal("credential provenance lost", err)
	}
	retains := 0
	result, err := repo.CommitCoverOperation(ctx, op, true, now, func(context.Context) error { retains++; return nil })
	if err != nil || result.Outcome != "selected" || result.Receipt.RecordingVersion != value.Version+1 || !result.Recording.ExpiresAt.Equal(expiry) {
		t.Fatalf("selection %+v %v", result, err)
	}
	replay, err := repo.GetCoverOperation(ctx, value.ID, op.Expected, op.Selection, actor, op.Key, now)
	if err != nil || replay.Receipt != result.Receipt {
		t.Fatal("receipt not immutable", err)
	}
	if _, err := repo.GetCoverOperation(ctx, value.ID, op.Expected, recordings.CoverSelection{Mode: "custom", UploadID: "changed"}, actor, op.Key, now); !errors.Is(err, recordings.ErrConflict) {
		t.Fatal("key payload mismatch accepted", err)
	}
	value = result.Recording
	second := prepare("cover-second", "cover-2", value.Version)
	stale := prepare("cover-stale", "cover-3", value.Version)
	result, err = repo.CommitCoverOperation(ctx, second, true, now, func(context.Context) error { retains++; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CommitCoverOperation(ctx, stale, true, now, func(context.Context) error { t.Fatal("stale CAS retained cover"); return nil }); !errors.Is(err, recordings.ErrPrecondition) {
		t.Fatal("stale CAS accepted", err)
	}
	if err := repo.FailCoverOperation(ctx, stale.ID); err != nil {
		t.Fatal(err)
	}
	replay, err = repo.GetCoverOperation(ctx, value.ID, op.Expected, op.Selection, actor, op.Key, now)
	if err != nil || replay.Outcome != "state_changed" || replay.Recording.SelectedCoverID != "cover-2" {
		t.Fatal("replay restored old cover", err)
	}
	var released []string
	failFirst := true
	release := func(_ context.Context, op recordings.CoverOperation) error {
		if failFirst {
			failFirst = false
			return errors.New("fixture release failed")
		}
		released = append(released, op.ID)
		return nil
	}
	if err := repo.ReconcileCoverReferences(ctx, now, release); err == nil {
		t.Fatal("release failure swallowed")
	}
	if err := repo.ReconcileCoverReferences(ctx, now.Add(2*time.Minute), release); err != nil {
		t.Fatal(err)
	}
	if len(released) != 2 || (released[0] != op.ID && released[1] != op.ID) || (released[0] != stale.ID && released[1] != stale.ID) {
		t.Fatal("released current reference", released)
	}
	var audits, notifications int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE payload_json->>'action'='cms.recording.cover.update' AND payload_json->>'resourceId'=$1 AND payload_json->>'actorType'='service' AND payload_json->>'actorId'=$2`, value.ID, actor).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("audit %d %v", audits, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE event_type='recording.notification.submit' AND aggregate_id::text=$1`, value.ID).Scan(&notifications); err != nil || notifications != 0 {
		t.Fatal("cover notified", notifications, err)
	}
	if retains != 2 {
		t.Fatal("replayed retain", retains)
	}
}

func TestConcurrentCoverCASRetainsOutsideLocksAndAcceptsOneReceipt(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db).WithAudit(NewAuditOutbox(db))
	actor := platform.NewID()
	ctx := auditclient.WithProvenance(context.Background(), actor, "cover-race-"+platform.NewID())
	now := time.Now().UTC().Truncate(time.Microsecond)
	value, err := repo.Create(ctx, "Concurrent cover", actor, platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, value.ID)
	pkg := strings.ReplaceAll(platform.NewID(), "-", "")
	if err := repo.AddPackage(ctx, value.ID, pkg, now); err != nil {
		t.Fatal(err)
	}
	value, err = repo.RecordPackageReady(ctx, value.ID, pkg, now)
	if err != nil {
		t.Fatal(err)
	}
	var operations []recordings.CoverOperation
	for _, cover := range []string{"cover-1", "cover-2"} {
		op, err := repo.PrepareCoverOperation(ctx, recordings.CoverOperation{ID: platform.NewID(), RecordingID: value.ID, PackageID: pkg, CoverID: cover, Expected: value.Version, Selection: recordings.CoverSelection{Mode: "custom", UploadID: cover}, Actor: actor, Key: cover}, now)
		if err != nil {
			t.Fatal(err)
		}
		operations = append(operations, op)
	}
	start := make(chan struct{})
	errorsChannel := make(chan error, 2)
	var retains atomic.Int32
	retained := make(chan struct{})
	for _, op := range operations {
		go func(op recordings.CoverOperation) {
			<-start
			_, err := repo.CommitCoverOperation(ctx, op, true, now, func(ctx context.Context) error {
				if retains.Add(1) == 2 {
					close(retained)
				}
				wait, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
				select {
				case <-retained:
					return nil
				case <-wait.Done():
					return wait.Err()
				}
			})
			errorsChannel <- err
		}(op)
	}
	close(start)
	first, second := <-errorsChannel, <-errorsChannel
	if !((first == nil && errors.Is(second, recordings.ErrPrecondition)) || (second == nil && errors.Is(first, recordings.ErrPrecondition))) || retains.Load() != 2 {
		t.Fatalf("race outcomes %v %v retains=%d", first, second, retains.Load())
	}
	current, err := repo.Get(ctx, value.ID)
	if err != nil || current.Version != value.Version+1 || current.SelectedCoverID == "" {
		t.Fatal(current, err)
	}
}

func TestFailedAndAmbiguousCoverRetainNeverReleasesCurrent(t *testing.T) {
	db := accessDB(t)
	repo := NewRecordingRepository(db).WithAudit(NewAuditOutbox(db))
	actor := platform.NewID()
	ctx := auditclient.WithProvenance(context.Background(), actor, "cover-ambiguity-"+platform.NewID())
	now := time.Now().UTC().Truncate(time.Microsecond)
	value, err := repo.Create(ctx, "Ambiguous cover", actor, platform.NewID(), now)
	if err != nil {
		t.Fatal(err)
	}
	recordingCleanup(t, db, value.ID)
	pkg := strings.ReplaceAll(platform.NewID(), "-", "")
	repo.AddPackage(ctx, value.ID, pkg, now)
	value, err = repo.RecordPackageReady(ctx, value.ID, pkg, now)
	if err != nil {
		t.Fatal(err)
	}
	op, err := repo.PrepareCoverOperation(ctx, recordings.CoverOperation{ID: platform.NewID(), RecordingID: value.ID, PackageID: pkg, CoverID: "cover-1", Expected: value.Version, Selection: recordings.CoverSelection{Mode: "custom", UploadID: "upload-1"}, Actor: actor, Key: "ambiguous"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CommitCoverOperation(ctx, op, true, now, func(context.Context) error { return errors.New("accepted but response lost") }); err == nil {
		t.Fatal("ambiguous retain claimed success")
	}
	current, err := repo.Get(ctx, value.ID)
	if err != nil || current.SelectedCoverID != "" || current.Version != value.Version {
		t.Fatal("ambiguous retain selected", err)
	}
	released := 0
	if err := repo.ReconcileCoverReferences(ctx, now.Add(11*time.Minute), func(_ context.Context, v recordings.CoverOperation) error {
		released++
		if v.ID != op.ID {
			t.Fatal("wrong ref")
		}
		return nil
	}); err != nil || released != 1 {
		t.Fatal("orphan not released", released, err)
	}
	if _, err := repo.CommitCoverOperation(ctx, op, true, now, func(context.Context) error { t.Fatal("retained tombstoned operation"); return nil }); !errors.Is(err, recordings.ErrConflict) {
		t.Fatal("aborted intent resurrected", err)
	}
}
