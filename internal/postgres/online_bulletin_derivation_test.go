package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestSimplifiedContentRejectsManualMutations(t *testing.T) {
	// No database is needed: derived content must be rejected before any write.
	repo := &Repository{}
	now := time.Now().UTC()
	ctx := context.Background()
	_, saveErr := repo.SaveOnlineBulletinDraft(ctx, "issue", "general", "zh-Hans", onlinebulletins.SaveDraftInput{Components: make([]onlinebulletins.Component, 6)}, "admin", now)
	_, restoreErr := repo.RestoreOnlineBulletin(ctx, "issue", "general", "zh-Hans", onlinebulletins.RestoreInput{Revision: 1}, "admin", now)
	_, compareErr := repo.ApplyOnlineBulletinComparison(ctx, "issue", "general", "zh-Hans", onlinebulletins.CompareInput{BaseRevision: 1, LocalRevision: 2, IncomingRevision: 3}, "admin", now)
	for name, err := range map[string]error{"save": saveErr, "restore": restoreErr, "compare": compareErr} {
		if !errors.Is(err, bulletins.ErrConflict) {
			t.Fatalf("%s allowed derived content mutation: %v", name, err)
		}
	}
}

func seedSimplifiedSource(t *testing.T, db *sql.DB, now time.Time) (*Repository, onlinebulletins.ExtractionJob) {
	t.Helper()
	repo, source := seedOnlineLayout(t, db, now)
	ctx := context.Background()
	if _, err := repo.QueueOnlineBulletinLayout(ctx, source.DocumentID, 2, now); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
	if err != nil || job == nil {
		t.Fatal("layout claim", err)
	}
	result := measuredLayout(t, *job)
	if err := repo.CompleteOnlineBulletinLayout(ctx, *job, result, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ConfirmOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.ConfirmInput{CanonicalVersion: 1, OnlineVersion: 4, Revision: 2, LayoutValidationHash: result.LayoutValidationHash, PageCount: 4, AcceptedIssues: []onlinebulletins.ReviewIssue{}}, "admin", now); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, query := range []string{
			`DELETE FROM hhc_web.online_bulletin_conversion_job WHERE issue_id=$1`,
			`DELETE FROM hhc_web.online_bulletin_confirmation WHERE document_id IN (SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1)`,
			`DELETE FROM hhc_web.online_bulletin_layout_job WHERE document_id IN (SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1 AND content_locale='zh-Hans')`,
		} {
			if _, err := db.Exec(query, source.Canonical.IssueID); err != nil {
				t.Error(err)
			}
		}
	})
	return repo, source
}

func TestQueueSimplifiedRequiresConfirmedSource(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	_, err := repo.QueueOnlineBulletinConversion(context.Background(), source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: 1, SourceOnlineVersion: 2, SourceRevision: 1}, "admin", now)
	if !errors.Is(err, bulletins.ErrConflict) {
		t.Fatalf("unconfirmed: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_document WHERE issue_id=$1 AND content_locale='zh-Hans'`, source.Canonical.IssueID).Scan(&count); err != nil || count != 0 {
		t.Fatal("rejection wrote Hans draft", count, err)
	}
}

func TestSimplifiedRetryAfterEditionCreation(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedSimplifiedSource(t, db, now)
	ctx := context.Background()
	first, err := repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: 1, SourceOnlineVersion: 5, SourceRevision: 2}, "admin", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_conversion_job SET status='failed' WHERE id=$1`, first.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_document SET version=6 WHERE id=$1`, source.DocumentID); err != nil {
		t.Fatal(err)
	}
	retried, err := repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: 2, SourceOnlineVersion: 6, SourceRevision: 2, TargetOnlineVersion: first.Version, Retry: true}, "admin", now)
	if err != nil || retried.Job.ID != first.Job.ID {
		t.Fatalf("retry: %+v %v", retried, err)
	}
	var sourceVersion int64
	if err = db.QueryRow(`SELECT source_online_version FROM hhc_web.online_bulletin_conversion_job WHERE id=$1`, retried.Job.ID).Scan(&sourceVersion); err != nil || sourceVersion != 6 {
		t.Fatalf("stale retry fence: %d %v", sourceVersion, err)
	}
}

