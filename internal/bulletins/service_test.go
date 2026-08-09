package bulletins

import (
	"context"
	"testing"
	"time"
)

func TestServiceRejectsInvalidIssueAndVersion(t *testing.T) {
	repo := &repositoryStub{}
	service := NewService(repo, time.Now)
	if _, err := service.CreateIssue(context.Background(), CreateIssueInput{IssueNumber: 1, IssueDate: "07/12/2026"}, "user", "key"); err != ErrInvalid {
		t.Fatalf("error=%v", err)
	}
	if _, err := service.PutVersion(context.Background(), "id", 1, PutVersionInput{Locale: "fr", Title: "x", PDFAssetID: "a", PDFFileName: "a.pdf"}, "user"); err != ErrInvalid {
		t.Fatalf("error=%v", err)
	}
}
func TestServiceNormalizesPagination(t *testing.T) {
	repo := &repositoryStub{}
	service := NewService(repo, time.Now)
	_, _ = service.ListIssues(context.Background(), 0, 1000, "", "")
	if repo.page != 1 || repo.pageSize != 100 {
		t.Fatalf("page=%d size=%d", repo.page, repo.pageSize)
	}
}

func TestServiceForwardsPublishNotificationIntent(t *testing.T) {
	repo := &repositoryStub{}
	service := NewService(repo, func() time.Time { return time.Unix(123, 0) })

	if _, err := service.Publish(context.Background(), "issue-1", "zh-Hant", 2, true, "user-1"); err != nil {
		t.Fatal(err)
	}
	if !repo.notifySubscribers {
		t.Fatal("notification intent was not forwarded")
	}
}

func TestServiceGetPublicByNumberValidatesRangeAndLocale(t *testing.T) {
	repo := &repositoryStub{}
	service := NewService(repo, time.Now)

	for _, issueNumber := range []int{0, -1, 2147483648} {
		if _, err := service.GetPublicByNumber(context.Background(), issueNumber, "zh-Hant"); err != ErrInvalid {
			t.Fatalf("issueNumber=%d error=%v", issueNumber, err)
		}
	}
	if _, err := service.GetPublicByNumber(context.Background(), 1, "fr"); err != ErrInvalid {
		t.Fatalf("invalid locale error=%v", err)
	}
	if _, err := service.GetPublicByNumber(context.Background(), 2147483647, "en"); err != nil {
		t.Fatal(err)
	}
	if repo.issueNumber != 2147483647 || repo.locale != "en" {
		t.Fatalf("issueNumber=%d locale=%q", repo.issueNumber, repo.locale)
	}
}

func TestServiceUpdatesIssueMetadata(t *testing.T) {
	repo := &repositoryStub{}
	service := NewService(repo, func() time.Time { return time.Unix(123, 0) })

	if _, err := service.UpdateIssue(context.Background(), "issue-1", 2, UpdateIssueInput{IssueNumber: 1733, IssueDate: "2026-08-09"}, "user-1"); err != nil {
		t.Fatal(err)
	}
	if repo.issueNumber != 1733 || repo.issueDate != "2026-08-09" || repo.expected != 2 {
		t.Fatalf("metadata forwarding=%#v", repo)
	}
}

func TestServiceDeletesIssue(t *testing.T) {
	repo := &repositoryStub{}
	service := NewService(repo, func() time.Time { return time.Unix(123, 0) })

	if err := service.DeleteIssue(context.Background(), "", 1, "user-1"); err != ErrInvalid {
		t.Fatalf("invalid delete error=%v", err)
	}
	if err := service.DeleteIssue(context.Background(), "issue-1", 0, "user-1"); err != ErrInvalid {
		t.Fatalf("invalid version error=%v", err)
	}
	if err := service.DeleteIssue(context.Background(), "issue-1", 2, "user-1"); err != nil {
		t.Fatal(err)
	}
	if repo.deletedID != "issue-1" || repo.expected != 2 || repo.actor != "user-1" || !repo.now.Equal(time.Unix(123, 0).UTC()) {
		t.Fatalf("delete forwarding=%#v", repo)
	}
}

func TestServiceUpdatesAndDeletesVersion(t *testing.T) {
	repo := &repositoryStub{}
	service := NewService(repo, func() time.Time { return time.Unix(123, 0) })
	if _, err := service.UpdateVersion(context.Background(), "issue-1", "fr", 2, UpdateVersionInput{Title: "Title"}, "user-1"); err != ErrInvalid {
		t.Fatalf("invalid update error=%v", err)
	}
	if _, err := service.UpdateVersion(context.Background(), "issue-1", "en", 2, UpdateVersionInput{Title: " Title "}, "user-1"); err != nil {
		t.Fatal(err)
	}
	if repo.locale != "en" || repo.title != "Title" {
		t.Fatalf("locale=%q title=%q", repo.locale, repo.title)
	}
	if _, err := service.DeleteVersion(context.Background(), "issue-1", "en", 2, "user-1"); err != nil {
		t.Fatal(err)
	}
	if repo.deletedLocale != "en" {
		t.Fatalf("deleted locale=%q", repo.deletedLocale)
	}
}

