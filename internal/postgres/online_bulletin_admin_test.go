package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestDraftSaveRecoversPublishedAnchorsWithoutPublishing(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source, published := seedReaderPublication(t, db, now)
	repo = repo.WithAudit(NewAuditOutbox(db))
	ctx := bulletinAuditContext()
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	oldID := state.Local.Document.Components[1].BodySection.Blocks[0].Sentences[0].ID
	seedDraftParserIdentity(t, db, source.DocumentID, state.Local.Document, state.Local.Revision, "replacement-parser-id", nil)
	state, err = repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	components := state.Local.Document.Components
	components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text += " "
	saved, err := repo.SaveOnlineBulletinDraft(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.SaveDraftInput{CanonicalVersion: 1, OnlineVersion: state.Version, Components: components}, postgresBulletinAuditActor, now)
	if err != nil {
		t.Fatal(err)
	}
	state, err = repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	if state.Local.Document.Components[1].BodySection.Blocks[0].Sentences[0].ID != oldID || *state.PublishedRevision != published || state.Local.Revision != saved.Revision || state.Local.Document.LayoutManifest.LayoutValidationHash != "" {
		t.Fatal("draft lost published identity or reused publication/proof", state)
	}
	if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE id=$1`, source.DocumentID); err != nil {
		t.Fatal(err)
	}
	seedDraftParserIdentity(t, db, source.DocumentID, state.Local.Document, state.Local.Revision, "unpublished-parser-id", nil)
	state, err = repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	components = state.Local.Document.Components
	if _, err = repo.SaveOnlineBulletinDraft(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.SaveDraftInput{CanonicalVersion: 1, OnlineVersion: state.Version, Components: components}, postgresBulletinAuditActor, now); err != nil {
		t.Fatal(err)
	}
	state, err = repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || state.Local.Document.Components[1].BodySection.Blocks[0].Sentences[0].ID != oldID || state.PublishedRevision != nil {
		t.Fatal("unpublished save lost immutable publication identity", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.outbox_event WHERE destination='audit-log' AND payload_json->>'resourceId'=$1`, source.DocumentID)
	})
}

func TestDraftAnchorRecoveryKeepsSentenceSpecificReviewLocations(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source, published := seedReaderPublication(t, db, now)
	ctx := bulletinAuditContext()
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	document := state.Local.Document
	oldID := document.Components[1].BodySection.Blocks[0].Sentences[0].ID
	const parserID = "legacy-parser-id"
	seedDraftParserIdentity(t, db, source.DocumentID, document, published, parserID, []onlinebulletins.ReviewIssue{{Code: "unknown_font_role", ComponentID: document.Components[1].ID, BlockID: document.Components[1].BodySection.Blocks[0].ID, SentenceID: parserID, SourcePage: 2, Blocking: true}})
	state, err = repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	repo = repo.WithAudit(NewAuditOutbox(db))
	if _, err = repo.SaveOnlineBulletinDraft(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.SaveDraftInput{CanonicalVersion: 1, OnlineVersion: state.Version, Components: state.Local.Document.Components}, postgresBulletinAuditActor, now); err != nil {
		t.Fatal(err)
	}
	state, err = repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, issue := range state.Local.ReviewIssues {
		if issue.Code == "unknown_font_role" {
			found = true
			if issue.SentenceID != oldID || issue.SourcePage != 2 || !issue.Blocking {
				t.Fatal("review finding lost source location", issue)
			}
		}
	}
	if !found {
		t.Fatal("recovery silently discarded review finding")
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.outbox_event WHERE destination='audit-log' AND payload_json->>'resourceId'=$1`, source.DocumentID)
	})
}

func TestDraftSaveKeepsAuthoredReplacementAcrossRepeatedSaves(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source, _ := seedReaderPublication(t, db, now)
	repo = repo.WithAudit(NewAuditOutbox(db))
	ctx := bulletinAuditContext()
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	components := state.Local.Document.Components
	components[1].BodySection.Blocks[0].Sentences[0].ID = "authored-replacement"
	components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = "替代。"
	for i := 0; i < 2; i++ {
		if _, err = repo.SaveOnlineBulletinDraft(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.SaveDraftInput{CanonicalVersion: 1, OnlineVersion: state.Version, Components: components}, postgresBulletinAuditActor, now); err != nil {
			t.Fatal(err)
		}
		state, err = repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
		if err != nil {
			t.Fatal(err)
		}
		components = state.Local.Document.Components
		if components[1].BodySection.Blocks[0].Sentences[0].ID != "authored-replacement" {
			t.Fatal("deliberate replacement lost identity on save", i)
		}
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.outbox_event WHERE destination='audit-log' AND payload_json->>'resourceId'=$1`, source.DocumentID)
	})
}

