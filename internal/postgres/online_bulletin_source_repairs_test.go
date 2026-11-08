package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestSourceRepairCatalogAndSaveAreBoundToCurrentImmutableExtraction(t *testing.T) {
	db := accessDB(t)
	repo := New(db).WithAudit(NewAuditOutbox(db))
	ctx, now := bulletinAuditContext(), time.Now().UTC()
	seedOnlineExtraction(t, db, now)
	job, err := repo.ClaimOnlineBulletinExtraction(context.Background(), now, 120*time.Second, 3)
	if err != nil || job == nil {
		t.Fatal("claim", err)
	}
	candidate := extractionCandidate(*job)
	candidate.Evidence.PopplerVersion = onlinebulletins.PopplerVersion
	for i := 1; i <= 4; i++ {
		candidate.Evidence.Pages = append(candidate.Evidence.Pages, onlinebulletins.GeometryPage{Number: i, Width: 595.32, Height: 841.92})
	}
	candidate.Evidence.Pages[1].Texts = []onlinebulletins.GeometryText{{Text: "補回𠮷安。", FontFamily: "MingLiU", FontSize: 14, Box: onlinebulletins.Box{X: .1, Y: .4, Width: .7, Height: .03}}}
	if err := repo.CompleteOnlineBulletinExtraction(ctx, *job, candidate, nil, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	catalog, err := repo.OnlineBulletinSourceParts(ctx, job.Canonical.IssueID)
	if err != nil || len(catalog.Parts) != 1 || !catalog.Parts[0].Available {
		t.Fatal("jsonb snapshot/source lookup", catalog, err)
	}
	locked, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer locked.Rollback()
	if _, err = locked.ExecContext(ctx, `UPDATE hhc_web.online_bulletin_document SET version=version+1 WHERE id=$1`, catalog.DocumentID); err != nil {
		t.Fatal(err)
	}
	readCtx, stop := context.WithTimeout(ctx, time.Second)
	visible, readErr := repo.OnlineBulletinSourceParts(readCtx, job.Canonical.IssueID)
	stop()
	if readErr != nil || visible.Version != catalog.Version || visible.Revision != catalog.Revision {
		t.Fatal("catalog blocked on a writer or mixed snapshots", readErr)
	}
	if err = locked.Rollback(); err != nil {
		t.Fatal(err)
	}
	repair := onlinebulletins.PreparedSourceRepair{Input: onlinebulletins.SourceRepairInput{CanonicalVersion: catalog.CanonicalVersion, OnlineVersion: catalog.Version, Revision: catalog.Revision, SourceChecksum: catalog.SourceChecksum, PartID: catalog.Parts[0].ID, ComponentID: "body"}, Part: catalog.Parts[0]}
	saved, err := repo.SaveOnlineBulletinSourceRepair(ctx, job.Canonical.IssueID, repair, postgresBulletinAuditActor, now.Add(2*time.Second))
	if err != nil || saved.Revision == catalog.Revision || saved.LayoutJobID == "" {
		t.Fatal("repair save", saved, err)
	}
	if _, err := repo.SaveOnlineBulletinSourceRepair(ctx, job.Canonical.IssueID, repair, postgresBulletinAuditActor, now.Add(3*time.Second)); !errors.Is(err, bulletins.ErrPrecondition) {
		t.Fatal("stale repair changed draft", err)
	}
	state, err := repo.OnlineBulletinState(ctx, job.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || state.Local.Revision != saved.Revision || state.Confirmed || state.PublishedRevision != nil || state.Local.Document.LayoutManifest.LayoutValidationHash != "" {
		t.Fatal("repair published or retained stale proof", err)
	}
	if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET pdf_sha256=$2 WHERE issue_id=$1`, job.Canonical.IssueID, strings.Repeat("f", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.OnlineBulletinSourceParts(ctx, job.Canonical.IssueID); !errors.Is(err, bulletins.ErrConflict) {
		t.Fatal("replaced PDF retained old repair evidence", err)
	}
}