func TestSimplifiedRetryQueuesOnlyFailedSavedLayout(t *testing.T) {
	db := accessDB(t)
	now, ctx := time.Now().UTC(), context.Background()
	repo, source := seedSimplifiedSource(t, db, now)
	queued, err := repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: 1, SourceOnlineVersion: 5, SourceRevision: 2}, "admin", now)
	if err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimOnlineBulletinConversion(ctx, now, time.Minute, 3)
	if err != nil || job == nil {
		t.Fatal("claim", err)
	}
	draft := job.Source
	draft.ContentLocale = "zh-Hans"
	draft.TemplateVersion, draft.LayoutManifest.TemplateVersion, draft.LayoutManifest.RendererVersion = "v2", "v2", "v2"
	draft.LayoutManifest.ContentHash, draft.LayoutManifest.LayoutValidationHash = "", ""
	if err = repo.CompleteOnlineBulletinConversion(ctx, *job, draft, job.SourceMetadata, now); err != nil {
		t.Fatal(err)
	}
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hans")
	if err != nil || state.Local == nil {
		t.Fatal("state", err)
	}
	input := onlinebulletins.QueueSimplifiedInput{CanonicalVersion: state.CanonicalVersion, SourceOnlineVersion: 5, SourceRevision: 2, TargetOnlineVersion: state.Version, Retry: true}
	if _, err = repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, input, "admin", now); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("running layout retried", err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_layout_job SET status='failed' WHERE document_id=$1`, queued.DocumentID); err != nil {
		t.Fatal(err)
	}
	stale := input
	stale.TargetOnlineVersion--
	if _, err = repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, stale, "admin", now); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("stale target retried", err)
	}
	stale = input
	stale.CanonicalVersion--
	if _, err = repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, stale, "admin", now); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("stale metadata retried", err)
	}
	stale = input
	stale.SourceOnlineVersion--
	if _, err = repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, stale, "admin", now); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("stale source retried", err)
	}
	result, err := repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, input, "admin", now.Add(time.Second))
	if err != nil {
		t.Fatal("retry saved layout", err)
	}
	if result.Version != state.Version+1 || result.Job.Status != "review_ready" || result.Job.ID != queued.Job.ID {
		t.Fatalf("conversion was restarted: %+v", result)
	}
	after, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hans")
	if err != nil || after.Local.Revision != state.Local.Revision || after.Layout == nil || after.Layout.Status != "queued" {
		t.Fatalf("wrong saved layout queued: %+v %v", after, err)
	}
	var revisions int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_revision WHERE document_id=$1`, queued.DocumentID).Scan(&revisions); err != nil || revisions != 1 {
		t.Fatal("retry rewrote derived content", revisions, err)
	}
	input.TargetOnlineVersion = result.Version
	if _, err = repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, input, "admin", now); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("retry duplicated queued layout", err)
	}
}

