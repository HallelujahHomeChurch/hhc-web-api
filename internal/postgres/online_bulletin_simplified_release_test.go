package postgres

import (
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
)

func claimSimplifiedRelease(t *testing.T, db *sql.DB, id string, now time.Time) publication.Event {
	t.Helper()
	var e publication.Event
	err := db.QueryRow(`UPDATE hhc_web.outbox_event SET status='processing',attempts=attempts+1,claimed_until=$2 WHERE id=$1 AND status IN ('pending','processing') RETURNING id::text,event_type,aggregate_id::text,aggregate_version,payload_json,attempts,created_at,claimed_until`, id, now.Add(time.Minute)).Scan(&e.ID, &e.EventType, &e.AggregateID, &e.AggregateVersion, &e.Payload, &e.Attempts, &e.CreatedAt, &e.ClaimedUntil)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestPublishedSimplifiedReleaseUsesFrozenSourceAndPublishesOnce(t *testing.T) {
	for _, scenario := range []string{"clean", "republished-source", "superseded-failure", "draft-edit", "metadata-edit", "unpublished", "layout-failed", "lease-fenced", "audit-failure", "source-withdrawn-before-conversion", "retagged-claim", "retry-conversion", "retry-layout"} {
		t.Run(scenario, func(t *testing.T) {
			db := accessDB(t)
			now := time.Now().UTC()
			ctx := bulletinAuditContext()
			repo, source := seedSimplifiedSource(t, db, now)
			repo = repo.WithAudit(NewAuditOutbox(db))
			t.Cleanup(func() {
				for _, query := range []string{
					`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE issue_id=$1`,
					`DELETE FROM hhc_web.online_bulletin_mapping WHERE document_id IN (SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1)`,
					`DELETE FROM hhc_web.online_bulletin_publication WHERE document_id IN (SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1)`,
					`DELETE FROM hhc_web.outbox_event WHERE aggregate_id IN (SELECT id FROM hhc_web.online_bulletin_document WHERE issue_id=$1) OR payload_json->>'resourceId' IN (SELECT id::text FROM hhc_web.online_bulletin_document WHERE issue_id=$1)`,
					`DELETE FROM hhc_web.bulletin_published_version WHERE issue_id=$1`,
				} {
					if _, err := db.Exec(query, source.Canonical.IssueID); err != nil {
						t.Error(err)
					}
				}
			})
			if _, err := db.Exec(`UPDATE hhc_web.bulletin_version SET status='published' WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO hhc_web.bulletin_published_version(issue_id,series,locale,title,subtitle,asset_id,published_at,version) SELECT issue_id,series,locale,title,subtitle,pdf_asset_id,$2,1 FROM hhc_web.bulletin_version WHERE issue_id=$1`, source.Canonical.IssueID, now); err != nil {
				t.Fatal(err)
			}
			state, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
			if err != nil {
				t.Fatal(err)
			}
			published, err := repo.PublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.PublishInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: state.Version, Revision: state.Local.Revision}, postgresBulletinAuditActor, now)
			if err != nil {
				t.Fatal(err)
			}
			var intentCount int
			if err = db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type=$2`, source.DocumentID, simplifiedReleaseEvent).Scan(&intentCount); err != nil || intentCount != 1 {
				t.Fatal("publication did not enqueue exactly one intent", intentCount, err)
			}
			replay, err := repo.PublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.PublishInput{CanonicalVersion: state.CanonicalVersion, OnlineVersion: published.Version, Revision: published.Revision}, postgresBulletinAuditActor, now)
			if err != nil || replay != published {
				t.Fatal("publication replay", replay, err)
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if err = enqueueSimplifiedRelease(ctx, tx, published, source.Canonical.IssueID, state.CanonicalMetadata, postgresBulletinAuditActor, now); err != nil {
				tx.Rollback()
				t.Fatal(err)
			}
			if err = enqueueSimplifiedRelease(ctx, tx, published, source.Canonical.IssueID, state.CanonicalMetadata, postgresBulletinAuditActor, now); err != nil {
				tx.Rollback()
				t.Fatal("duplicate enqueue", err)
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			var id string
			if err = db.QueryRow(`SELECT id::text FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type=$2`, source.DocumentID, simplifiedReleaseEvent).Scan(&id); err != nil {
				t.Fatal(err)
			}
			if scenario == "republished-source" || scenario == "superseded-failure" {
				current, e := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
				if e != nil {
					t.Fatal(e)
				}
				if scenario == "republished-source" {
					_, err = repo.UnpublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", current.CanonicalVersion, current.Version, postgresBulletinAuditActor, now)
				} else {
					if _, err = db.Exec(`UPDATE hhc_web.outbox_event SET status='failed',last_error='transient_error' WHERE id=$1`, id); err != nil {
						t.Fatal(err)
					}
					// An incoming comparison can advance the counter while Local stays published.
					_, err = db.Exec(`UPDATE hhc_web.online_bulletin_document SET version=version+1 WHERE id=$1`, source.DocumentID)
				}
				if err != nil {
					t.Fatal(err)
				}
				current, err = repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
				if err != nil {
					t.Fatal(err)
				}
				now = now.Add(time.Second)
				published, err = repo.PublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.PublishInput{CanonicalVersion: current.CanonicalVersion, OnlineVersion: current.Version, Revision: current.Local.Revision}, postgresBulletinAuditActor, now)
				if err != nil {
					t.Fatal(err)
				}
				oldID := id
				if err = db.QueryRow(`SELECT id::text FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type=$2 ORDER BY created_at DESC,id DESC LIMIT 1`, source.DocumentID, simplifiedReleaseEvent).Scan(&id); err != nil || id == oldID {
					t.Fatal("republish did not replace cancelled intent", err)
				}
			}
			e := claimSimplifiedRelease(t, db, id, now)
			if scenario == "lease-fenced" {
				if _, err = db.Exec(`UPDATE hhc_web.outbox_event SET attempts=attempts+1 WHERE id=$1`, id); err != nil {
					t.Fatal(err)
				}
				if err = repo.CompleteSimplifiedRelease(ctx, e, now); err != publication.ErrStalePublication {
					t.Fatal("stale claim", err)
				}
				return
			}
			if err = repo.CompleteSimplifiedRelease(ctx, e, now); err != nil {
				t.Fatal("queue", err)
			}
			conversion, err := repo.ClaimOnlineBulletinConversion(ctx, now, time.Minute, 3)
			if err != nil || conversion == nil || conversion.PublicationEventID != id {
				t.Fatal("claim conversion", err)
			}
			if scenario == "retry-conversion" {
				if err = repo.FailOnlineBulletinConversion(ctx, *conversion, "simplified_conversion_failed", nil, now, 1); err != nil {
					t.Fatal(err)
				}
				failed := claimSimplifiedRelease(t, db, id, now.Add(time.Second))
				if err = repo.CompleteSimplifiedRelease(ctx, failed, now.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				hant, e := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
				if e != nil {
					t.Fatal(e)
				}
				hans, e := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hans")
				if e != nil {
					t.Fatal(e)
				}
				if _, err = repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: hant.CanonicalVersion, SourceOnlineVersion: hant.Version, SourceRevision: *hant.PublishedRevision, TargetOnlineVersion: hans.Version, Retry: true}, postgresBulletinAuditActor, now.Add(2*time.Second)); err != nil {
					t.Fatal("resume published intent", err)
				}
				conversion, err = repo.ClaimOnlineBulletinConversion(ctx, now.Add(2*time.Second), time.Minute, 3)
				if err != nil || conversion == nil {
					t.Fatal("retry claim", err)
				}
			}
			if scenario == "draft-edit" {
				if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_document SET local_revision=1,version=version+1 WHERE id=$1`, source.DocumentID); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "metadata-edit" {
				if _, err = db.Exec(`UPDATE hhc_web.bulletin_issue SET issue_date=issue_date+1,version=version+1 WHERE id=$1`, source.Canonical.IssueID); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "source-withdrawn-before-conversion" {
				current, e := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
				if e != nil {
					t.Fatal(e)
				}
				if _, err = repo.UnpublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", current.CanonicalVersion, current.Version, postgresBulletinAuditActor, now); err != nil {
					t.Fatal(err)
				}
				var status, code string
				if err = db.QueryRow(`SELECT status,last_error FROM hhc_web.outbox_event WHERE id=$1`, id).Scan(&status, &code); err != nil || status != "failed" || code != "publication_withdrawn" {
					t.Fatal("unpublish left an active intent", status, code, err)
				}
			}
			if scenario == "retagged-claim" {
				if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_conversion_job SET publication_event_id=NULL WHERE id=$1`, conversion.ID); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "retry-conversion" {
				now = now.Add(3 * time.Second)
			}
			draft := conversion.Source
			draft.ContentLocale = "zh-Hans"
			draft.TemplateVersion = "v2"
			draft.LayoutManifest.TemplateVersion = "v2"
			draft.LayoutManifest.RendererVersion = "v2"
			err = repo.CompleteOnlineBulletinConversion(ctx, *conversion, draft, conversion.SourceMetadata, now)
			if scenario == "source-withdrawn-before-conversion" {
				if !errors.Is(err, onlinebulletins.ErrConversionStale) {
					t.Fatal("withdrawn source accepted", err)
				}
				return
			}
			if scenario == "retagged-claim" {
				if !errors.Is(err, onlinebulletins.ErrExtractionLease) {
					t.Fatal("retagged claim accepted", err)
				}
				return
			}
			if err != nil {
				t.Fatal("convert", err)
			}
			layout, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
			if err != nil || layout == nil {
				t.Fatal("layout", err)
			}
			if scenario == "retry-layout" {
				if err = repo.FailOnlineBulletinLayout(ctx, *layout, "layout_validation_failed", nil, now, 1); err != nil {
					t.Fatal(err)
				}
				failed := claimSimplifiedRelease(t, db, id, now.Add(time.Second))
				if err = repo.CompleteSimplifiedRelease(ctx, failed, now.Add(time.Second)); err != nil {
					t.Fatal(err)
				}
				if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_conversion_job SET publication_event_id=NULL WHERE id=$1`, conversion.ID); err != nil {
					t.Fatal(err)
				}
				hant, e := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
				if e != nil {
					t.Fatal(e)
				}
				hans, e := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hans")
				if e != nil {
					t.Fatal(e)
				}
				if _, err = repo.QueueOnlineBulletinConversion(ctx, source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: hant.CanonicalVersion, SourceOnlineVersion: hant.Version, SourceRevision: *hant.PublishedRevision, TargetOnlineVersion: hans.Version, Retry: true}, postgresBulletinAuditActor, now.Add(2*time.Second)); err != nil {
					t.Fatal("resume layout", err)
				}
				layout, err = repo.ClaimOnlineBulletinLayout(ctx, now.Add(2*time.Second), time.Minute, 3)
				if err != nil || layout == nil {
					t.Fatal("retry layout claim", err)
				}
				now = now.Add(3 * time.Second)
			}
			if scenario == "layout-failed" {
				if err = repo.FailOnlineBulletinLayout(ctx, *layout, "layout_validation_failed", nil, now, 1); err != nil {
					t.Fatal(err)
				}
			} else if err = repo.CompleteOnlineBulletinLayout(ctx, *layout, measuredLayout(t, *layout), now); err != nil {
				t.Fatal(err)
			}
			if scenario == "unpublished" {
				current, e := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
				if e != nil {
					t.Fatal(e)
				}
				if _, err = repo.UnpublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", current.CanonicalVersion, current.Version, postgresBulletinAuditActor, now); err != nil {
					t.Fatal(err)
				}
				var status, code string
				if err = db.QueryRow(`SELECT status,last_error FROM hhc_web.outbox_event WHERE id=$1`, id).Scan(&status, &code); err != nil || status != "failed" || code != "publication_withdrawn" {
					t.Fatal("unpublish left an active intent", status, code, err)
				}
			}
			if scenario == "unpublished" {
				var count int
				if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_publication WHERE document_id=$1`, conversion.DocumentID).Scan(&count); err != nil || count != 0 {
					t.Fatal("withdrawn intent published target", count, err)
				}
				return
			}
			if scenario == "audit-failure" {
				if _, err = db.Exec(`ALTER TABLE hhc_web.outbox_event ADD CONSTRAINT test_simplified_publish_audit CHECK(payload_json->>'action'<>'cms.bulletin.online.publish') NOT VALID`); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Exec(`ALTER TABLE hhc_web.outbox_event DROP CONSTRAINT test_simplified_publish_audit`) })
			}
			e = claimSimplifiedRelease(t, db, id, now.Add(11*time.Second))
			err = repo.CompleteSimplifiedRelease(ctx, e, now.Add(11*time.Second))
			if scenario == "audit-failure" {
				if err == nil {
					t.Fatal("audit failure accepted")
				}
				var n int
				if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_confirmation WHERE document_id=$1`, conversion.DocumentID).Scan(&n); err != nil || n != 0 {
					t.Fatal("confirmation did not roll back", n, err)
				}
			} else if err != nil {
				t.Fatal("publish", err)
			}
			var count int
			if err = db.QueryRow(`SELECT count(*) FROM hhc_web.online_bulletin_publication WHERE document_id=$1`, conversion.DocumentID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 1
			if scenario == "unpublished" || scenario == "layout-failed" || scenario == "audit-failure" {
				want = 0
			}
			if count != want {
				t.Fatal("published count", count, "want", want)
			}
			if scenario == "layout-failed" {
				current, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
				if err != nil {
					t.Fatal(err)
				}
				if _, err = repo.UnpublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", current.CanonicalVersion, current.Version, postgresBulletinAuditActor, now.Add(12*time.Second)); err != nil {
					t.Fatal(err)
				}
				var code string
				if err = db.QueryRow(`SELECT last_error FROM hhc_web.outbox_event WHERE id=$1`, id).Scan(&code); err != nil || code != "publication_withdrawn" {
					t.Fatal("withdrawal left failed intent resumable", code, err)
				}
			}
			if want == 1 {
				target, stateErr := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hans")
				if stateErr != nil || target.Conversion == nil || target.Conversion.Status != "review_ready" || target.PublishedRevision == nil {
					t.Fatal("delivered intent was masked by historical failure", target.Conversion, stateErr)
				}
				if scenario == "superseded-failure" {
					hant, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
					if err != nil {
						t.Fatal(err)
					}
					tx, err := db.Begin()
					if err != nil {
						t.Fatal(err)
					}
					retried, err := repo.retrySimplifiedReleaseTx(ctx, tx, source.Canonical.IssueID, onlinebulletins.QueueSimplifiedInput{CanonicalVersion: hant.CanonicalVersion, SourceOnlineVersion: hant.Version, SourceRevision: *hant.PublishedRevision, TargetOnlineVersion: target.Version, Retry: true}, postgresBulletinAuditActor, now.Add(12*time.Second))
					tx.Rollback()
					if err != nil || retried != nil {
						t.Fatal("old failure revived after newer delivery", retried, err)
					}
				}
				if err = repo.CompleteSimplifiedRelease(ctx, e, now.Add(12*time.Second)); err != nil {
					t.Fatal("replay", err)
				}
				var metadata onlinebulletins.CanonicalMetadata
				var raw []byte
				if err = db.QueryRow(`SELECT canonical_metadata FROM hhc_web.online_bulletin_publication WHERE document_id=$1`, conversion.DocumentID).Scan(&raw); err != nil || json.Unmarshal(raw, &metadata) != nil || metadata.Date != state.CanonicalMetadata.Date {
					t.Fatal("lost frozen metadata", err)
				}
				if scenario == "clean" {
					var next onlinebulletins.Document
					bytes, _ := json.Marshal(draft)
					json.Unmarshal(bytes, &next)
					body := next.Components[1].BodySection.Blocks[0]
					old := body.Sentences[0].ID
					a, b := body.Sentences[0], body.Sentences[0]
					a.ID = "split-a"
					b.ID = "split-b"
					next.Components[1].BodySection.Blocks[0].Sentences = []onlinebulletins.Sentence{a, b}
					for i := range next.LayoutManifest.Pages {
						for j := range next.LayoutManifest.Pages[i].Slots {
							slot := &next.LayoutManifest.Pages[i].Slots[j]
							if slot.BlockID == body.ID {
								slot.Fragments = []onlinebulletins.Fragment{{SentenceID: a.ID, Start: 0, End: 1}, {SentenceID: b.ID, Start: 0, End: 1}}
							}
						}
					}
					if _, e := onlinebulletins.ValidateDocument(next); e != nil {
						t.Fatal("mapping fixture", e)
					}

					// Seed the independently reviewed source checkpoint; test only
					// the inherited mapping policy, not a forged layout acceptance.
					if _, err = db.Exec(`INSERT INTO hhc_web.online_bulletin_revision(document_id,issue_id,series,content_locale,revision,document_json,source_asset_checksum,template_version,extractor_version,content_hash,renderer_version,renderer_artifact_sha256,layout_validation_hash,review_issues,created_by) SELECT document_id,issue_id,series,content_locale,revision+1,document_json,source_asset_checksum,template_version,extractor_version,content_hash,renderer_version,renderer_artifact_sha256,layout_validation_hash,review_issues,created_by FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=$2`, source.DocumentID, published.Revision); err != nil {
						t.Fatal(err)
					}
					if _, err = db.Exec(`INSERT INTO hhc_web.online_bulletin_publication(document_id,revision,canonical_metadata,created_by,created_at) SELECT document_id,revision+1,canonical_metadata,created_by,created_at FROM hhc_web.online_bulletin_publication WHERE document_id=$1 AND revision=$2`, source.DocumentID, published.Revision); err != nil {
						t.Fatal(err)
					}
					if _, err = db.Exec(`INSERT INTO hhc_web.online_bulletin_mapping(document_id,from_revision,to_revision,from_sentence_id,status,to_sentence_ids) VALUES($1,$2,$3,$4,'split','["split-a","split-b"]')`, source.DocumentID, published.Revision, published.Revision+1, old); err != nil {
						t.Fatal(err)
					}
					tx, err := db.Begin()
					if err != nil {
						t.Fatal(err)
					}
					decisions, err := simplifiedReleaseMappings(ctx, tx, conversion.DocumentID, source.DocumentID, published.Revision+1, next)
					tx.Rollback()
					if err != nil || len(decisions) != 1 || decisions[0].Status != "split" {
						t.Fatal("source structural decision not inherited", decisions, err)
					}
				}

			}
		})
	}
}
