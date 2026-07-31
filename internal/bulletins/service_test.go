package bulletins

import (
	"context"
	"testing"
	"time"
)

func TestServiceRejectsInvalidIssueAndVersion(t *testing.T) {
	repo := &repositoryStub{}
	service := NewService(repo, time.Now)
	if _, err := service.CreateIssue(context.Background(), CreateIssueInput{IssueDate: "07/12/2026"}, "user", "key"); err != ErrInvalid {
		t.Fatalf("error=%v", err)
	}
	if _, err := service.PutVersion(context.Background(), "id", 1, PutVersionInput{Locale: "fr", Title: "x", PDFAssetID: "a", PDFFileName: "a.pdf"}, "user"); err != ErrInvalid {
		t.Fatalf("error=%v", err)
	}
}
func TestServiceNormalizesPagination(t *testing.T) {
	repo := &repositoryStub{}
	service := NewService(repo, time.Now)
	_, _ = service.ListIssues(context.Background(), 0, 1000, "")
	if repo.page != 1 || repo.pageSize != 100 {
		t.Fatalf("page=%d size=%d", repo.page, repo.pageSize)
	}
}

func TestServiceArchivesAndRestoresIssue(t *testing.T) {
	repo := &repositoryStub{}
	service := NewService(repo, func() time.Time { return time.Unix(123, 0) })

	if _, err := service.ArchiveIssue(context.Background(), "", 1, "user-1"); err != ErrInvalid {
		t.Fatalf("invalid archive error=%v", err)
	}
	if _, err := service.RestoreIssue(context.Background(), "issue-1", 0, "user-1"); err != ErrInvalid {
		t.Fatalf("invalid restore error=%v", err)
	}
	if _, err := service.ArchiveIssue(context.Background(), "issue-1", 2, "user-1"); err != nil {
		t.Fatal(err)
	}
	if repo.archiveID != "issue-1" || repo.expected != 2 || repo.actor != "user-1" {
		t.Fatalf("archive forwarding=%#v", repo)
	}
	if _, err := service.RestoreIssue(context.Background(), "issue-1", 3, "user-2"); err != nil {
		t.Fatal(err)
	}
	if repo.restoreID != "issue-1" || repo.expected != 3 || repo.actor != "user-2" {
		t.Fatalf("restore forwarding=%#v", repo)
	}
}

type repositoryStub struct {
	page, pageSize       int
	expected             int64
	actor                string
	archiveID, restoreID string
}

func (r *repositoryStub) CreateIssue(context.Context, string, string, string, time.Time) (Issue, error) {
	return Issue{}, nil
}
func (r *repositoryStub) ListIssues(_ context.Context, p, s int, _ string) (Page, error) {
	r.page = p
	r.pageSize = s
	return Page{}, nil
}
func (r *repositoryStub) GetIssue(context.Context, string) (Issue, error) { return Issue{}, nil }
func (r *repositoryStub) PutVersion(context.Context, string, int64, PutVersionInput, string, time.Time) (Issue, error) {
	return Issue{}, nil
}
func (r *repositoryStub) StartPublish(context.Context, string, string, int64, string, time.Time) (Workflow, error) {
	return Workflow{}, nil
}
func (r *repositoryStub) Unpublish(context.Context, string, string, int64, string, time.Time) (Issue, error) {
	return Issue{}, nil
}
func (r *repositoryStub) ArchiveIssue(_ context.Context, id string, expected int64, actor string, _ time.Time) (Issue, error) {
	r.archiveID, r.expected, r.actor = id, expected, actor
	return Issue{}, nil
}
func (r *repositoryStub) RestoreIssue(_ context.Context, id string, expected int64, actor string, _ time.Time) (Issue, error) {
	r.restoreID, r.expected, r.actor = id, expected, actor
	return Issue{}, nil
}
func (r *repositoryStub) GetPublicLatest(context.Context, string) (PublicBulletin, error) {
	return PublicBulletin{}, nil
}
func (r *repositoryStub) GetPublicByDate(context.Context, string, string) (PublicBulletin, error) {
	return PublicBulletin{}, nil
}
func (r *repositoryStub) ListPublic(context.Context, int, int) (PublicPage, error) {
	return PublicPage{}, nil
}