func TestQueueSimplifiedIdempotentAndStaleVersions(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedSimplifiedSource(t, db, now)
	ctx := context.Background()
	input := onlinebulletins.QueueSimplifiedInput{CanonicalVersion: 1, SourceOnlineVersion: 5, SourceRevision: 2}
	bad := input
	bad.SourceOnlineVersion--
	if _, err := repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, bad, "admin", now); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("stale source", err)
	}
	first, err := repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, input, "admin", now)
	if err != nil || first.Version != 1 || first.Job.Status != "queued" {
		t.Fatalf("queue: %+v %v", first, err)
	}
	again, err := repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, input, "admin", now)
	if err != nil || again.Job.ID != first.Job.ID || again.DocumentID != first.DocumentID {
		t.Fatal("duplicate job", err)
	}
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hans")
	if err != nil || state.Conversion == nil || state.Conversion.ID != first.Job.ID || state.Derivation == nil || state.Derivation.SourceRevision != 2 || state.Derivation.Stale {
		t.Fatalf("missing conversion provenance: %+v %v", state, err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET subtitle='changed source' WHERE issue_id=$1 AND locale='zh-Hant'`, source.Canonical.IssueID); err != nil {
		t.Fatal(err)
	}
	state, err = repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hans")
	if err != nil || state.Derivation == nil || !state.Derivation.Stale {
		t.Fatal("source metadata change not reported", err)
	}
	var asset, status string
	if err := db.QueryRow(`SELECT pdf_asset_id,status FROM hhc_web.bulletin_version WHERE issue_id=$1 AND series='general' AND locale='zh-Hans'`, source.Canonical.IssueID).Scan(&asset, &status); err != nil || asset != "" || status != "draft" {
		t.Fatal("fake PDF or automatic publication", asset, status, err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_document SET version=version+1 WHERE id=$1`, source.DocumentID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, input, "admin", now); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("stale replay accepted", err)
	}
}