func seedDraftParserIdentity(t *testing.T, db *sql.DB, documentID string, document onlinebulletins.Document, revision int64, parserID string, findings []onlinebulletins.ReviewIssue) {
	t.Helper()
	oldID := document.Components[1].BodySection.Blocks[0].Sentences[0].ID
	document.Components[1].BodySection.Blocks[0].Sentences[0].ID = parserID
	for i := range document.LayoutManifest.Pages {
		for j := range document.LayoutManifest.Pages[i].Slots {
			for k := range document.LayoutManifest.Pages[i].Slots[j].Fragments {
				fragment := &document.LayoutManifest.Pages[i].Slots[j].Fragments[k]
				if fragment.SentenceID == oldID {
					fragment.SentenceID = parserID
				}
			}
		}
	}
	raw, _ := json.Marshal(document)
	if findings == nil {
		findings = []onlinebulletins.ReviewIssue{}
	}
	issues, _ := json.Marshal(findings)
	if _, err := db.Exec(`INSERT INTO hhc_web.online_bulletin_revision(document_id,issue_id,series,content_locale,revision,document_json,source_asset_checksum,template_version,extractor_version,content_hash,renderer_version,renderer_artifact_sha256,review_issues,created_by) SELECT document_id,issue_id,series,content_locale,$2,$3,source_asset_checksum,template_version,extractor_version,content_hash,renderer_version,renderer_artifact_sha256,$4,'bulletin-extractor' FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=$5`, documentID, revision+1, raw, issues, revision); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_document SET base_revision=$2,local_revision=$2,version=version+1 WHERE id=$1`, documentID, revision+1); err != nil {
		t.Fatal(err)
	}
}

func TestSaveOnlineDraftCreatesImmutableRevisionAndLayoutJob(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	ctx := context.Background()
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	components := state.Local.Document.Components
	components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = "修正文字。"
	input := onlinebulletins.SaveDraftInput{CanonicalVersion: 1, OnlineVersion: 2, Components: components}
	repo = repo.WithAudit(NewAuditOutbox(db))
	if _, err = repo.SaveOnlineBulletinDraft(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrAuditUnavailable) {
		t.Fatal("unaudited save accepted", err)
	}
	ctx = bulletinAuditContext()
	bad := input
	bad.CanonicalVersion++
	if _, err = repo.SaveOnlineBulletinDraft(ctx, source.Canonical.IssueID, "general", "zh-Hant", bad, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("stale canonical metadata accepted", err)
	}
	bad = input
	bad.Components = []onlinebulletins.Component{}
	if _, err = repo.SaveOnlineBulletinDraft(ctx, source.Canonical.IssueID, "general", "zh-Hant", bad, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrInvalid) {
		t.Fatal("invalid components accepted", err)
	}
	saved, err := repo.SaveOnlineBulletinDraft(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now)
	if err != nil || saved.Version != 3 || saved.Revision != 2 || saved.LayoutJobID == "" {
		t.Fatal("save", err)
	}
	if _, err = repo.SaveOnlineBulletinDraft(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("stale save accepted", err)
	}
	var raw []byte
	if err = db.QueryRow(`SELECT document_json FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=1`, source.DocumentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var original onlinebulletins.Document
	if err = json.Unmarshal(raw, &original); err != nil || original.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text != "文" {
		t.Fatal("original revision changed", err)
	}
	state, err = repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || state.Local.Revision != 2 || state.DraftStatus != "reviewRequired" || *state.BaseRevision != 1 || state.Local.Document.LayoutManifest.LayoutValidationHash != "" {
		t.Fatal("draft not invalidated", err)
	}
	job, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
	if err != nil || job == nil || job.ID != saved.LayoutJobID || job.Revision != 2 || job.Canonical != source.Canonical.Metadata || job.Document.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text != "修正文字。" {
		t.Fatal("wrong immutable layout input", err)
	}
	var count int
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.outbox_event WHERE destination='audit-log' AND payload_json->>'resourceId'=$1`, source.DocumentID)
	})
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE destination='audit-log' AND payload_json->>'resourceId'=$1 AND payload_json->>'action'='cms.bulletin.online.update'`, source.DocumentID).Scan(&count); err != nil || count != 1 {
		t.Fatal("draft audit not atomic", err)
	}
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_mapping WHERE document_id=$1`, source.DocumentID).Scan(&count); err != nil || count != 0 {
		t.Fatal("draft created publication checkpoint", err)
	}
}

