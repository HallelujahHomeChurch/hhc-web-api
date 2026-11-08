package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

func seedOnlineExtraction(t *testing.T, db *sql.DB, now time.Time) string {
	t.Helper()
	issue, document, job := platform.NewID(), platform.NewID(), platform.NewID()
	for _, statement := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO hhc_web.bulletin_issue(id,issue_number,issue_date,idempotency_key,created_by,updated_by,created_at,updated_at) VALUES($1::uuid,987654,'2099-12-30',$1::text,'test','test',$2,$2)`, []any{issue, now}},
		{`INSERT INTO hhc_web.bulletin_version(id,issue_id,series,locale,title,subtitle,pdf_asset_id,pdf_file_name,pdf_sha256,created_by,updated_by,created_at,updated_at) VALUES($1,$2,'general','zh-Hant','test title','subtitle','test-asset','test.pdf',$3,'test','test',$4,$4)`, []any{platform.NewID(), issue, strings.Repeat("a", 64), now}},
		{`INSERT INTO hhc_web.online_bulletin_document(id,issue_id,series,content_locale) VALUES($1,$2,'general','zh-Hant')`, []any{document, issue}},
		{`INSERT INTO hhc_web.online_bulletin_extraction_job(id,document_id,issue_id,series,content_locale,source_asset_id,source_asset_checksum,template_version,extractor_version,available_at) VALUES($1,$2,$3,'general','zh-Hant','test-asset',$4,'v1','test-v1',$5)`, []any{job, document, issue, strings.Repeat("a", 64), now}},
	} {
		if _, err := db.Exec(statement.query, statement.args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM hhc_web.online_bulletin_layout_job WHERE document_id=$1`, document); err != nil {
			t.Error(err)
		}
		for _, query := range []string{`DELETE FROM hhc_web.online_bulletin_extraction_job WHERE issue_id=$1`, `UPDATE hhc_web.online_bulletin_document SET base_revision=NULL,local_revision=NULL,incoming_revision=NULL,published_revision=NULL WHERE issue_id=$1`, `DELETE FROM hhc_web.online_bulletin_revision WHERE issue_id=$1`, `DELETE FROM hhc_web.online_bulletin_document WHERE issue_id=$1`, `DELETE FROM hhc_web.bulletin_version WHERE issue_id=$1`, `DELETE FROM hhc_web.bulletin_issue WHERE id=$1`} {
			if _, err := db.Exec(query, issue); err != nil {
				t.Error(err)
			}
		}
	})
	return job
}

