package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/legal"
	"github.com/google/uuid"
)

func TestLegalPublicationIsImmutableAtomicAndPrivate(t *testing.T) {
	db := pageGroupTestDatabase(t)
	ctx := auditclient.WithProvenance(context.Background(), postgresContentAuditActor, "legal-publication-test")
	repo := NewLegalRepository(db)
	service := legal.NewService(repo)
	body := json.RawMessage(`{"schemaVersion":1,"template":"legal.v1","data":{"heroTitle":"Policy","updatedAtLabel":"Updated","updatedAt":"2026年9月30日","intro":"Notice","sections":[{"title":"Purpose","body":["Public notice"]}]}}`)
	docs := map[string]legal.Documents{}
	for _, locale := range legal.Locales {
		docs[locale] = legal.Documents{Terms: body, Privacy: body}
		for _, key := range []string{"terms-of-use", "privacy-policy"} {
			_, err := db.ExecContext(ctx, `INSERT INTO hhc_web.public_projection(projection_key,resource_type,locale,route_path,version,etag,payload_json,updated_at) VALUES($1,'pages',$2,$3,1,'legal-test',jsonb_build_object('content',$4::jsonb),now()) ON CONFLICT(projection_key) DO UPDATE SET payload_json=EXCLUDED.payload_json`, "page:"+locale+":"+key, locale, "/"+key, []byte(body))
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	// Isolated test database; pointers/drafts are mutable, evidence rows are never rewritten.
	if _, err := db.ExecContext(ctx, `TRUNCATE hhc_web.legal_current,hhc_web.legal_draft,hhc_web.legal_document,hhc_web.legal_snapshot,hhc_web.legal_change`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.ExecContext(context.Background(), `TRUNCATE hhc_web.legal_current,hhc_web.legal_draft,hhc_web.legal_document,hhc_web.legal_snapshot,hhc_web.legal_change`)
	})
	version := "test-" + uuid.NewString()
	common := legal.Draft{Scope: legal.Common, TermsVersion: version, PrivacyNoticeVersion: version, Translations: docs}
	if _, err := service.SaveDraft(context.Background(), common, 0, postgresContentAuditActor); err == nil {
		t.Fatal("missing provenance accepted")
	}
	if _, err := service.SaveDraft(ctx, common, 0, postgresContentAuditActor); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(ctx, legal.Common, 2, postgresContentAuditActor); !errors.Is(err, legal.ErrConflict) {
		t.Fatalf("stale publish: %v", err)
	}
	snapshots, err := service.Publish(ctx, legal.Common, 1, postgresContentAuditActor)
	if err != nil || len(snapshots) != 5 {
		t.Fatalf("common publish: %v", err)
	}
	again, err := service.Publish(ctx, legal.Common, 1, postgresContentAuditActor)
	if err != nil || again[0].SnapshotID != snapshots[0].SnapshotID {
		t.Fatalf("idempotent publish: %v", err)
	}
	for _, table := range []string{"legal_document", "legal_snapshot", "legal_change"} {
		if _, err := db.ExecContext(ctx, "DELETE FROM hhc_web."+table); err == nil {
			t.Fatalf("%s evidence deleted", table)
		}
	}
	member := common
	member.Scope = legal.VerifiedMember
	member.CommonTermsVersion = version
	member.CommonPrivacyNoticeVersion = "missing"
	if _, err := service.SaveDraft(ctx, member, 0, postgresContentAuditActor); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(ctx, legal.VerifiedMember, 1, postgresContentAuditActor); !errors.Is(err, legal.ErrConflict) {
		t.Fatalf("missing common dependency: %v", err)
	}
	member.CommonPrivacyNoticeVersion = version
	privateBody := json.RawMessage(`{"schemaVersion":1,"template":"legal.v1","data":{"heroTitle":"Private marker","updatedAtLabel":"Updated","updatedAt":"2026年9月30日","intro":"Notice","sections":[{"title":"Purpose","body":["Private marker"]}]}}`)
	member.Translations = map[string]legal.Documents{}
	for _, locale := range legal.Locales {
		member.Translations[locale] = legal.Documents{Terms: privateBody, Privacy: privateBody}
	}
	if _, err := service.SaveDraft(ctx, member, 1, postgresContentAuditActor); err != nil {
		t.Fatal(err)
	}
	published, err := service.Publish(ctx, legal.VerifiedMember, 2, postgresContentAuditActor)
	if err != nil {
		t.Fatal(err)
	}
	var leaks int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hhc_web.public_projection WHERE payload_json::text LIKE '%Private marker%'`).Scan(&leaks); err != nil || leaks != 0 {
		t.Fatalf("public leaks=%d err=%v", leaks, err)
	}
	resolved, err := service.Resolve(ctx, published[0].SnapshotID)
	if err != nil || resolved.Manifest != published[0].Manifest {
		t.Fatalf("historical resolution: %v", err)
	}
	member.Translations["en"] = legal.Documents{Terms: body, Privacy: body}
	if _, err := service.SaveDraft(ctx, member, 2, postgresContentAuditActor); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Publish(ctx, legal.VerifiedMember, 3, postgresContentAuditActor); !errors.Is(err, legal.ErrConflict) {
		t.Fatalf("version reused for changed text: %v", err)
	}
}