func TestQueueOnlineExtractionPreservesLocalAndRequiresCAS(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	ctx := context.Background()
	if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET pdf_asset_id='replacement' WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
		t.Fatal(err)
	}
	input := onlinebulletins.QueueExtractionInput{CanonicalVersion: 1, OnlineVersion: 2, SourceAssetID: "replacement", SourceChecksum: strings.Repeat("b", 64)}
	queued, err := repo.QueueOnlineBulletinExtraction(ctx, source.Canonical.IssueID, input, "admin", now.Add(2*time.Second))
	if err != nil || queued.Version != 3 || queued.Job.Status != "queued" {
		t.Fatal("queue", err)
	}
	if _, err = repo.QueueOnlineBulletinExtraction(ctx, source.Canonical.IssueID, input, "admin", now); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("stale Online accepted", err)
	}
	input.OnlineVersion = 3
	replayed, err := repo.QueueOnlineBulletinExtraction(ctx, source.Canonical.IssueID, input, "admin", now)
	if err != nil || replayed.Job.ID != queued.Job.ID || replayed.Version != 3 {
		t.Fatal("duplicate job", err)
	}
	input.Retry = true
	if _, err = repo.QueueOnlineBulletinExtraction(ctx, source.Canonical.IssueID, input, "admin", now); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("active job reset", err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET status='failed',attempts=3,last_error_code='attempt_limit' WHERE id=$1`, queued.Job.ID); err != nil {
		t.Fatal(err)
	}
	retried, err := repo.QueueOnlineBulletinExtraction(ctx, source.Canonical.IssueID, input, "admin", now)
	if err != nil || retried.Version != 4 || retried.Job.ID != queued.Job.ID || retried.Job.Attempts != 0 || retried.Job.Status != "queued" {
		t.Fatal("explicit retry", err)
	}
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || state.Local == nil || state.Local.Revision != 1 || state.BaseRevision == nil || *state.BaseRevision != 1 || state.IncomingRevision != nil || state.PDFStatus != "draft" {
		t.Fatal("queue overwrote Local or PDF", err)
	}
	var snapshot bool
	if err = db.QueryRow(`SELECT snapshot_json IS NOT NULL FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, source.ID).Scan(&snapshot); err != nil || !snapshot {
		t.Fatal("old snapshot lost", err)
	}
}