func TestSimplifiedRetryPreservesEditedMetadata(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	ctx := context.Background()
	repo, source := seedSimplifiedSource(t, db, now)
	queued, err := repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: 1, SourceOnlineVersion: 5, SourceRevision: 2}, "admin", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_conversion_job SET status='failed' WHERE id=$1`, queued.Job.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.UpdateVersion(ctx, source.Canonical.IssueID, "general", "zh-Hans", 2, "人工标题", "人工副标题", "admin", now); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: 3, SourceOnlineVersion: 5, SourceRevision: 2, TargetOnlineVersion: queued.Version, Retry: true}, "admin", now); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimOnlineBulletinConversion(ctx, now, time.Minute, 3)
	if err != nil || job == nil {
		t.Fatal("claim", err)
	}
	if job.InitializesMetadata {
		t.Error("retry still owns manually edited canonical metadata")
	}
	draft := job.Source
	draft.ContentLocale, draft.TemplateVersion = "zh-Hans", "v2"
	draft.LayoutManifest.TemplateVersion, draft.LayoutManifest.RendererVersion = "v2", "v2"
	converted := job.SourceMetadata
	converted.Title = "转换标题"
	if err = repo.CompleteOnlineBulletinConversion(ctx, *job, draft, converted, now); err != nil {
		t.Fatal(err)
	}
	var title, subtitle string
	if err = db.QueryRow(`SELECT title,subtitle FROM hhc_web.bulletin_version WHERE issue_id=$1 AND locale='zh-Hans'`, source.Canonical.IssueID).Scan(&title, &subtitle); err != nil || title != "人工标题" || subtitle != "人工副标题" {
		t.Fatalf("manual metadata overwritten: %q %q %v", title, subtitle, err)
	}
}

func TestSimplifiedConversionReplacesWorkingCopyAndRetainsHistory(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	ctx := context.Background()
	repo, source := seedSimplifiedSource(t, db, now)
	input := onlinebulletins.QueueSimplifiedInput{CanonicalVersion: 1, SourceOnlineVersion: 5, SourceRevision: 2}
	queued, err := repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, input, "admin", now)
	if err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimOnlineBulletinConversion(ctx, now, time.Minute, 3)
	if err != nil || job == nil {
		t.Fatal("claim", err)
	}
	draft := job.Source
	draft.ContentLocale = "zh-Hans"
	draft.TemplateVersion = "v2"
	draft.LayoutManifest.TemplateVersion = "v2"
	draft.LayoutManifest.RendererVersion = "v2"
	draft.LayoutManifest.ContentHash = ""
	draft.LayoutManifest.LayoutValidationHash = ""
	metadata := job.SourceMetadata
	metadata.Title = "简中标题"
	if err = repo.CompleteOnlineBulletinConversion(ctx, *job, draft, metadata, now); err != nil {
		t.Fatal("complete", err)
	}
	var local, base, version int64
	var incoming sql.NullInt64
	read := func() {
		t.Helper()
		if err := db.QueryRow(`SELECT local_revision,base_revision,incoming_revision,version FROM hhc_web.online_bulletin_document WHERE id=$1`, queued.DocumentID).Scan(&local, &base, &incoming, &version); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if local != 1 || base != 1 || incoming.Valid {
		t.Fatal("first conversion pointers", local, base, incoming)
	}
	if err = repo.CompleteOnlineBulletinConversion(ctx, *job, draft, metadata, now); err != nil {
		t.Fatal("completion replay", err)
	}
	input.CanonicalVersion = 3
	input.TargetOnlineVersion = version
	again, err := repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, input, "admin", now)
	if err != nil || again.Job.ID != queued.Job.ID || again.Version != version {
		t.Fatalf("unchanged source created another conversion: %+v %v", again, err)
	}
	read()
	if incoming.Valid {
		t.Fatal("unchanged source created incoming")
	}
	// Seed a later immutable, confirmed source revision.
	if _, err = db.Exec(`INSERT INTO hhc_web.online_bulletin_revision(document_id,issue_id,series,content_locale,revision,document_json,source_asset_checksum,schema_version,template_version,extractor_version,content_hash,renderer_version,renderer_artifact_sha256,layout_validation_hash,review_issues,created_by,created_at)
 SELECT document_id,issue_id,series,content_locale,3,document_json,source_asset_checksum,schema_version,template_version,extractor_version,content_hash,renderer_version,renderer_artifact_sha256,layout_validation_hash,review_issues,created_by,created_at FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=2`, source.DocumentID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO hhc_web.online_bulletin_confirmation(document_id,revision,canonical_metadata,layout_validation_hash,page_count,accepted_issues,created_by,created_at)
 SELECT document_id,3,canonical_metadata,layout_validation_hash,page_count,accepted_issues,created_by,created_at FROM hhc_web.online_bulletin_confirmation WHERE document_id=$1 AND revision=2`, source.DocumentID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_document SET version=6,local_revision=3 WHERE id=$1`, source.DocumentID); err != nil {
		t.Fatal(err)
	}
	input.SourceOnlineVersion = 6
	input.SourceRevision = 3
	input.CanonicalVersion = 3
	input.TargetOnlineVersion = version
	if _, err = repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, input, "admin", now); err != nil {
		t.Fatal("requeue", err)
	}
	next, err := repo.ClaimOnlineBulletinConversion(ctx, now, time.Minute, 3)
	if err != nil || next == nil {
		t.Fatal(err)
	}
	if err = repo.CompleteOnlineBulletinConversion(ctx, *next, draft, metadata, now); err != nil {
		t.Fatal(err)
	}
	read()
	if local != 2 || base != 2 || incoming.Valid {
		t.Fatal("conversion did not replace working copy", local, base, incoming)
	}
	var revisions int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_revision WHERE document_id=$1`, queued.DocumentID).Scan(&revisions); err != nil || revisions != 2 {
		t.Fatal("conversion lost immutable history", revisions, err)
	}
	var layoutStatus string
	if err = db.QueryRow(`SELECT status FROM hhc_web.online_bulletin_layout_job WHERE document_id=$1 AND source_revision=2`, queued.DocumentID).Scan(&layoutStatus); err != nil || layoutStatus != "queued" {
		t.Fatal("replacement requires new layout validation", layoutStatus, err)
	}
	// A completed pre-upgrade conversion could still be waiting as Incoming.
	if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_document SET incoming_revision=2 WHERE id=$1`, queued.DocumentID); err != nil {
		t.Fatal(err)
	}
	input.TargetOnlineVersion = version
	migrated, err := repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, input, "admin", now)
	if err != nil || migrated.Job.Status != "queued" {
		t.Fatalf("legacy incoming stranded: %+v %v", migrated, err)
	}
	next, err = repo.ClaimOnlineBulletinConversion(ctx, now, time.Minute, 3)
	if err != nil || next == nil {
		t.Fatal("claim legacy conversion", err)
	}
	if err = repo.CompleteOnlineBulletinConversion(ctx, *next, draft, metadata, now); err != nil {
		t.Fatal(err)
	}
	read()
	if local != 3 || base != 3 || incoming.Valid {
		t.Fatal("legacy conversion not reconciled", local, base, incoming)
	}
}

func TestSimplifiedJobSurvivesSourcePublication(t *testing.T) {
	for _, unpublish := range []bool{false, true} {
		t.Run(map[bool]string{false: "publish", true: "publish-then-unpublish"}[unpublish], func(t *testing.T) {
			db := accessDB(t)
			now, ctx := time.Now().UTC(), context.Background()
			repo, source := seedSimplifiedSource(t, db, now)
			t.Cleanup(func() {
				for _, query := range []string{
					`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE id=$1`,
					`DELETE FROM hhc_web.online_bulletin_publication WHERE document_id=$1`,
				} {
					if _, err := db.Exec(query, source.DocumentID); err != nil {
						t.Error(err)
					}
				}
				if _, err := db.Exec(`DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
					t.Error(err)
				}
			})
			queued, err := repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: 1, SourceOnlineVersion: 5, SourceRevision: 2}, "admin", now)
			if err != nil {
				t.Fatal(err)
			}
			job, err := repo.ClaimOnlineBulletinConversion(ctx, now, time.Minute, 3)
			if err != nil || job == nil {
				t.Fatal("claim", err)
			}
			if _, err = db.Exec(`UPDATE hhc_web.bulletin_version SET status='published' WHERE issue_id=$1 AND locale='zh-Hant'`, source.Canonical.IssueID); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Exec(`INSERT INTO hhc_web.bulletin_published_version(issue_id,series,locale,title,subtitle,asset_id,published_at,version) SELECT issue_id,series,locale,title,subtitle,pdf_asset_id,$2,1 FROM hhc_web.bulletin_version WHERE issue_id=$1 AND locale='zh-Hant'`, source.Canonical.IssueID, now); err != nil {
				t.Fatal(err)
			}
			published, err := repo.PublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.PublishInput{CanonicalVersion: 2, OnlineVersion: 5, Revision: 2}, "admin", now)
			if err != nil {
				t.Fatal("publish source", err)
			}
			if unpublish {
				if _, err = repo.UnpublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", 2, published.Version, "admin", now); err != nil {
					t.Fatal("unpublish source", err)
				}
			}
			draft := job.Source
			draft.ContentLocale, draft.TemplateVersion = "zh-Hans", "v2"
			draft.LayoutManifest.TemplateVersion, draft.LayoutManifest.RendererVersion = "v2", "v2"
			if err = repo.CompleteOnlineBulletinConversion(ctx, *job, draft, job.SourceMetadata, now); err != nil {
				t.Fatal("unchanged confirmed source must remain convertible", err)
			}
			target, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hans")
			if err != nil || target.Local == nil || target.Local.Revision != 1 || target.PublishedRevision != nil || target.Conversion.ID != queued.Job.ID || target.Conversion.Status != "review_ready" || target.Layout.Status != "queued" || target.Derivation.Stale {
				t.Fatalf("conversion not saved as an unpublished derived draft: %+v %v", target, err)
			}
		})
	}
}