func TestServiceListsAndRestoresIssueRevision(t *testing.T) {
	repo := &repositoryStub{}
	service := NewService(repo, func() time.Time { return time.Unix(123, 0) })

	if _, err := service.IssueRevisions(context.Background(), ""); err != ErrInvalid {
		t.Fatalf("invalid list error=%v", err)
	}
	if _, err := service.RestoreIssueRevision(context.Background(), "issue-1", 0, 2, "user-1"); err != ErrInvalid {
		t.Fatalf("invalid revision error=%v", err)
	}
	if _, err := service.RestoreIssueRevision(context.Background(), "issue-1", 1, 0, "user-1"); err != ErrInvalid {
		t.Fatalf("invalid expected error=%v", err)
	}
	if _, err := service.RestoreIssueRevision(context.Background(), "issue-1", 1, 2, "user-1"); err != nil {
		t.Fatal(err)
	}
	if repo.revision != 1 || repo.expected != 2 || repo.actor != "user-1" || !repo.now.Equal(time.Unix(123, 0).UTC()) {
		t.Fatalf("restore forwarding=%#v", repo)
	}
}

type repositoryStub struct {
	page, pageSize    int
	expected          int64
	actor             string
	deletedID         string
	revision          int64
	locale            string
	title             string
	deletedLocale     string
	issueNumber       int
	issueDate         string
	notifySubscribers bool
	now               time.Time
}

func (r *repositoryStub) CreateIssue(context.Context, int, string, string, string, time.Time) (Issue, error) {
	return Issue{}, nil
}
func (r *repositoryStub) ListIssues(_ context.Context, p, s int, _, _ string) (Page, error) {
	r.page = p
	r.pageSize = s
	return Page{}, nil
}
func (r *repositoryStub) GetIssue(context.Context, string) (Issue, error) { return Issue{}, nil }
func (r *repositoryStub) UpdateIssue(_ context.Context, _ string, expected int64, input UpdateIssueInput, _ string, _ time.Time) (Issue, error) {
	r.expected, r.issueNumber, r.issueDate = expected, input.IssueNumber, input.IssueDate
	return Issue{}, nil
}
func (r *repositoryStub) PutVersion(context.Context, string, int64, PutVersionInput, string, time.Time) (Issue, error) {
	return Issue{}, nil
}
func (r *repositoryStub) UpdateVersion(_ context.Context, _ string, locale string, _ int64, title, _ string, _ string, _ time.Time) (Issue, error) {
	r.locale, r.title = locale, title
	return Issue{}, nil
}
func (r *repositoryStub) DeleteVersion(_ context.Context, _ string, locale string, _ int64, _ string, _ time.Time) (Issue, error) {
	r.deletedLocale = locale
	return Issue{}, nil
}
func (r *repositoryStub) StartPublish(_ context.Context, _ string, _ string, _ int64, notifySubscribers bool, _ string, _ time.Time) (Workflow, error) {
	r.notifySubscribers = notifySubscribers
	return Workflow{}, nil
}
func (r *repositoryStub) Unpublish(context.Context, string, string, int64, string, time.Time) (Issue, error) {
	return Issue{}, nil
}
func (r *repositoryStub) DeleteIssue(_ context.Context, id string, expected int64, actor string, now time.Time) error {
	r.deletedID, r.expected, r.actor, r.now = id, expected, actor, now
	return nil
}
func (*repositoryStub) IssueRevisions(context.Context, string) ([]Revision, error) {
	return []Revision{}, nil
}
func (r *repositoryStub) RestoreIssueRevision(_ context.Context, _ string, revision, expected int64, actor string, now time.Time) (Issue, error) {
	r.revision, r.expected, r.actor, r.now = revision, expected, actor, now
	return Issue{}, nil
}
func (r *repositoryStub) GetPublicLatest(context.Context, string) (PublicBulletin, error) {
	return PublicBulletin{}, nil
}
func (r *repositoryStub) GetPublicByDate(context.Context, string, string) (PublicBulletin, error) {
	return PublicBulletin{}, nil
}
func (r *repositoryStub) GetPublicByNumber(_ context.Context, issueNumber int, locale string) (PublicBulletin, error) {
	r.issueNumber, r.locale = issueNumber, locale
	return PublicBulletin{}, nil
}
func (r *repositoryStub) ListPublic(context.Context, int, int) (PublicPage, error) {
	return PublicPage{}, nil
}
