package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/accountclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

type commentResolverFunc func(context.Context, []string) ([]accountclient.Identity, error)

func (f commentResolverFunc) ResolveIdentities(ctx context.Context, ids []string) ([]accountclient.Identity, error) {
	return f(ctx, ids)
}

func TestCommentAuthorsBatchCacheAndPrivacy(t *testing.T) {
	now := time.Now()
	calls := 0
	h := &Handler{commentAuthors: commentAuthorCache{now: func() time.Time { return now }}}
	h.watermarkIdentities = commentResolverFunc(func(_ context.Context, ids []string) ([]accountclient.Identity, error) {
		calls++
		if len(ids) != 1 || ids[0] != "author" {
			t.Fatalf("not deduplicated: %v", ids)
		}
		return []accountclient.Identity{{ID: "author", DisplayName: "Member", AvatarURL: fmt.Sprintf("https://example.test/avatar-%d", calls), Email: "never-cache@example.test"}}, nil
	})
	page := func() []*recordings.Comment {
		return []*recordings.Comment{
			{AuthorID: "author"}, {AuthorID: "author", ReplyTo: &recordings.ReplyTarget{AuthorID: "author"}},
			{AuthorID: "deleted", Deleted: true, AvatarURL: "stale"}, {AuthorID: "hidden", Hidden: true, AvatarURL: "stale"},
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	first := page()
	h.commentNames(r, first)
	second := page()
	h.commentNames(r, second)
	if calls != 1 || second[0].AvatarURL != "https://example.test/avatar-1" || second[1].ReplyTo.DisplayName != "Member" {
		t.Fatalf("calls=%d page=%+v", calls, second)
	}
	if first[2].AvatarURL != "" || first[3].AvatarURL != "" {
		t.Fatal("deleted/hidden author leaked")
	}
	now = now.Add(commentAuthorTTL)
	third := page()
	h.commentNames(r, third)
	if calls != 2 || third[0].AvatarURL != "https://example.test/avatar-2" {
		t.Fatal("profile stayed stale")
	}
}

func TestCommentAuthorCacheCoalescesAndDoesNotCacheFailures(t *testing.T) {
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	resolver := commentResolverFunc(func(ctx context.Context, ids []string) ([]accountclient.Identity, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []accountclient.Identity{{ID: ids[0], DisplayName: "Member"}}, nil
	})
	cache := &commentAuthorCache{}
	done := make(chan error, 20)
	for range 20 {
		go func() { _, err := cache.resolve(context.Background(), resolver, []string{"one"}); done <- err }()
	}
	<-started
	close(release)
	for range 20 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	failing := commentResolverFunc(func(context.Context, []string) ([]accountclient.Identity, error) {
		return nil, errors.New("unavailable")
	})
	if _, err := cache.resolve(context.Background(), failing, []string{"two"}); err == nil {
		t.Fatal("expected lookup failure")
	}
	closeable := context.Background()
	if _, err := cache.resolve(closeable, resolver, []string{"two"}); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatal("failed lookup was cached")
	}
}

func TestCommentAuthorCacheBoundsMissingAndCancellation(t *testing.T) {
	calls := 0
	cache := &commentAuthorCache{}
	missing := commentResolverFunc(func(context.Context, []string) ([]accountclient.Identity, error) { calls++; return nil, nil })
	for range 2 {
		if _, err := cache.resolve(context.Background(), missing, []string{"gone"}); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatal("absent account was not negatively cached")
	}
	for i := 0; i < maxCommentAuthors+5; i++ {
		_, _ = cache.resolve(context.Background(), missing, []string{fmt.Sprint(i)})
	}
	if len(cache.entries) != maxCommentAuthors {
		t.Fatalf("unbounded entries: %d", len(cache.entries))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	blocked := commentResolverFunc(func(ctx context.Context, _ []string) ([]accountclient.Identity, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	if _, err := cache.resolve(ctx, blocked, []string{"cancelled"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error: %v", err)
	}
}

func TestCommentPreviewAuthorsUseBoundedBatchesAndRemovedNodesStayAnonymous(t *testing.T) {
	h := &Handler{}
	calls, resolved := 0, 0
	h.watermarkIdentities = commentResolverFunc(func(_ context.Context, ids []string) ([]accountclient.Identity, error) {
		calls++
		if len(ids) > 100 {
			t.Fatalf("Account batch limit exceeded: %d", len(ids))
		}
		found := make([]accountclient.Identity, len(ids))
		for i, id := range ids {
			found[i] = accountclient.Identity{ID: id, DisplayName: "Resolved " + id, AvatarURL: "https://example.test/" + id}
		}
		resolved += len(ids)
		return found, nil
	})
	items := make([]*recordings.Comment, 0, 51)
	for i := range 50 {
		items = append(items, &recordings.Comment{AuthorID: fmt.Sprintf("author-%d", i), ReplyTo: &recordings.ReplyTarget{AuthorID: fmt.Sprintf("target-%d", i)}, ReplyPreviewAuthors: []recordings.ReplyPreviewAuthor{{AuthorID: fmt.Sprintf("preview-%d", i)}}})
	}
	items = append(items, &recordings.Comment{Hidden: true, AuthorID: "removed", ReplyPreviewAuthors: []recordings.ReplyPreviewAuthor{{AuthorID: "removed-preview", AvatarURL: "https://example.test/stale"}}})
	h.commentNames(httptest.NewRequest("GET", "/", nil), items)
	if calls != 2 || resolved != 150 || items[0].ReplyPreviewAuthors[0].DisplayName != "Resolved preview-0" || items[0].ReplyPreviewAuthors[0].AvatarURL == "" || len(items[50].ReplyPreviewAuthors) != 0 {
		t.Fatalf("unbounded or anonymous preview leaked: calls=%d resolved=%d preview=%+v removed=%+v", calls, resolved, items[0].ReplyPreviewAuthors, items[50])
	}
}