func TestSimplifiedJobRequiresCurrentSourceConfirmationOrCleanProof(t *testing.T) {
	db := accessDB(t)
	now, ctx := time.Now().UTC(), context.Background()
	repo, source := seedSimplifiedSource(t, db, now)
	if _, err := repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: 1, SourceOnlineVersion: 5, SourceRevision: 2}, "admin", now); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimOnlineBulletinConversion(ctx, now, time.Minute, 3)
	if err != nil || job == nil {
		t.Fatal("claim", err)
	}
	if _, err = db.Exec(`DELETE FROM hhc_web.online_bulletin_confirmation WHERE document_id=$1`, source.DocumentID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM hhc_web.online_bulletin_layout_job WHERE document_id=$1`, source.DocumentID); err != nil {
		t.Fatal(err)
	}
	draft := job.Source
	draft.ContentLocale, draft.TemplateVersion = "zh-Hans", "v2"
	draft.LayoutManifest.TemplateVersion, draft.LayoutManifest.RendererVersion = "v2", "v2"
	if err = repo.CompleteOnlineBulletinConversion(ctx, *job, draft, job.SourceMetadata, now); !errors.Is(err, onlinebulletins.ErrConversionStale) {
		t.Fatal("unconfirmed source committed", err)
	}
	target, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hans")
	if err != nil || target.Local != nil || target.PublishedRevision != nil {
		t.Fatal("rejected conversion wrote content", err)
	}
}

func TestSimplifiedJobRejectsChangedSourceRevision(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	ctx := context.Background()
	repo, source := seedSimplifiedSource(t, db, now)
	if _, err := repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: 1, SourceOnlineVersion: 5, SourceRevision: 2}, "admin", now); err != nil {
		t.Fatal(err)
	}
	first, err := repo.ClaimOnlineBulletinConversion(ctx, now, time.Minute, 3)
	if err != nil || first == nil {
		t.Fatal(err)
	}
	next, err := repo.ClaimOnlineBulletinConversion(ctx, now.Add(61*time.Second), time.Minute, 3)
	if err != nil || next == nil || first.LeaseToken == next.LeaseToken {
		t.Fatal("lease not fenced", err)
	}
	if err = repo.FailOnlineBulletinConversion(ctx, *first, "conversion_failed", nil, now.Add(62*time.Second), 3); !errors.Is(err, onlinebulletins.ErrExtractionLease) {
		t.Fatal("old lease wrote state", err)
	}
	draft := next.Source
	draft.ContentLocale = "zh-Hans"
	draft.TemplateVersion = "v2"
	draft.LayoutManifest.TemplateVersion = "v2"
	draft.LayoutManifest.RendererVersion = "v2"
	if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_document SET version=version+1,local_revision=1 WHERE id=$1`, source.DocumentID); err != nil {
		t.Fatal(err)
	}
	if err = repo.CompleteOnlineBulletinConversion(ctx, *next, draft, next.SourceMetadata, now.Add(62*time.Second)); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("stale source committed", err)
	}
}

func TestSimplifiedFailureLocationsPreserveSourceAndTarget(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	ctx := context.Background()
	repo, source := seedSimplifiedSource(t, db, now)
	if _, err := repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: 1, SourceOnlineVersion: 5, SourceRevision: 2}, "admin", now); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimOnlineBulletinConversion(ctx, now, time.Minute, 1)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	issues := []onlinebulletins.ReviewIssue{{Code: "layout_validation_failed", SourcePage: 2, Blocking: true}}
	if err = repo.FailOnlineBulletinConversion(ctx, *job, "simplified_conversion_failed", issues, now, 1); err != nil {
		t.Fatal(err)
	}
	target, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hans")
	if err != nil || target.Local != nil || target.PublishedRevision != nil || target.Conversion == nil || len(target.Conversion.ReviewIssues) != 1 || target.Conversion.ReviewIssues[0] != issues[0] {
		t.Fatal("lost diagnostic or changed target", err)
	}
	original, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || original.Local.Revision != 2 || !original.Confirmed {
		t.Fatal("changed source", err)
	}
}