func TestQueueOnlineExtractionInitialSourceAndAuditAreAtomic(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	jobID := seedOnlineExtraction(t, db, now)
	var issueID string
	if err := db.QueryRow(`SELECT issue_id::text FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, jobID).Scan(&issueID); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{`DELETE FROM hhc_web.online_bulletin_extraction_job WHERE issue_id=$1`, `DELETE FROM hhc_web.online_bulletin_document WHERE issue_id=$1`} {
		if _, err := db.Exec(query, issueID); err != nil {
			t.Fatal(err)
		}
	}
	repo := New(db).WithAudit(NewAuditOutbox(db))
	input := onlinebulletins.QueueExtractionInput{CanonicalVersion: 1, SourceAssetID: "test-asset", SourceChecksum: strings.Repeat("a", 64)}
	if _, err := repo.QueueOnlineBulletinExtraction(context.Background(), issueID, input, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrAuditUnavailable) {
		t.Fatal("unaudited mutation", err)
	}
	ctx := bulletinAuditContext()
	bad := input
	bad.SourceAssetID = "replaced-source"
	if _, err := repo.QueueOnlineBulletinExtraction(ctx, issueID, bad, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("stale source accepted", err)
	}
	bad = input
	bad.CanonicalVersion = 2
	if _, err := repo.QueueOnlineBulletinExtraction(ctx, issueID, bad, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("stale canonical metadata accepted", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_document WHERE issue_id=$1`, issueID).Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected start left document", err)
	}
	queued, err := repo.QueueOnlineBulletinExtraction(ctx, issueID, input, postgresBulletinAuditActor, now)
	if err != nil || queued.Version != 1 {
		t.Fatal("initial start", err)
	}
	var authorizer, request string
	if err = db.QueryRow(`SELECT authorizer_id::text,authorizer_request_id::text FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, queued.Job.ID).Scan(&authorizer, &request); err != nil || authorizer != postgresBulletinAuditActor || request != postgresBulletinAuditRequest {
		t.Fatal("automatic conversion lost original authorization provenance", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.outbox_event WHERE destination='audit-log' AND payload_json->>'resourceId'=$1`, queued.DocumentID)
	})
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE destination='audit-log' AND payload_json->>'resourceId'=$1 AND payload_json->>'action'='cms.bulletin.online.extract'`, queued.DocumentID).Scan(&count); err != nil || count != 1 {
		t.Fatal("missing atomic audit", err)
	}
	claimed, err := repo.ClaimOnlineBulletinExtraction(ctx, now, time.Minute, 3)
	if err != nil || claimed == nil || claimed.ID != queued.Job.ID || claimed.SourceAssetChecksum != input.SourceChecksum || claimed.ExtractorVersion != onlinebulletins.ExtractorVersion {
		t.Fatal("worker cannot consume queued source", err)
	}
}

func TestQueueOnlineExtractionRefreshesPreStableKindCandidate(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	ctx := context.Background()
	repo, source := seedOnlineLayout(t, db, now)
	if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET extractor_version=$2 WHERE id=$1`, source.ID, "poppler-"+onlinebulletins.PopplerVersion+"-weekly-v1.13"); err != nil {
		t.Fatal(err)
	}
	before, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	input := onlinebulletins.QueueExtractionInput{CanonicalVersion: 1, OnlineVersion: before.Version, SourceAssetID: "test-asset", SourceChecksum: strings.Repeat("a", 64)}
	queued, err := repo.QueueOnlineBulletinExtraction(ctx, source.Canonical.IssueID, input, "admin", now)
	if err != nil || queued.Job.ID == source.ID || queued.Job.Status != "queued" {
		t.Fatalf("reused pre-stable-kind candidate: %+v %v", queued, err)
	}
	after, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || !reflect.DeepEqual(before.Local, after.Local) || !reflect.DeepEqual(before.BaseRevision, after.BaseRevision) || !reflect.DeepEqual(before.IncomingRevision, after.IncomingRevision) || !reflect.DeepEqual(before.PublishedRevision, after.PublishedRevision) {
		t.Fatal("queue changed saved content or revision pointers", err)
	}
	input.OnlineVersion = queued.Version
	again, err := repo.QueueOnlineBulletinExtraction(ctx, source.Canonical.IssueID, input, "admin", now)
	if err != nil || again.Job.ID != queued.Job.ID || again.Version != queued.Version {
		t.Fatal("repeat request duplicated current extraction", err)
	}
}