func TestOnlineHistoryPreventsIssueAndEditionDeletion(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	ctx := context.Background()
	if _, err := repo.DeleteVersion(ctx, source.Canonical.IssueID, "general", "zh-Hant", 1, "admin", now); !errors.Is(err, bulletins.ErrOnlineDocumentExists) {
		t.Fatalf("edition deletion error=%v", err)
	}
	if err := repo.DeleteIssue(ctx, source.Canonical.IssueID, 1, "admin", now); !errors.Is(err, bulletins.ErrOnlineDocumentExists) {
		t.Fatalf("issue deletion error=%v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_revision WHERE document_id=$1`, source.DocumentID).Scan(&count); err != nil || count != 1 {
		t.Fatal("history changed", count, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1`, source.Canonical.IssueID).Scan(&count); err != nil || count != 0 {
		t.Fatal("rejected deletion enqueued side effects", count, err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.bulletin_issue SET status='published' WHERE id=$1`, source.Canonical.IssueID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET status='published' WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DeleteVersion(ctx, source.Canonical.IssueID, "general", "zh-Hant", 1, "admin", now); !errors.Is(err, bulletins.ErrOnlineDocumentExists) {
		t.Fatalf("published edition lost history conflict: %v", err)
	}
	if err := repo.DeleteIssue(ctx, source.Canonical.IssueID, 1, "admin", now); !errors.Is(err, bulletins.ErrOnlineDocumentExists) {
		t.Fatalf("published issue lost history conflict: %v", err)
	}
}

func TestOnlineExtractionLeaseFencesExpiredWorkersAndBoundsAttempts(t *testing.T) {
	db := accessDB(t)
	repo := New(db)
	ctx := context.Background()
	now := time.Now().UTC()
	jobID := seedOnlineExtraction(t, db, now)
	first, err := repo.ClaimOnlineBulletinExtraction(ctx, now, 120*time.Second, 2)
	if err != nil || first == nil || first.ID != jobID || first.Attempts != 1 || first.Canonical.Metadata.Title != "test title" {
		t.Fatalf("first claim: %#v %v", first, err)
	}
	if next, err := repo.ClaimOnlineBulletinExtraction(ctx, now.Add(time.Second), 120*time.Second, 2); err != nil || next != nil {
		t.Fatal("live lease claimed twice")
	}
	second, err := repo.ClaimOnlineBulletinExtraction(ctx, now.Add(121*time.Second), 120*time.Second, 2)
	if err != nil || second == nil || second.LeaseToken == first.LeaseToken || second.Attempts != 2 {
		t.Fatal("expired lease not reclaimed", err)
	}
	if err := repo.FailOnlineBulletinExtraction(ctx, *first, "source_unavailable", now.Add(122*time.Second), 2); err == nil {
		t.Fatal("stale worker changed new lease")
	}
	if err := repo.FailOnlineBulletinExtraction(ctx, *second, "source_unavailable", now.Add(123*time.Second), 2); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, jobID).Scan(&status); err != nil || status != "failed" {
		t.Fatal("attempt cap ignored", status, err)
	}
}

func extractionCandidate(job onlinebulletins.ExtractionJob) onlinebulletins.DocumentCandidate {
	doc := onlinebulletins.Document{IssueID: job.Canonical.IssueID, Series: "general", ContentLocale: "zh-Hant", SchemaVersion: "1", TemplateVersion: "v1", SourceAssetChecksum: job.SourceAssetChecksum, SourcePageCount: 4}
	doc.LayoutManifest = onlinebulletins.LayoutManifest{TemplateVersion: "v1", RendererVersion: "v1", RendererArtifactSha256: strings.Repeat("b", 64), Assets: []onlinebulletins.Asset{{URL: "/assets/weekly/v1/" + strings.Repeat("c", 64) + ".woff2", SHA256: strings.Repeat("c", 64), Kind: "font", FontRole: "body"}}}
	for _, id := range []string{"cover-page", "body-page", "lyrics-page", "back-page"} {
		doc.Pages = append(doc.Pages, onlinebulletins.Page{ID: id, Width: 595, Height: 842})
		doc.LayoutManifest.Pages = append(doc.LayoutManifest.Pages, onlinebulletins.LayoutPage{PageID: id, Slots: []onlinebulletins.Slot{}})
	}
	block := func(id, component string, page int) onlinebulletins.Block {
		doc.LayoutManifest.Pages[page].Slots = append(doc.LayoutManifest.Pages[page].Slots, onlinebulletins.Slot{ID: "slot-" + id, ComponentID: component, BlockID: id, Box: onlinebulletins.Box{X: .1, Y: .1, Width: .8, Height: .1}, Fragments: []onlinebulletins.Fragment{{SentenceID: "s-" + id, Start: 0, End: 1}}})
		return onlinebulletins.Block{ID: id, Style: onlinebulletins.ParagraphStyle{FontSize: 16, LineHeight: 24}, Sentences: []onlinebulletins.Sentence{{ID: "s-" + id, Spans: []onlinebulletins.Span{{Text: "文", FontRole: "body"}}}}}
	}
	song := block("song", "cover", 0)
	doc.Components = []onlinebulletins.Component{
		{ID: "cover", Type: "cover", Cover: &onlinebulletins.Cover{Welcome: []onlinebulletins.Block{block("welcome", "cover", 0)}, Worship: []onlinebulletins.Item{{ID: "worship", Title: &song, Blocks: []onlinebulletins.Block{}}}, Work: []onlinebulletins.Item{{ID: "work-item", Blocks: []onlinebulletins.Block{block("work", "cover", 0)}}}, WordQuestions: []onlinebulletins.Item{{ID: "question-item", Blocks: []onlinebulletins.Block{block("question", "cover", 0)}}}, WeeklyVerses: []onlinebulletins.Block{block("verse", "cover", 0)}}},
		{ID: "body", Type: "bodySection", BodySection: &onlinebulletins.BodySection{Kind: "sermon", Title: block("title", "body", 1), Contributors: []onlinebulletins.Contributor{{Role: "speaker", Name: block("speaker", "body", 1)}}, Blocks: []onlinebulletins.Block{block("body-text", "body", 1)}}},
		{ID: "lyrics", Type: "hymnLyrics", HymnLyrics: &onlinebulletins.HymnLyrics{Hymns: []onlinebulletins.Hymn{{ID: "hymn", Title: block("hymn-title", "lyrics", 2), Sections: []onlinebulletins.HymnSection{{ID: "stanza", Kind: "verse", Lines: []onlinebulletins.Block{block("line", "lyrics", 2)}}}}}}},
	}
	for _, kind := range []string{"backSummary", "announcements", "victoriesAndPrayers"} {
		doc.Components = append(doc.Components, onlinebulletins.Component{ID: kind, Type: kind, Items: []onlinebulletins.Item{{ID: kind + "-item", Blocks: []onlinebulletins.Block{block(kind+"-text", kind, 3)}}}})
	}
	return onlinebulletins.DocumentCandidate{Document: &doc, Evidence: onlinebulletins.GeometryDocument{SourceSHA256: job.SourceAssetChecksum}}
}

func TestOnlineExtractionCompletionIsAtomicAndReuploadOnlyCreatesIncoming(t *testing.T) {
	db := accessDB(t)
	repo := New(db)
	ctx := context.Background()
	now := time.Now().UTC()
	id := seedOnlineExtraction(t, db, now)
	job, err := repo.ClaimOnlineBulletinExtraction(ctx, now, 120*time.Second, 3)
	if err != nil || job == nil || job.ID != id {
		t.Fatal("claim", err)
	}
	candidate := extractionCandidate(*job)
	candidate.Document.Components[1].BodySection.Title.Sentences[0].Spans[0].Text = "題"
	candidate.Document.Components[1].BodySection.Contributors[0].Name.Sentences[0].Spans[0].Text = "人"
	if _, err := onlinebulletins.ValidateDocument(*candidate.Document); err != nil {
		t.Fatal("invalid fixture", err)
	}
	if err := repo.CompleteOnlineBulletinExtraction(ctx, *job, candidate, nil, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteOnlineBulletinExtraction(ctx, *job, candidate, nil, now.Add(2*time.Second)); err != nil {
		t.Fatal("idempotent completion", err)
	}
	var base, local int64
	var incoming, published sql.NullInt64
	read := func() {
		t.Helper()
		if err := db.QueryRow(`SELECT base_revision,local_revision,incoming_revision,published_revision FROM hhc_web.online_bulletin_document WHERE id=$1`, job.DocumentID).Scan(&base, &local, &incoming, &published); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if base != 1 || local != 1 || incoming.Valid || published.Valid {
		t.Fatal("initial pointers incorrect")
	}
	layout, err := repo.ClaimOnlineBulletinLayout(ctx, now.Add(2*time.Second), 120*time.Second, 3)
	if err != nil || layout == nil || layout.DocumentID != job.DocumentID || layout.Revision != 1 || layout.Canonical != job.Canonical.Metadata || layout.Document.LayoutManifest.LayoutValidationHash != "" {
		t.Fatalf("initial extraction did not queue authoritative layout: %#v %v", layout, err)
	}
	var version, layoutCount int64
	if err = db.QueryRow(`SELECT version,(SELECT count(*) FROM hhc_web.online_bulletin_layout_job WHERE document_id=$1) FROM hhc_web.online_bulletin_document WHERE id=$1`, job.DocumentID).Scan(&version, &layoutCount); err != nil || version != 2 || layoutCount != 1 {
		t.Fatal("initial extraction retry duplicated layout or version", version, layoutCount, err)
	}
	newID := platform.NewID()
	if _, err := db.Exec(`INSERT INTO hhc_web.online_bulletin_extraction_job(id,document_id,issue_id,series,content_locale,source_asset_id,source_asset_checksum,template_version,extractor_version,available_at) VALUES($1,$2,$3,'general','zh-Hant','test-asset',$4,'v1','test-v2',$5)`, newID, job.DocumentID, job.Canonical.IssueID, job.SourceAssetChecksum, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	job, err = repo.ClaimOnlineBulletinExtraction(ctx, now.Add(4*time.Second), 120*time.Second, 3)
	if err != nil || job == nil || job.ID != newID {
		t.Fatal("reupload claim", err)
	}
	// A parser's new positional IDs must not replace a surviving sentence's
	// account annotation identity after a source correction.
	body := candidate.Document.Components[1].BodySection
	body.Blocks[0].ID = "reextracted-body"
	body.Blocks[0].Sentences[0].ID = "reextracted-sentence"
	for i := range candidate.Document.LayoutManifest.Pages[1].Slots {
		slot := &candidate.Document.LayoutManifest.Pages[1].Slots[i]
		if slot.BlockID == "body-text" {
			slot.BlockID = "reextracted-body"
			slot.Fragments[0].SentenceID = "reextracted-sentence"
		}
	}
	if err := repo.CompleteOnlineBulletinExtraction(ctx, *job, candidate, nil, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	read()
	if base != 1 || local != 1 || !incoming.Valid || incoming.Int64 != 2 || published.Valid {
		t.Fatal("reupload modified Local or publication")
	}
	var raw []byte
	if err := db.QueryRow(`SELECT document_json FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=2`, job.DocumentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var normalized onlinebulletins.Document
	if err := json.Unmarshal(raw, &normalized); err != nil {
		t.Fatal(err)
	}
	if got := normalized.Components[1].BodySection.Blocks[0].Sentences[0].ID; got != "s-body-text" {
		t.Fatalf("reupload lost stable sentence ID: %s", got)
	}
	if err := repo.CompleteOnlineBulletinExtraction(ctx, *job, candidate, nil, now.Add(6*time.Second)); err != nil {
		t.Fatal("normalized retry must remain idempotent", err)
	}
	var layoutStatus string
	if err = db.QueryRow(`SELECT status,(SELECT count(*) FROM hhc_web.online_bulletin_layout_job WHERE document_id=$1) FROM hhc_web.online_bulletin_layout_job WHERE id=$2`, job.DocumentID, layout.ID).Scan(&layoutStatus, &layoutCount); err != nil || layoutStatus != "validating" || layoutCount != 1 {
		t.Fatal("incoming extraction replaced current draft layout", layoutStatus, layoutCount, err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET snapshot_json='{}' WHERE id=$1`, newID); err == nil {
		t.Fatal("snapshot mutable")
	}
}

func TestNewParserIncomingPreservesConfirmedPublication(t *testing.T) {
	for _, version := range []string{"v6", "v7", "v8"} {
		t.Run(version, func(t *testing.T) { testNewParserIncomingPreservesConfirmedPublication(t, version) })
	}
}

func testNewParserIncomingPreservesConfirmedPublication(t *testing.T, version string) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint("failed=", fail), func(t *testing.T) {
			db := accessDB(t)
			now, ctx := time.Now().UTC(), context.Background()
			repo, source, _ := seedReaderPublication(t, db, now)
			before, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
			if err != nil || !before.Confirmed || before.PublishedRevision == nil {
				t.Fatal("confirmed publication fixture", err)
			}
			queued, err := repo.QueueOnlineBulletinExtraction(ctx, source.Canonical.IssueID, onlinebulletins.QueueExtractionInput{
				CanonicalVersion: before.CanonicalVersion, OnlineVersion: before.Version,
				SourceAssetID: source.SourceAssetID, SourceChecksum: source.SourceAssetChecksum,
			}, "admin", now)
			if err != nil {
				t.Fatal(err)
			}
			job, err := repo.ClaimOnlineBulletinExtraction(ctx, now, time.Minute, 3)
			if err != nil || job == nil || job.ID != queued.Job.ID || job.ExtractorVersion != onlinebulletins.ExtractorVersion {
				t.Fatal("new parser must claim a distinct same-source job", err)
			}
			if fail {
				err = repo.FailOnlineBulletinExtraction(ctx, *job, "invalid_source", now, 1)
			} else {
				candidate := extractionCandidate(*job)
				candidate.Document.LayoutManifest.RendererVersion = version
				candidate.Document.LayoutManifest.RendererArtifactSha256 = "142cdc601bfb33e2715b46efea7823bac8ee89b1b9620f5186599e34a4363d27"
				if version == "v8" {
					candidate.Document.LayoutManifest.RendererArtifactSha256 = "646c849b47e87cc3607abccd3e92ad60a432bc55d8b1835f97c5b34ca2793c66"
				}
				if version == "v7" {
					candidate.Document.LayoutManifest.RendererArtifactSha256 = "cc55a5bd97412c64f90205f1cf8d9c27530d9fb2fedf2cd5b2e23a2435152c85"
				}
				err = repo.CompleteOnlineBulletinExtraction(ctx, *job, candidate, nil, now)
			}
			if err != nil {
				t.Fatal(err)
			}
			after, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before.Local, after.Local) || !reflect.DeepEqual(before.BaseRevision, after.BaseRevision) || !reflect.DeepEqual(before.PublishedRevision, after.PublishedRevision) || !reflect.DeepEqual(before.PublishedMetadata, after.PublishedMetadata) || before.CanonicalVersion != after.CanonicalVersion || !after.Confirmed {
				t.Fatal("re-extraction changed authored or published state")
			}
			if fail && !reflect.DeepEqual(before.IncomingRevision, after.IncomingRevision) {
				t.Fatal("failed extraction changed Incoming")
			}
			if !fail && (after.IncomingRevision == nil || *after.IncomingRevision <= after.Local.Revision) {
				t.Fatal("successful extraction did not create distinct Incoming")
			}
		})
	}
}

func TestOnlineExtractionLayoutQueueFailureRollsBackInitialDraft(t *testing.T) {
	db := accessDB(t)
	repo := New(db)
	ctx := context.Background()
	now := time.Now().UTC()
	seedOnlineExtraction(t, db, now)
	job, err := repo.ClaimOnlineBulletinExtraction(ctx, now, 120*time.Second, 3)
	if err != nil || job == nil {
		t.Fatal("claim", err)
	}
	if _, err = db.Exec(`ALTER TABLE hhc_web.online_bulletin_layout_job ADD CONSTRAINT test_initial_layout_failure CHECK(document_id::text <> '` + job.DocumentID + `') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`ALTER TABLE hhc_web.online_bulletin_layout_job DROP CONSTRAINT IF EXISTS test_initial_layout_failure`); err != nil {
			t.Error(err)
		}
	})
	if err = repo.CompleteOnlineBulletinExtraction(ctx, *job, extractionCandidate(*job), nil, now.Add(time.Second)); err == nil {
		t.Fatal("layout queue failure ignored")
	}
	var local sql.NullInt64
	var version, count int64
	var status string
	if err = db.QueryRow(`SELECT d.local_revision,d.version,j.status,(SELECT count(*) FROM hhc_web.online_bulletin_revision WHERE document_id=d.id) FROM hhc_web.online_bulletin_document d JOIN hhc_web.online_bulletin_extraction_job j ON j.document_id=d.id WHERE j.id=$1`, job.ID).Scan(&local, &version, &status, &count); err != nil || local.Valid || version != 1 || status != "parsing" || count != 0 {
		t.Fatal("partial initial draft committed", local, version, status, count, err)
	}
	if _, err = db.Exec(`ALTER TABLE hhc_web.online_bulletin_layout_job DROP CONSTRAINT test_initial_layout_failure`); err != nil {
		t.Fatal(err)
	}
	if err = repo.CompleteOnlineBulletinExtraction(ctx, *job, extractionCandidate(*job), nil, now.Add(2*time.Second)); err != nil {
		t.Fatal("atomic extraction retry failed", err)
	}
}

func TestOnlineExtractionRejectsStaleSourceAndKeepsUnsupportedEvidence(t *testing.T) {
	db := accessDB(t)
	repo := New(db)
	ctx := context.Background()
	now := time.Now().UTC()
	id := seedOnlineExtraction(t, db, now)
	job, err := repo.ClaimOnlineBulletinExtraction(ctx, now, 120*time.Second, 3)
	if err != nil || job == nil || job.ID != id {
		t.Fatal("claim", err)
	}
	candidate := extractionCandidate(*job)
	if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET title='changed' WHERE issue_id=$1`, job.Canonical.IssueID); err != nil {
		t.Fatal(err)
	}
	if err := repo.CompleteOnlineBulletinExtraction(ctx, *job, candidate, nil, now.Add(time.Second)); err == nil {
		t.Fatal("stale metadata accepted")
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_revision WHERE document_id=$1`, job.DocumentID).Scan(&count); err != nil || count != 0 {
		t.Fatal("stale revision committed")
	}
	if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET title=$2 WHERE issue_id=$1`, job.Canonical.IssueID, job.Canonical.Metadata.Title); err != nil {
		t.Fatal(err)
	}
	candidate.Document = nil
	if err := repo.CompleteOnlineBulletinExtraction(ctx, *job, candidate, []onlinebulletins.ReviewIssue{{Code: "unsupported_source_structure", Blocking: true}}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	var status string
	var saved bool
	if err := db.QueryRow(`SELECT status,snapshot_json IS NOT NULL FROM hhc_web.online_bulletin_extraction_job WHERE id=$1`, job.ID).Scan(&status, &saved); err != nil || status != "review_ready" || !saved {
		t.Fatal("unsupported evidence discarded")
	}
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_layout_job WHERE document_id=$1`, job.DocumentID).Scan(&count); err != nil || count != 0 {
		t.Fatal("unsupported source queued layout", count, err)
	}
}

func TestOnlineExtractionDeduplicatesRepeatedReviewFindings(t *testing.T) {
	db := accessDB(t)
	repo := New(db)
	ctx := context.Background()
	now := time.Now().UTC()
	seedOnlineExtraction(t, db, now)
	job, err := repo.ClaimOnlineBulletinExtraction(ctx, now, 120*time.Second, 3)
	if err != nil || job == nil {
		t.Fatal("claim", err)
	}
	candidate := extractionCandidate(*job)
	issue := onlinebulletins.ReviewIssue{Code: "unknown_font_role", ComponentID: candidate.Document.Components[0].ID, Blocking: true}
	if err = repo.CompleteOnlineBulletinExtraction(ctx, *job, candidate, []onlinebulletins.ReviewIssue{issue, issue}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err = db.QueryRow(`SELECT review_issues FROM hhc_web.online_bulletin_revision WHERE document_id=$1 ORDER BY revision DESC LIMIT 1`, job.DocumentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var issues []onlinebulletins.ReviewIssue
	if err = json.Unmarshal(raw, &issues); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, saved := range issues {
		if saved == issue {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("same finding stored %d times", count)
	}
}

func TestOnlineExtractionCompletionRollsBackAllWrites(t *testing.T) {
	db := accessDB(t)
	repo := New(db)
	ctx := context.Background()
	now := time.Now().UTC()
	id := seedOnlineExtraction(t, db, now)
	job, err := repo.ClaimOnlineBulletinExtraction(ctx, now, 120*time.Second, 3)
	if err != nil || job == nil || job.ID != id {
		t.Fatal("claim", err)
	}
	// Random test-owned UUID scopes the failure to this job only.
	if _, err := db.Exec(`ALTER TABLE hhc_web.online_bulletin_extraction_job ADD CONSTRAINT test_extraction_completion_failure CHECK(id::text <> '` + id + `' OR status <> 'review_ready') NOT VALID`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.Exec(`ALTER TABLE hhc_web.online_bulletin_extraction_job DROP CONSTRAINT test_extraction_completion_failure`); err != nil {
			t.Error(err)
		}
	})
	if err := repo.CompleteOnlineBulletinExtraction(ctx, *job, extractionCandidate(*job), nil, now.Add(time.Second)); err == nil {
		t.Fatal("forced snapshot failure ignored")
	}
	var count int
	var local sql.NullInt64
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_revision WHERE document_id=$1`, job.DocumentID).Scan(&count); err != nil || count != 0 {
		t.Fatal("revision escaped rollback")
	}
	if err := db.QueryRow(`SELECT local_revision FROM hhc_web.online_bulletin_document WHERE id=$1`, job.DocumentID).Scan(&local); err != nil || local.Valid {
		t.Fatal("Local pointer escaped rollback")
	}
}
