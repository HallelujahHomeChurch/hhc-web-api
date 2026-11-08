package legal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

const page = `{"schemaVersion":1,"template":"legal.v1","data":{"heroTitle":"Policy","heroSubtitle":"","updatedAtLabel":"Updated","updatedAt":"2026年9月30日","intro":"Notice","sections":[{"title":"Purpose","body":["Private marker"]}]}}`

type repoStub struct {
	saved     bool
	draft     Draft
	snapshots []Snapshot
}

func (r *repoStub) Draft(context.Context, Scope) (Draft, error) { return r.draft, nil }
func (r *repoStub) SaveDraft(_ context.Context, d Draft, _ int64, _ string) (Draft, error) {
	r.saved = true
	r.draft = d
	return d, nil
}
func (r *repoStub) Publish(_ context.Context, d Draft, _ int64, _ string) ([]Snapshot, error) {
	r.saved = true
	return r.snapshots, nil
}
func (r *repoStub) Current(_ context.Context, _ Scope, _ string) (Snapshot, error) {
	if len(r.snapshots) == 0 {
		return Snapshot{}, ErrNotFound
	}
	return r.snapshots[0], nil
}
func (r *repoStub) Resolve(_ context.Context, _ string) (Snapshot, error) {
	return r.Current(context.Background(), Common, "en")
}
func validDraft() Draft {
	translations := map[string]Documents{}
	for _, locale := range Locales {
		translations[locale] = Documents{Terms: json.RawMessage(page), Privacy: json.RawMessage(page)}
	}
	return Draft{Scope: VerifiedMember, TermsVersion: "member-terms-v1", PrivacyNoticeVersion: "member-privacy-v1", CommonTermsVersion: "terms-v1", CommonPrivacyNoticeVersion: "privacy-v1", Translations: translations, Version: 1}
}
func TestLegalDraftRequiresCompleteValidatedContent(t *testing.T) {
	repo := &repoStub{}
	service := NewService(repo)
	d := validDraft()
	delete(d.Translations, "ko")
	if _, err := service.SaveDraft(context.Background(), d, 1, "actor"); !errors.Is(err, ErrInvalid) || repo.saved {
		t.Fatalf("invalid stored=%v err=%v", repo.saved, err)
	}
	d = validDraft()
	d.Translations["ko"] = Documents{Terms: json.RawMessage(`{"private":"unvalidated"}`), Privacy: json.RawMessage(page)}
	if _, err := service.SaveDraft(context.Background(), d, 1, "actor"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err=%v", err)
	}
	d = validDraft()
	d.Scope = Scope("public-member")
	if _, err := service.SaveDraft(context.Background(), d, 1, "actor"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err=%v", err)
	}
}
func TestCanonicalLegalHashesAndSnapshotValidation(t *testing.T) {
	d := validDraft()
	snapshots, err := BuildSnapshots(d)
	if err != nil || len(snapshots) != 5 {
		t.Fatalf("snapshots=%v err=%v", snapshots, err)
	}
	for _, s := range snapshots {
		if err := ValidateSnapshot(s); err != nil {
			t.Fatal(err)
		}
		s.Documents.Terms = json.RawMessage(`{}`)
		if ValidateSnapshot(s) == nil {
			t.Fatal("tampered snapshot accepted")
		}
	}
}

func TestCurrentRejectsWrongScopeOrLocale(t *testing.T) {
	snapshots, err := BuildSnapshots(validDraft())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(&repoStub{snapshots: snapshots})
	if _, err := service.Current(context.Background(), Common, snapshots[0].Manifest.Locale); err == nil {
		t.Fatal("member snapshot returned through common surface")
	}
	if _, err := service.Current(context.Background(), VerifiedMember, "en"); err == nil {
		t.Fatal("wrong locale returned")
	}
}