func TestQueueOnlineExtractionRetriesPriorParserFailureUnderCurrentCAS(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	input := onlinebulletins.QueueExtractionInput{CanonicalVersion: 1, OnlineVersion: 2, SourceAssetID: "test-asset", SourceChecksum: strings.Repeat("a", 64), Retry: true}
	if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET extractor_version='weekly-immutable-previous',status='failed' WHERE id=$1`, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.QueueOnlineBulletinExtraction(context.Background(), source.Canonical.IssueID, input, "admin", now); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("immutable prior job accepted", err)
	}
	input.Retry = false
	previous, err := repo.QueueOnlineBulletinExtraction(context.Background(), source.Canonical.IssueID, input, "admin", now)
	if err != nil {
		t.Fatal("seed snapshot-free extraction", err)
	}
	input.OnlineVersion, input.Retry = previous.Version, true
	if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET extractor_version='weekly-previous' WHERE id=$1`, previous.Job.ID); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{"queued", "parsing"} {
		if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET status=$2 WHERE id=$1`, previous.Job.ID, status); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.QueueOnlineBulletinExtraction(context.Background(), source.Canonical.IssueID, input, "admin", now); !errors.Is(err, bulletins.ErrConflict) {
			t.Fatal("active prior job accepted", status, err)
		}
	}
	if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET status='failed' WHERE id=$1`, previous.Job.ID); err != nil {
		t.Fatal(err)
	}
	bad := input
	bad.OnlineVersion++
	if _, err := repo.QueueOnlineBulletinExtraction(context.Background(), source.Canonical.IssueID, bad, "admin", now); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("upgrade bypassed Online CAS", err)
	}
	bad = input
	bad.SourceChecksum = strings.Repeat("b", 64)
	if _, err := repo.QueueOnlineBulletinExtraction(context.Background(), source.Canonical.IssueID, bad, "admin", now); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("upgrade retried a different source", err)
	}
	queued, err := repo.QueueOnlineBulletinExtraction(context.Background(), source.Canonical.IssueID, input, "admin", now)
	if err != nil || queued.Job.ID == previous.Job.ID || queued.Job.Status != "queued" || queued.Version != 4 {
		t.Fatal("failed prior parser did not queue the current version", queued, err)
	}
	var previousSuperseded bool
	var extractor string
	if err = db.QueryRow(`SELECT superseded FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, previous.Job.ID).Scan(&previousSuperseded); err != nil || !previousSuperseded {
		t.Fatal("old parser lease not fenced", err)
	}
	if err = db.QueryRow(`SELECT extractor_version FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, queued.Job.ID).Scan(&extractor); err != nil || extractor != onlinebulletins.ExtractorVersion {
		t.Fatal("retry retained the old parser", err)
	}
	input.OnlineVersion = queued.Version
	if _, err = repo.QueueOnlineBulletinExtraction(context.Background(), source.Canonical.IssueID, input, "admin", now); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("upgrade reset the new active job", err)
	}
	state, err := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || state.Local == nil || state.Local.Revision != 1 || state.BaseRevision == nil || *state.BaseRevision != 1 || state.IncomingRevision != nil || state.PublishedRevision != nil || state.Version != 4 {
		t.Fatal("parser retry overwrote content pointers", err)
	}
}

