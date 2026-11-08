package postgres

import (
	"errors"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

func TestOnlineComparisonRefreshesSourceTemplateFindings(t *testing.T) {
	for _, scenario := range []string{"resolved", "still invalid", "ambiguous anchors", "canonical resolved", "canonical still invalid", "stale metadata"} {
		t.Run(scenario, func(t *testing.T) {
			db := accessDB(t)
			now := time.Now().UTC()
			old := []onlinebulletins.ReviewIssue{{Code: "fixed_template_source_mismatch", ComponentID: "cover", Blocking: true}, {Code: "unknown_font_role", ComponentID: "cover", Blocking: true}}
			if scenario == "canonical resolved" || scenario == "canonical still invalid" || scenario == "stale metadata" {
				old[0].Code = "canonical_source_mismatch"
			}
			repo := New(db)
			seedOnlineExtraction(t, db, now)
			ctx := bulletinAuditContext()
			source, err := repo.ClaimOnlineBulletinExtraction(ctx, now, 120*time.Second, 3)
			if err != nil || source == nil {
				t.Fatal("initial claim", err)
			}
			t.Cleanup(func() {
				if _, err := db.Exec(`DELETE FROM hhc_web.online_bulletin_layout_job WHERE document_id=$1`, source.DocumentID); err != nil {
					t.Error(err)
				}
			})
			// Distinct paragraphs give reconciliation unambiguous source anchors.
			initial := extractionCandidate(*source)
			if scenario != "ambiguous anchors" {
				cover := initial.Document.Components[0].Cover
				for i, block := range []*onlinebulletins.Block{&cover.Welcome[0], cover.Worship[0].Title, &cover.Work[0].Blocks[0], &cover.WordQuestions[0].Blocks[0], &cover.WeeklyVerses[0]} {
					block.Sentences[0].Spans[0].Text = string(rune('甲' + i))
				}
				body := initial.Document.Components[1].BodySection
				body.Title.Sentences[0].Spans[0].Text = "題"
				body.Contributors[0].Name.Sentences[0].Spans[0].Text = "人"
				initial.Document.Components[2].HymnLyrics.Hymns[0].Title.Sentences[0].Spans[0].Text = "歌"
			}
			if err = repo.CompleteOnlineBulletinExtraction(ctx, *source, initial, old, now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO hhc_web.online_bulletin_extraction_job(id,document_id,issue_id,series,content_locale,source_asset_id,source_asset_checksum,template_version,extractor_version,available_at) VALUES($1,$2,$3,'general','zh-Hant','test-asset',$4,'v1','test-v2',$5)`, platform.NewID(), source.DocumentID, source.Canonical.IssueID, source.SourceAssetChecksum, now.Add(3*time.Second)); err != nil {
				t.Fatal(err)
			}
			job, err := repo.ClaimOnlineBulletinExtraction(ctx, now.Add(4*time.Second), 120*time.Second, 3)
			if err != nil || job == nil {
				t.Fatal("claim", err)
			}
			incoming := []onlinebulletins.ReviewIssue{}
			if scenario == "still invalid" || scenario == "canonical still invalid" {
				incoming = append(incoming, old[0])
			}
			candidate := initial
			if err = repo.CompleteOnlineBulletinExtraction(ctx, *job, candidate, incoming, now.Add(5*time.Second)); err != nil {
				t.Fatal(err)
			}
			canonicalVersion := int64(1)
			if scenario == "stale metadata" {
				if _, err = db.Exec(`UPDATE hhc_web.bulletin_version SET subtitle='changed after extraction' WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
					t.Fatal(err)
				}
				if _, err = db.Exec(`UPDATE hhc_web.bulletin_issue SET version=2 WHERE id=$1`, source.Canonical.IssueID); err != nil {
					t.Fatal(err)
				}
				canonicalVersion = 2
			}
			_, err = repo.ApplyOnlineBulletinComparison(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.CompareInput{CanonicalVersion: canonicalVersion, OnlineVersion: 3, BaseRevision: 1, LocalRevision: 1, IncomingRevision: 2}, "admin", now)
			if scenario == "stale metadata" {
				if !errors.Is(err, bulletins.ErrPrecondition) {
					t.Fatal("stale canonical evidence accepted", err)
				}
				state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
				if err != nil || state.Local.Revision != 1 || state.IncomingRevision == nil || *state.IncomingRevision != 2 || state.Local.ReviewIssues[0] != old[0] {
					t.Fatal("rejected comparison mutated draft or evidence", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
			if err != nil {
				t.Fatal(err)
			}
			found, font := false, false
			for _, issue := range state.Local.ReviewIssues {
				found = found || issue.Code == old[0].Code
				font = font || issue.Code == "unknown_font_role"
			}
			// New source anchors replace old geometry; note mapping is reviewed at publication.
			wantInvalid := scenario == "still invalid" || scenario == "canonical still invalid"
			if found != wantInvalid || font {
				t.Fatalf("template invalid=%v want=%v, stale font review=%v", found, wantInvalid, font)
			}
		})
	}
}

func TestOnlineComparisonCASAndAtomicDraft(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	repo, source := seedOnlineLayout(t, db, now)
	ctx := bulletinAuditContext()
	if _, err := db.Exec(`INSERT INTO hhc_web.online_bulletin_extraction_job(id,document_id,issue_id,series,content_locale,source_asset_id,source_asset_checksum,template_version,extractor_version,available_at) VALUES($1,$2,$3,'general','zh-Hant','test-asset',$4,'v1','test-v2',$5)`, platform.NewID(), source.DocumentID, source.Canonical.IssueID, source.SourceAssetChecksum, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimOnlineBulletinExtraction(ctx, now.Add(4*time.Second), 120*time.Second, 3)
	if err != nil || job == nil {
		t.Fatal("claim", err)
	}
	candidate := extractionCandidate(*job)
	candidate.Document.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = "修正。"
	for i := range candidate.Document.LayoutManifest.Pages[1].Slots {
		if candidate.Document.LayoutManifest.Pages[1].Slots[i].BlockID == "body-text" {
			candidate.Document.LayoutManifest.Pages[1].Slots[i].Fragments[0].End = 3
		}
	}
	if err = repo.CompleteOnlineBulletinExtraction(ctx, *job, candidate, nil, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	comparison, err := repo.OnlineBulletinComparison(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || comparison.Version != 3 || comparison.BaseRevision != 1 || comparison.LocalRevision != 1 || comparison.IncomingRevision != 2 || len(comparison.Units) == 0 {
		t.Fatal("comparison snapshot", comparison, err)
	}
	input := onlinebulletins.CompareInput{CanonicalVersion: 1, OnlineVersion: 3, BaseRevision: 1, LocalRevision: 1, IncomingRevision: 2}
	bad := input
	bad.IncomingRevision = 1
	if _, err = repo.ApplyOnlineBulletinComparison(ctx, source.Canonical.IssueID, "general", "zh-Hant", bad, "admin", now); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("wrong revision accepted", err)
	}
	repo = repo.WithAudit(NewAuditOutbox(db))
	if _, err = repo.ApplyOnlineBulletinComparison(t.Context(), source.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrAuditUnavailable) {
		t.Fatal("unaudited comparison", err)
	}
	saved, err := repo.ApplyOnlineBulletinComparison(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now)
	if err != nil || saved.Version != 4 || saved.Revision != 3 || saved.LayoutJobID == "" {
		t.Fatal("compare save", saved, err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.outbox_event WHERE destination='audit-log' AND payload_json->>'resourceId'=$1`, source.DocumentID)
	})
	if _, err = repo.ApplyOnlineBulletinComparison(ctx, source.Canonical.IssueID, "general", "zh-Hant", input, postgresBulletinAuditActor, now); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("stale comparison accepted", err)
	}
	state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || *state.BaseRevision != 2 || state.IncomingRevision != nil || state.Local.Revision != 3 || state.PublishedRevision != nil || state.Local.Document.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text != "修正。" {
		t.Fatal("wrong comparison result", state, err)
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE destination='audit-log' AND payload_json->>'resourceId'=$1 AND payload_json->>'action'='cms.bulletin.online.compare'`, source.DocumentID).Scan(&count); err != nil || count != 1 {
		t.Fatal("missing atomic audit", count, err)
	}
}
