package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestSimplifiedOnlineOnlyPublicationAndLocaleIsolation(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	now := time.Now().UTC()
	repo, source := seedSimplifiedSource(t, db, now)
	service := bulletins.NewService(repo, func() time.Time { return now })
	queued, err := repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: 1, SourceOnlineVersion: 5, SourceRevision: 2}, "admin", now)
	if err != nil {
		t.Fatal(err)
	}
	job, err := repo.ClaimOnlineBulletinConversion(ctx, now, time.Minute, 3)
	if err != nil || job == nil {
		t.Fatal(err)
	}
	draft := job.Source
	draft.ContentLocale = "zh-Hans"
	draft.TemplateVersion = "v2"
	draft.LayoutManifest.TemplateVersion = "v2"
	draft.LayoutManifest.RendererVersion = "v2"
	if err = repo.CompleteOnlineBulletinConversion(ctx, *job, draft, job.SourceMetadata, now); err != nil {
		t.Fatal(err)
	}
	state, err := service.OnlineState(ctx, source.Canonical.IssueID, "general", "zh-Hans")
	if err != nil {
		t.Fatal("Hans editor unavailable", err)
	}
	components := state.Local.Document.Components
	components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = "人工简中修订。"
	sourceRevision := state.Local.Revision
	_, err = service.SaveOnlineDraft(ctx, source.Canonical.IssueID, "general", "zh-Hans", onlinebulletins.SaveDraftInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: state.Version, Components: components}, "admin")
	if !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("manual Hans edit must be rejected", err)
	}
	hant, err := service.OnlineState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || hant.Version != 5 || hant.Local.Document.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text != "文" {
		t.Fatal("Hans save mutated Hant", err)
	}
	layout, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
	if err != nil || layout == nil || layout.DocumentID != queued.DocumentID {
		t.Fatal("Hans layout", err)
	}
	if err = repo.CompleteOnlineBulletinLayout(ctx, *layout, measuredLayout(t, *layout), now); err != nil {
		t.Fatal(err)
	}
	state, err = service.OnlineState(ctx, source.Canonical.IssueID, "general", "zh-Hans")
	if err != nil || state.Local.Revision <= sourceRevision {
		t.Fatal("measured Hans state", err)
	}
	confirmed, err := service.ConfirmOnline(ctx, source.Canonical.IssueID, "general", "zh-Hans", onlinebulletins.ConfirmInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: state.Version, Revision: state.Local.Revision, PageCount: 4, LayoutValidationHash: state.Local.Document.LayoutManifest.LayoutValidationHash, AcceptedIssues: state.Local.ReviewIssues}, "admin")
	if err != nil {
		t.Fatal("confirm Hans", err)
	}
	published, err := service.PublishOnline(ctx, source.Canonical.IssueID, "general", "zh-Hans", onlinebulletins.PublishInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: confirmed.Version, Revision: confirmed.Revision}, "admin")
	if err != nil {
		t.Fatal("online-only Hans publish", err)
	}
	t.Cleanup(func() {
		for _, q := range []string{`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE id=$1`, `DELETE FROM hhc_web.online_bulletin_publication WHERE document_id=$1`} {
			if _, err := db.Exec(q, queued.DocumentID); err != nil {
				t.Error(err)
			}
		}
	})
	if published.DocumentID != queued.DocumentID {
		t.Fatal("published wrong language")
	}
	if _, err = service.UnpublishOnline(ctx, source.Canonical.IssueID, "general", "zh-Hans", state.CanonicalVersion, published.Version, "admin"); err != nil {
		t.Fatal("unpublish Hans", err)
	}
}
