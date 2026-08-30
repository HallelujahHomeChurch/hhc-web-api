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

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

func TestHomeV2GroupPublishCompletesManifestAndChildrenAtomically(t *testing.T) {
	db := pageGroupTestDatabase(t)
	ctx := context.Background()
	repository := New(db)
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	resetPageGroupTables(t, ctx, db)

	page, err := repository.CreateContent(ctx, content.ModulePages, homeV2IntegrationInput(t, "Home"), "seed", "page:home", now)
	if err != nil {
		t.Fatal(err)
	}
	video, err := repository.CreateContent(ctx, content.ModuleVideos, videoGroupInput("First"), "admin", "video:first", now)
	if err != nil {
		t.Fatal(err)
	}
	removedInput := videoGroupInput("Removed")
	removedInput.YouTubeVideoID = "dQw4w9WgXcQ"
	removedVideo, err := repository.CreateContent(ctx, content.ModuleVideos, removedInput, "admin", "video:removed", now)
	if err != nil {
		t.Fatal(err)
	}
	page, _ = repository.GetContent(ctx, content.ModulePages, page.ID)
	publishing, err := repository.PublishContent(ctx, content.ModulePages, page.ID, page.Version, "admin", now.Add(time.Minute))
	if err != nil || publishing.Status != content.StatusPublishing {
		t.Fatalf("publishing=%#v err=%v", publishing, err)
	}
	event, found, err := repository.Claim(ctx, now.Add(2*time.Minute), 30*time.Second)
	if err != nil || !found || event.EventType != "home.publish.ensure_asset" {
		t.Fatalf("event=%#v found=%v err=%v", event, found, err)
	}
	var payload publication.ContentPublishPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.ManifestSHA256 == "" {
		t.Fatalf("payload=%#v err=%v", payload, err)
	}
	var manifestSHA, manifestStatus string
	if err := db.QueryRowContext(ctx, `SELECT manifest_sha256,publication_status FROM hhc_web.page_publication_manifest WHERE page_id=$1 AND page_version=$2`, page.ID, publishing.Version).Scan(&manifestSHA, &manifestStatus); err != nil || manifestSHA != payload.ManifestSHA256 || manifestStatus != "pending" {
		t.Fatalf("sha=%q status=%q err=%v", manifestSHA, manifestStatus, err)
	}
	if _, err := repository.UpdateContent(ctx, content.ModuleVideos, video.ID, video.Version, videoGroupInput("Blocked"), "admin", now.Add(2*time.Minute)); !errors.Is(err, content.ErrConflict) {
		t.Fatalf("child write err=%v", err)
	}
	if err := repository.CompleteContentPublish(ctx, event, []publication.PublishedAsset{{Usage: "banner", AssetID: "banner-1", GrantID: "grant-1", PublicURL: "/assets/banner-1"}}, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	currentVideo, err := repository.GetContent(ctx, content.ModuleVideos, video.ID)
	if err != nil || currentVideo.Status != content.StatusPublished || currentVideo.Version != video.Version+1 {
		t.Fatalf("video=%#v err=%v", currentVideo, err)
	}
	var pageProjections, videoProjections int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FILTER (WHERE resource_type='pages'),count(*) FILTER (WHERE resource_type='videos') FROM hhc_web.public_projection`).Scan(&pageProjections, &videoProjections); err != nil || pageProjections != 5 || videoProjections != 10 {
		t.Fatalf("page=%d video=%d err=%v", pageProjections, videoProjections, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT publication_status FROM hhc_web.page_publication_manifest WHERE page_id=$1 AND page_version=$2`, page.ID, publishing.Version).Scan(&manifestStatus); err != nil || manifestStatus != "published" {
		t.Fatalf("status=%q err=%v", manifestStatus, err)
	}
	revisions, err := repository.ContentRevisions(ctx, content.ModulePages, page.ID)
	if err != nil || len(revisions) != 1 || revisions[0].Snapshot.Status != content.StatusPublished || revisions[0].Snapshot.PublishedVersion != publishing.Version || revisions[0].Snapshot.PublishedAt == nil {
		t.Fatalf("revisions=%#v err=%v", revisions, err)
	}
	removedVideo, _ = repository.GetContent(ctx, content.ModuleVideos, removedVideo.ID)
	if err := repository.DeleteContent(ctx, content.ModuleVideos, removedVideo.ID, removedVideo.Version, "admin", now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	removedVideo, _ = repository.GetContent(ctx, content.ModuleVideos, removedVideo.ID)
	if removedVideo.Status != content.StatusPendingRemoval {
		t.Fatalf("pending removal=%#v", removedVideo)
	}
	page, _ = repository.GetContent(ctx, content.ModulePages, page.ID)
	publishing, err = repository.PublishContent(ctx, content.ModulePages, page.ID, page.Version, "admin", now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	event, _, _ = repository.Claim(ctx, now.Add(6*time.Minute), 30*time.Second)
	if err := repository.CompleteContentPublish(ctx, event, []publication.PublishedAsset{{Usage: "banner", AssetID: "banner-1", GrantID: "grant-2", PublicURL: "/assets/banner-1"}}, now.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}
	removedVideo, _ = repository.GetContent(ctx, content.ModuleVideos, removedVideo.ID)
	currentVideo, _ = repository.GetContent(ctx, content.ModuleVideos, currentVideo.ID)
	if removedVideo.Status != content.StatusUnpublished || currentVideo.Status != content.StatusPublished {
		t.Fatalf("kept=%#v removed=%#v", currentVideo, removedVideo)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.public_projection WHERE resource_type='videos'`).Scan(&videoProjections); err != nil || videoProjections != 5 {
		t.Fatalf("video projections=%d err=%v", videoProjections, err)
	}
	retire, found, err := repository.Claim(ctx, now.Add(8*time.Minute), 30*time.Second)
	if err != nil || !found || retire.EventType != "asset.grant.revoke" || !strings.Contains(string(retire.Payload), "grant-1") {
		t.Fatalf("retire=%#v found=%v err=%v", retire, found, err)
	}
	if err := repository.Complete(ctx, retire.ID, now.Add(9*time.Minute)); err != nil {
		t.Fatal(err)
	}
	page, _ = repository.GetContent(ctx, content.ModulePages, page.ID)
	unpublished, err := repository.UnpublishContent(ctx, content.ModulePages, page.ID, page.Version, "admin", now.Add(10*time.Minute))
	if err != nil || unpublished.Status != content.StatusUnpublished {
		t.Fatalf("unpublished=%#v err=%v", unpublished, err)
	}
	if rows := homeGroupProjectionRows(t, ctx, db); len(rows) != 0 {
		t.Fatalf("projections=%v", rows)
	}
	currentVideo, _ = repository.GetContent(ctx, content.ModuleVideos, currentVideo.ID)
	if currentVideo.Status != content.StatusUnpublished {
		t.Fatalf("unpublished video=%#v", currentVideo)
	}
	var unpublishManifest []byte
	if err := db.QueryRowContext(ctx, `SELECT manifest_json FROM hhc_web.page_publication_manifest WHERE page_id=$1 AND page_version=$2 AND publication_status='unpublished'`, page.ID, unpublished.Version).Scan(&unpublishManifest); err != nil {
		t.Fatal(err)
	}
	var manifest content.PageGroupManifest
	if err := json.Unmarshal(unpublishManifest, &manifest); err != nil || len(manifest.Items) != 1 || manifest.Items[0].ID != currentVideo.ID || manifest.Items[0].Action != content.GroupActionRemove {
		t.Fatalf("manifest=%#v err=%v", manifest, err)
	}
	var pendingEvents int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.outbox_event WHERE status='pending'`).Scan(&pendingEvents); err != nil || pendingEvents != 1 {
		t.Fatalf("pending events=%d err=%v", pendingEvents, err)
	}
	retire, found, err = repository.Claim(ctx, now.Add(11*time.Minute), 30*time.Second)
	if err != nil || !found || retire.EventType != "asset.grant.revoke" || !strings.Contains(string(retire.Payload), "grant-2") {
		t.Fatalf("retire=%#v found=%v err=%v", retire, found, err)
	}
}

func TestHomeV2GroupStaleCompletionPreservesLiveState(t *testing.T) {
	db := pageGroupTestDatabase(t)
	ctx := context.Background()
	repository := New(db)
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	resetPageGroupTables(t, ctx, db)

	page, _ := repository.CreateContent(ctx, content.ModulePages, homeV2IntegrationInput(t, "Home"), "seed", "page:home", now)
	video, _ := repository.CreateContent(ctx, content.ModuleVideos, videoGroupInput("First"), "admin", "video:first", now)
	page, _ = repository.GetContent(ctx, content.ModulePages, page.ID)
	first, err := repository.PublishContent(ctx, content.ModulePages, page.ID, page.Version, "admin", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	event, _, _ := repository.Claim(ctx, now.Add(2*time.Minute), 30*time.Second)
	if err := repository.CompleteContentPublish(ctx, event, []publication.PublishedAsset{{Usage: "banner", AssetID: "banner-1", GrantID: "grant-live", PublicURL: "/assets/live"}}, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	video, _ = repository.GetContent(ctx, content.ModuleVideos, video.ID)
	video, err = repository.UpdateContent(ctx, content.ModuleVideos, video.ID, video.Version, videoGroupInput("Second"), "admin", now.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	page, _ = repository.GetContent(ctx, content.ModulePages, page.ID)
	page, err = repository.PublishContent(ctx, content.ModulePages, page.ID, page.Version, "admin", now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	event, _, _ = repository.Claim(ctx, now.Add(6*time.Minute), 30*time.Second)
	before := homeGroupProjectionRows(t, ctx, db)
	if _, err := db.ExecContext(ctx, `UPDATE hhc_web.content_entry SET version=version+1 WHERE id=$1`, video.ID); err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteContentPublish(ctx, event, []publication.PublishedAsset{{Usage: "banner", AssetID: "banner-1", GrantID: "grant-unused", PublicURL: "/assets/new"}}, now.Add(7*time.Minute)); !errors.Is(err, publication.ErrStalePublication) {
		t.Fatalf("err=%v", err)
	}
	if after := homeGroupProjectionRows(t, ctx, db); !reflect.DeepEqual(before, after) {
		t.Fatalf("before=%v after=%v", before, after)
	}
	current, err := repository.GetContent(ctx, content.ModulePages, first.ID)
	if err != nil || current.BannerPublicGrantID != "grant-live" {
		t.Fatalf("page=%#v err=%v", current, err)
	}
}

func TestHomeV2GroupFailurePreservesChildrenAndLiveProjections(t *testing.T) {
	db := pageGroupTestDatabase(t)
	ctx := context.Background()
	repository := New(db)
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	resetPageGroupTables(t, ctx, db)

	page, _ := repository.CreateContent(ctx, content.ModulePages, homeV2IntegrationInput(t, "Home"), "seed", "page:home", now)
	video, _ := repository.CreateContent(ctx, content.ModuleVideos, videoGroupInput("Live"), "admin", "video:first", now)
	page, _ = repository.GetContent(ctx, content.ModulePages, page.ID)
	page, err := repository.PublishContent(ctx, content.ModulePages, page.ID, page.Version, "admin", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	event, _, _ := repository.Claim(ctx, now.Add(2*time.Minute), 30*time.Second)
	if err := repository.CompleteContentPublish(ctx, event, []publication.PublishedAsset{{Usage: "banner", AssetID: "banner-1", GrantID: "grant-live", PublicURL: "/assets/live"}}, now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	video, _ = repository.GetContent(ctx, content.ModuleVideos, video.ID)
	video, err = repository.UpdateContent(ctx, content.ModuleVideos, video.ID, video.Version, videoGroupInput("Draft"), "admin", now.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	page, _ = repository.GetContent(ctx, content.ModulePages, page.ID)
	page, err = repository.PublishContent(ctx, content.ModulePages, page.ID, page.Version, "admin", now.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	event, _, _ = repository.Claim(ctx, now.Add(6*time.Minute), 30*time.Second)
	before := homeGroupProjectionRows(t, ctx, db)
	constraint := fmt.Sprintf(`ALTER TABLE hhc_web.public_projection ADD CONSTRAINT test_home_group_late_failure CHECK (projection_key <> 'videos:ko:%s' OR version < %d)`, video.ID, video.Version+1)
	if _, err := db.ExecContext(ctx, `ALTER TABLE hhc_web.public_projection DROP CONSTRAINT IF EXISTS test_home_group_late_failure`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, constraint); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(ctx, `ALTER TABLE hhc_web.public_projection DROP CONSTRAINT IF EXISTS test_home_group_late_failure`); err != nil {
			t.Errorf("drop late-failure constraint: %v", err)
		}
	})
	if err := repository.CompleteContentPublish(ctx, event, []publication.PublishedAsset{{Usage: "banner", AssetID: "banner-1", GrantID: "grant-unused", PublicURL: "/assets/new"}}, now.Add(7*time.Minute)); err == nil {
		t.Fatal("late completion failure unexpectedly succeeded")
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE hhc_web.public_projection DROP CONSTRAINT test_home_group_late_failure`); err != nil {
		t.Fatal(err)
	}
	if after := homeGroupProjectionRows(t, ctx, db); !reflect.DeepEqual(before, after) {
		t.Fatalf("before=%v after late failure=%v", before, after)
	}
	var pendingStatus, eventStatus string
	if err := db.QueryRowContext(ctx, `SELECT publication_status FROM hhc_web.page_publication_manifest WHERE page_id=$1 AND page_version=$2`, page.ID, page.Version).Scan(&pendingStatus); err != nil || pendingStatus != "pending" {
		t.Fatalf("manifest=%q err=%v", pendingStatus, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT status FROM hhc_web.outbox_event WHERE id=$1`, event.ID).Scan(&eventStatus); err != nil || eventStatus != "processing" {
		t.Fatalf("event=%q err=%v", eventStatus, err)
	}
	pendingPage, err := repository.GetContent(ctx, content.ModulePages, page.ID)
	if err != nil || pendingPage.Status != content.StatusPublishing || pendingPage.BannerPublicGrantID != "grant-live" {
		t.Fatalf("page=%#v err=%v", pendingPage, err)
	}
	if err := repository.FailContentPublish(ctx, event, []publication.PublishedAsset{{Usage: "banner", AssetID: "banner-1", GrantID: "grant-unused"}}, "failed", now.Add(7*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if after := homeGroupProjectionRows(t, ctx, db); !reflect.DeepEqual(before, after) {
		t.Fatalf("before=%v after=%v", before, after)
	}
	currentVideo, err := repository.GetContent(ctx, content.ModuleVideos, video.ID)
	if err != nil || currentVideo.Status != content.StatusDraft || currentVideo.Version != video.Version {
		t.Fatalf("video=%#v err=%v", currentVideo, err)
	}
	currentPage, err := repository.GetContent(ctx, content.ModulePages, page.ID)
	if err != nil || currentPage.Status != content.StatusPublishFailed || currentPage.BannerPublicGrantID != "grant-live" {
		t.Fatalf("page=%#v err=%v", currentPage, err)
	}
	var targetStatus string
	if err := db.QueryRowContext(ctx, `SELECT publication_status FROM hhc_web.page_publication_manifest WHERE page_id=$1 AND page_version=$2`, page.ID, page.Version).Scan(&targetStatus); err != nil || targetStatus != "failed" {
		t.Fatalf("manifest=%q err=%v", targetStatus, err)
	}
	compensation, found, err := repository.Claim(ctx, now.Add(8*time.Minute), 30*time.Second)
	if err != nil || !found || compensation.EventType != "asset.grant.revoke" || !strings.Contains(string(compensation.Payload), "grant-unused") {
		t.Fatalf("compensation=%#v found=%v err=%v", compensation, found, err)
	}
	if err := repository.Complete(ctx, compensation.ID, now.Add(9*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repository.Claim(ctx, now.Add(10*time.Minute), 30*time.Second); err != nil || found {
		t.Fatalf("extra compensation found=%v err=%v", found, err)
	}
}

func TestAboutGroupPublish(t *testing.T) {
	db := pageGroupTestDatabase(t)
	ctx := context.Background()
	repository := New(db)
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	resetPageGroupTables(t, ctx, db)

	page, err := repository.CreateContent(ctx, content.ModulePages, aboutGroupPageInput(t, "About"), "seed", "page:about", now)
	if err != nil {
		t.Fatal(err)
	}
	first, err := repository.CreateContent(ctx, content.ModuleHistory, historyGroupInput("1988", "First"), "admin", "history:first", now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.CreateContent(ctx, content.ModuleHistory, historyGroupInput("1990", "Second"), "admin", "history:second", now)
	if err != nil {
		t.Fatal(err)
	}
	page, err = repository.GetContent(ctx, content.ModulePages, page.ID)
	if err != nil {
		t.Fatal(err)
	}
	published, err := repository.PublishContent(ctx, content.ModulePages, page.ID, page.Version, "admin", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if published.Status != content.StatusPublished || published.Version != page.Version+1 {
		t.Fatalf("published=%#v", published)
	}
	for _, child := range []content.Item{first, second} {
		current, err := repository.GetContent(ctx, content.ModuleHistory, child.ID)
		if err != nil || current.Status != content.StatusPublished || current.Version != child.Version+1 {
			t.Fatalf("child=%#v err=%v", current, err)
		}
	}
	var pageProjections, historyProjections int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.public_projection WHERE resource_type='pages' AND resource_id=$1`, page.ID).Scan(&pageProjections); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.public_projection WHERE resource_type='history'`).Scan(&historyProjections); err != nil {
		t.Fatal(err)
	}
	if pageProjections != 5 || historyProjections != 10 {
		t.Fatalf("page projections=%d history projections=%d", pageProjections, historyProjections)
	}
	revisions, err := repository.ContentRevisions(ctx, content.ModulePages, page.ID)
	if err != nil || len(revisions) != 1 || revisions[0].GroupManifest == nil || revisions[0].GroupManifest.PageTargetVersion != published.Version {
		t.Fatalf("revisions=%#v err=%v", revisions, err)
	}
}

func TestAboutGroupRejectsInvalidOrStaleInputWithoutProjectionChanges(t *testing.T) {
	db := pageGroupTestDatabase(t)
	ctx := context.Background()
	repository := New(db)
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)

	for _, test := range []struct {
		name string
		run  func(content.Item, content.Item) error
		want error
	}{
		{name: "missing history locale", run: func(page, _ content.Item) error {
			_, err := repository.PublishContent(ctx, content.ModulePages, page.ID, page.Version, "admin", now)
			return err
		}, want: content.ErrNotPublishable},
		{name: "stale page", run: func(page, _ content.Item) error {
			_, err := repository.PublishContent(ctx, content.ModulePages, page.ID, page.Version-1, "admin", now)
			return err
		}, want: content.ErrPrecondition},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetPageGroupTables(t, ctx, db)
			page, err := repository.CreateContent(ctx, content.ModulePages, aboutGroupPageInput(t, "About"), "seed", "page:about", now)
			if err != nil {
				t.Fatal(err)
			}
			input := historyGroupInput("1988", "History")
			input.Translations = input.Translations[:4]
			child, err := repository.CreateContent(ctx, content.ModuleHistory, input, "admin", "history:one", now)
			if err != nil {
				t.Fatal(err)
			}
			page, err = repository.GetContent(ctx, content.ModulePages, page.ID)
			if err != nil {
				t.Fatal(err)
			}
			before := pageGroupProjectionRows(t, ctx, db)
			if err := test.run(page, child); !errors.Is(err, test.want) {
				t.Fatalf("err=%v want=%v", err, test.want)
			}
			after := pageGroupProjectionRows(t, ctx, db)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("before=%v after=%v", before, after)
			}
			currentPage, err := repository.GetContent(ctx, content.ModulePages, page.ID)
			if err != nil || currentPage.Version != page.Version || currentPage.Status != page.Status {
				t.Fatalf("page=%#v err=%v", currentPage, err)
			}
		})
	}
}

func TestAboutGroupProjectionFailureRollsBackEverything(t *testing.T) {
	db := pageGroupTestDatabase(t)
	ctx := context.Background()
	repository := New(db)
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	resetPageGroupTables(t, ctx, db)

	page, err := repository.CreateContent(ctx, content.ModulePages, aboutGroupPageInput(t, "About"), "seed", "page:about", now)
	if err != nil {
		t.Fatal(err)
	}
	child, err := repository.CreateContent(ctx, content.ModuleHistory, historyGroupInput("1988", "History"), "admin", "history:one", now)
	if err != nil {
		t.Fatal(err)
	}
	page, err = repository.GetContent(ctx, content.ModulePages, page.ID)
	if err != nil {
		t.Fatal(err)
	}
	constraint := fmt.Sprintf(`ALTER TABLE hhc_web.public_projection ADD CONSTRAINT test_about_group_projection_failure CHECK (projection_key <> 'history:ko:%s')`, child.ID)
	if _, err := db.ExecContext(ctx, `ALTER TABLE hhc_web.public_projection DROP CONSTRAINT IF EXISTS test_about_group_projection_failure`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, constraint); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `ALTER TABLE hhc_web.public_projection DROP CONSTRAINT IF EXISTS test_about_group_projection_failure`)
	})

	before := pageGroupProjectionRows(t, ctx, db)
	if _, err := repository.PublishContent(ctx, content.ModulePages, page.ID, page.Version, "admin", now.Add(time.Minute)); err == nil {
		t.Fatal("group publish unexpectedly succeeded")
	}
	if after := pageGroupProjectionRows(t, ctx, db); !reflect.DeepEqual(before, after) {
		t.Fatalf("before=%v after=%v", before, after)
	}
	currentPage, err := repository.GetContent(ctx, content.ModulePages, page.ID)
	if err != nil || currentPage.Version != page.Version || currentPage.Status != content.StatusDraft {
		t.Fatalf("page=%#v err=%v", currentPage, err)
	}
	currentChild, err := repository.GetContent(ctx, content.ModuleHistory, child.ID)
	if err != nil || currentChild.Version != child.Version || currentChild.Status != content.StatusDraft {
		t.Fatalf("child=%#v err=%v", currentChild, err)
	}
	var manifests int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.page_publication_manifest WHERE page_id=$1`, page.ID).Scan(&manifests); err != nil || manifests != 0 {
		t.Fatalf("manifests=%d err=%v", manifests, err)
	}
}

func TestAboutGroupCapturesLegacyBaselineAndUnpublishesAtomically(t *testing.T) {
	db := pageGroupTestDatabase(t)
	ctx := context.Background()
	repository := New(db)
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	resetPageGroupTables(t, ctx, db)

	page, err := repository.CreateContent(ctx, content.ModulePages, aboutGroupPageInput(t, "Legacy About"), "seed", "page:about", now)
	if err != nil {
		t.Fatal(err)
	}
	child, err := repository.CreateContent(ctx, content.ModuleHistory, historyGroupInput("1988", "Legacy History"), "admin", "history:one", now)
	if err != nil {
		t.Fatal(err)
	}
	page, err = repository.GetContent(ctx, content.ModulePages, page.ID)
	if err != nil {
		t.Fatal(err)
	}
	seedLegacyAboutProjections(t, ctx, db, page, child, now)

	changed := historyGroupInput("1988", "Changed History")
	child, err = repository.UpdateContent(ctx, content.ModuleHistory, child.ID, child.Version, changed, "admin", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	page, err = repository.GetContent(ctx, content.ModulePages, page.ID)
	if err != nil {
		t.Fatal(err)
	}
	published, err := repository.PublishContent(ctx, content.ModulePages, page.ID, page.Version, "admin", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var manifests int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.page_publication_manifest WHERE page_id=$1 AND publication_status='published'`, page.ID).Scan(&manifests); err != nil || manifests != 2 {
		t.Fatalf("published manifests=%d err=%v", manifests, err)
	}

	unpublished, err := repository.UnpublishContent(ctx, content.ModulePages, page.ID, published.Version, "admin", now.Add(3*time.Minute))
	if err != nil || unpublished.Status != content.StatusUnpublished || unpublished.Version != published.Version+1 {
		t.Fatalf("unpublished=%#v err=%v", unpublished, err)
	}
	if rows := pageGroupProjectionRows(t, ctx, db); len(rows) != 0 {
		t.Fatalf("projections=%v", rows)
	}
	currentChild, err := repository.GetContent(ctx, content.ModuleHistory, child.ID)
	if err != nil || currentChild.Status != content.StatusUnpublished {
		t.Fatalf("child=%#v err=%v", currentChild, err)
	}
}

func TestAboutGroupRestoreCreatesDraftsWithoutChangingProduction(t *testing.T) {
	db := pageGroupTestDatabase(t)
	ctx := context.Background()
	repository := New(db)
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	resetPageGroupTables(t, ctx, db)

	page, err := repository.CreateContent(ctx, content.ModulePages, aboutGroupPageInput(t, "About One"), "seed", "page:about", now)
	if err != nil {
		t.Fatal(err)
	}
	first, err := repository.CreateContent(ctx, content.ModuleHistory, historyGroupInput("1988", "First One"), "admin", "history:first", now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.CreateContent(ctx, content.ModuleHistory, historyGroupInput("1990", "Second One"), "admin", "history:second", now)
	if err != nil {
		t.Fatal(err)
	}
	page, _ = repository.GetContent(ctx, content.ModulePages, page.ID)
	firstPublish, err := repository.PublishContent(ctx, content.ModulePages, page.ID, page.Version, "admin", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	firstRevision := firstPublish.Version

	first, _ = repository.GetContent(ctx, content.ModuleHistory, first.ID)
	first, err = repository.UpdateContent(ctx, content.ModuleHistory, first.ID, first.Version, historyGroupInput("1988", "First Two"), "admin", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	third, err := repository.CreateContent(ctx, content.ModuleHistory, historyGroupInput("2000", "Third Two"), "admin", "history:third", now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	second, _ = repository.GetContent(ctx, content.ModuleHistory, second.ID)
	if err := repository.DeleteContent(ctx, content.ModuleHistory, second.ID, second.Version, "admin", now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	page, _ = repository.GetContent(ctx, content.ModulePages, page.ID)
	secondPublish, err := repository.PublishContent(ctx, content.ModulePages, page.ID, page.Version, "admin", now.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	production := pageGroupProjectionRows(t, ctx, db)

	restored, err := repository.RestorePageGroup(ctx, page.ID, firstRevision, secondPublish.Version, "admin", now.Add(4*time.Minute))
	if err != nil || restored.Status != content.StatusDraft || restored.Version != secondPublish.Version+1 {
		t.Fatalf("restored=%#v err=%v", restored, err)
	}
	if after := pageGroupProjectionRows(t, ctx, db); !reflect.DeepEqual(production, after) {
		t.Fatalf("production=%v after=%v", production, after)
	}
	first, _ = repository.GetContent(ctx, content.ModuleHistory, first.ID)
	second, _ = repository.GetContent(ctx, content.ModuleHistory, second.ID)
	third, _ = repository.GetContent(ctx, content.ModuleHistory, third.ID)
	if first.Status != content.StatusDraft || groupTranslationTitle(first, "zh-Hant") != "First One zh-Hant" {
		t.Fatalf("first=%#v", first)
	}
	if second.Status != content.StatusDraft || groupTranslationTitle(second, "zh-Hant") != "Second One zh-Hant" {
		t.Fatalf("second=%#v", second)
	}
	if third.Status != content.StatusPendingRemoval {
		t.Fatalf("third=%#v", third)
	}
}

func resetPageGroupTables(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `ALTER TABLE hhc_web.public_projection DROP CONSTRAINT IF EXISTS test_about_group_projection_failure`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `ALTER TABLE hhc_web.public_projection DROP CONSTRAINT IF EXISTS test_home_group_late_failure`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE hhc_web.outbox_event,hhc_web.public_projection,hhc_web.content_revision,hhc_web.content_translation,hhc_web.content_entry CASCADE`); err != nil {
		t.Fatal(err)
	}
}

func aboutGroupPageInput(t *testing.T, title string) content.WriteInput {
	t.Helper()
	payload := json.RawMessage(fmt.Sprintf(`{"schemaVersion":1,"template":"about.v1","data":{"heroTitle":%q,"heroSubtitle":"Subtitle","vision":{"intro":"Vision","imageAlt":"Image","actionsImageAlt":"Actions","sections":[{"eyebrow":"One","title":"Vision","body":"Body"},{"eyebrow":"Two","title":"Goals","body":"Body"},{"eyebrow":"Three","title":"Actions","cards":[{"title":"Share","body":"Body"}]},{"eyebrow":"Four","title":"Commitments","cards":[{"title":"Mission","body":"Body"}]}]},"history":{"scripture":[{"lines":["Scripture"],"cite":"Isaiah"}],"imageAlt":"History","intro":"History intro","title":"History"}}}`, title))
	translations := make([]content.Translation, 0, 5)
	for _, locale := range []string{"zh-Hant", "zh-Hans", "en", "ja", "ko"} {
		translations = append(translations, content.Translation{Locale: locale, Title: title + " " + locale, Summary: "Subtitle", BodyJSON: payload})
	}
	return content.WriteInput{PageKey: "about", PageTemplate: "about.v1", RoutePath: "/about", Indexable: true, Translations: translations}
}

func historyGroupInput(eventDate, title string) content.WriteInput {
	translations := make([]content.Translation, 0, 5)
	for _, locale := range []string{"zh-Hant", "zh-Hans", "en", "ja", "ko"} {
		translations = append(translations, content.Translation{Locale: locale, Title: title + " " + locale, Body: "Body " + locale, DateLabel: eventDate})
	}
	return content.WriteInput{EventDate: eventDate, Translations: translations}
}

func videoGroupInput(title string) content.WriteInput {
	translations := make([]content.Translation, 0, 5)
	for _, locale := range []string{"zh-Hant", "zh-Hans", "en", "ja", "ko"} {
		translations = append(translations, content.Translation{Locale: locale, Title: title + " " + locale})
	}
	return content.WriteInput{YouTubeVideoID: "K3ckFWeSQ-k", HomeEligible: true, Translations: translations}
}

func groupTranslationTitle(item content.Item, locale string) string {
	for _, translation := range item.Translations {
		if translation.Locale == locale {
			return translation.Title
		}
	}
	return ""
}

func pageGroupProjectionRows(t *testing.T, ctx context.Context, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT projection_key,resource_type,COALESCE(resource_id::text,''),locale,route_path,version,etag,payload_json::text,updated_at::text FROM hhc_web.public_projection WHERE resource_type IN ('pages','history') ORDER BY projection_key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var key, resourceType, resourceID, locale, route, etag, payload, updated string
		var version int64
		if err := rows.Scan(&key, &resourceType, &resourceID, &locale, &route, &version, &etag, &payload, &updated); err != nil {
			t.Fatal(err)
		}
		values = append(values, strings.Join([]string{key, resourceType, resourceID, locale, route, fmt.Sprint(version), etag, payload, updated}, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return values
}

func homeGroupProjectionRows(t *testing.T, ctx context.Context, db *sql.DB) []string {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT projection_key,resource_type,COALESCE(resource_id::text,''),locale,route_path,version,etag,payload_json::text,updated_at::text FROM hhc_web.public_projection WHERE resource_type IN ('pages','videos') ORDER BY projection_key`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var key, resourceType, resourceID, locale, route, etag, payload, updated string
		var version int64
		if err := rows.Scan(&key, &resourceType, &resourceID, &locale, &route, &version, &etag, &payload, &updated); err != nil {
			t.Fatal(err)
		}
		values = append(values, strings.Join([]string{key, resourceType, resourceID, locale, route, fmt.Sprint(version), etag, payload, updated}, "|"))
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return values
}

func seedLegacyAboutProjections(t *testing.T, ctx context.Context, db *sql.DB, page, child content.Item, now time.Time) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `UPDATE hhc_web.content_entry SET status='published',published_at=$2 WHERE id IN ($1,$3)`, page.ID, now, child.ID); err != nil {
		t.Fatal(err)
	}
	pageSnapshot, _ := json.Marshal(page)
	if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.content_revision(entry_id,version,snapshot_json,created_by,created_at) VALUES($1,$2,$3,'legacy',$4) ON CONFLICT(entry_id,version) DO UPDATE SET snapshot_json=EXCLUDED.snapshot_json`, page.ID, page.Version, pageSnapshot, now); err != nil {
		t.Fatal(err)
	}
	for _, translation := range page.Translations {
		payload, _ := json.Marshal(content.PublicEditorialPage{PageKey: "about", Template: "about.v1", RoutePath: "/about", Indexable: true, Content: translation.BodyJSON, ResolvedLocale: translation.Locale, AvailableLocales: []string{"zh-Hant", "zh-Hans", "en", "ja", "ko"}, Version: page.Version, PublishedAt: now})
		if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.public_projection(projection_key,resource_type,resource_id,locale,route_path,version,etag,payload_json,updated_at) VALUES($1,'pages',$2,$3,'/about',$4,'legacy',$5,$6)`, "page:"+translation.Locale+":about", page.ID, translation.Locale, page.Version, payload, now); err != nil {
			t.Fatal(err)
		}
	}
	for _, translation := range child.Translations {
		payload, _ := json.Marshal(publicContent(child, translation))
		if _, err := db.ExecContext(ctx, `INSERT INTO hhc_web.public_projection(projection_key,resource_type,resource_id,locale,route_path,version,etag,payload_json,updated_at) VALUES($1,'history',$2,$3,'/history',$4,'legacy',$5,$6)`, "history:"+translation.Locale+":"+child.ID, child.ID, translation.Locale, child.Version, payload, now); err != nil {
			t.Fatal(err)
		}
	}
}