func TestOnlineAdminStateExposesReviewWithoutInventingDocument(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	seedOnlineExtraction(t, db, now)
	repo := New(db)
	job, err := repo.ClaimOnlineBulletinExtraction(context.Background(), now, time.Minute, 3)
	if err != nil || job == nil {
		t.Fatal("claim", err)
	}
	candidate := extractionCandidate(*job)
	candidate.Document = nil
	issues := []onlinebulletins.ReviewIssue{{Code: "unsupported_template", Blocking: true}}
	if err = repo.CompleteOnlineBulletinExtraction(context.Background(), *job, candidate, issues, now); err != nil {
		t.Fatal(err)
	}
	state, err := repo.OnlineBulletinState(context.Background(), job.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || state.Local != nil || state.Extraction == nil || len(state.Extraction.ReviewIssues) != 1 || state.Extraction.ReviewIssues[0].Code != "unsupported_template" {
		t.Fatal("review reason lost", err)
	}
}

func TestOnlineAdminStateSeparatesDraftPublicationAndWorkerFailure(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	ctx := context.Background()
	if _, err := repo.QueueOnlineBulletinLayout(ctx, source.DocumentID, 2, now); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 1)
	if err != nil || job == nil {
		t.Fatal("layout claim", err)
	}
	if err = repo.FailOnlineBulletinLayout(ctx, *job, "layout_requires_split", nil, now, 1); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO hhc_web.online_bulletin_publication(document_id,revision,canonical_metadata,created_by) VALUES($1,1,'{"title":"published title","subtitle":"","issueNumber":123,"date":"2099-01-01"}','test')`, source.DocumentID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_document SET published_revision=1,metadata_sync_pending=true WHERE id=$1`, source.DocumentID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE id=$1`, source.DocumentID)
		_, _ = db.Exec(`DELETE FROM hhc_web.online_bulletin_publication WHERE document_id=$1`, source.DocumentID)
	})
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	if state.DocumentID != source.DocumentID || state.Version != 3 || state.CanonicalVersion != 1 || state.PDFStatus != "draft" || state.Local == nil || state.Local.Revision != 1 || state.Local.Document.IssueID != source.Canonical.IssueID {
		t.Fatal("incorrect state identity or independent PDF state")
	}
	if state.Layout == nil || state.Layout.Status != "failed" || state.Layout.ErrorCode != "layout_requires_split" || state.Layout.Attempts != 1 {
		t.Fatal("worker failure is hidden")
	}
	if state.Extraction == nil || state.Extraction.Status != "review_ready" || state.PublishedMetadata == nil || state.PublishedMetadata.Title != "published title" || state.CanonicalMetadata.Title != "test title" || !state.MetadataSyncPending {
		t.Fatal("publication metadata was mixed with current canonical metadata")
	}
	if _, err = repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "children", "zh-Hant"); !errors.Is(err, bulletins.ErrNotFound) {
		t.Fatal("cross-series state leaked", err)
	}
}

func TestOnlineAdminStateBeforeExtraction(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	jobID := seedOnlineExtraction(t, db, now)
	var issueID, documentID string
	if err := db.QueryRow(`SELECT issue_id::text,document_id::text FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, jobID).Scan(&issueID, &documentID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET status='failed',attempts=3,last_error_code='attempt_limit' WHERE id=$1`, jobID); err != nil {
		t.Fatal(err)
	}
	failed, err := New(db).OnlineBulletinState(context.Background(), issueID, "general", "zh-Hant")
	if err != nil || failed.DraftStatus != "extractionFailed" || failed.Extraction == nil || failed.Extraction.ErrorCode != "attempt_limit" {
		t.Fatal("failed initial extraction looks active", err)
	}
	if _, err := db.Exec(`DELETE FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, jobID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DELETE FROM hhc_web.online_bulletin_document WHERE id=$1`, documentID); err != nil {
		t.Fatal(err)
	}
	state, err := New(db).OnlineBulletinState(context.Background(), issueID, "general", "zh-Hant")
	if err != nil || state.DocumentID != "" || state.Version != 0 || state.DraftStatus != "notStarted" || state.Local != nil || state.Extraction != nil || state.Layout != nil {
		t.Fatal("unstarted edition not represented", err)
	}
}
