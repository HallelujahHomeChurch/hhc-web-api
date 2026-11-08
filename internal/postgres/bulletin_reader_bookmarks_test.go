package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinreader"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/google/uuid"
)

func TestReaderBookmarksPersistDedupeAndConcurrentLimit(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	repo, source, revision := seedReaderPublication(t, db, now)
	owner := uuid.NewString()
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_reader_migration_state WHERE document_id=$1`, source.DocumentID)
	})
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, snapshot, err := readerPublication(ctx, tx, source.DocumentID, revision)
	_ = tx.Rollback()
	if err != nil {
		t.Fatal(err)
	}
	var sentence, page string
	var offset int
	for id, fragments := range snapshot.SentenceFragments {
		if len(fragments) > 0 && fragments[0].End > fragments[0].Start {
			sentence, page, offset = id, fragments[0].PageID, fragments[0].Start
			break
		}
	}
	if sentence == "" {
		t.Fatal("missing canonical source")
	}
	makeMutation := func(kind string, base int64, payload any) bulletinreader.Mutation {
		raw, _ := json.Marshal(payload)
		m := bulletinreader.Mutation{MutationID: uuid.NewString(), CreatedAt: now, DocumentRevision: revision, Kind: kind, Payload: raw}
		if base > 0 {
			m.BaseVersion = &base
		}
		return m
	}
	create := func(id string) bulletinreader.Mutation {
		return makeMutation("createBookmark", 0, map[string]any{"bookmarkId": id, "name": "my bookmark", "sentenceId": sentence, "offset": offset, "pageId": page})
	}
	write := func(ops ...bulletinreader.Mutation) (bulletinreader.MutationResponse, error) {
		return repo.MutateReader(ctx, source.Canonical.IssueID, "general", "zh-Hant", owner, ops, now)
	}
	id := uuid.NewString()
	first := create(strings.ToUpper(id))
	result, err := write(first, first)
	if err != nil || len(result.State.Bookmarks) != 1 || result.State.Bookmarks[0].Version != 1 {
		t.Fatal(result, err)
	}
	rename := makeMutation("renameBookmark", 1, map[string]any{"bookmarkId": strings.ReplaceAll(id, "-", ""), "name": "renamed"})
	result, err = write(rename)
	if err != nil || result.Results[0].Bookmark.Version != 2 {
		t.Fatal(result, err)
	}
	result, err = write(first)
	if err != nil || result.Results[0].Bookmark.Version != 1 || result.State.Bookmarks[0].Version != 2 || result.State.Bookmarks[0].Name != "renamed" {
		t.Fatal("old receipt overwrote latest state", result, err)
	}
	changed := first
	changed.Payload, _ = json.Marshal(map[string]any{"bookmarkId": id, "name": "changed", "sentenceId": sentence, "offset": offset, "pageId": page})
	if _, err = write(changed); !errors.Is(err, bulletinreader.ErrMutationReuse) {
		t.Fatal("changed fingerprint accepted", err)
	}
	rename.MutationID = uuid.NewString()
	result, err = write(rename)
	if err != nil || result.Results[0].Status != "bookmark_conflict" || result.Results[0].Bookmark.Version != 2 {
		t.Fatal(result, err)
	}
	isolated, err := repo.ReaderState(ctx, source.Canonical.IssueID, "general", "zh-Hant", uuid.NewString(), nil, now)
	if err != nil || len(isolated.State.Bookmarks) != 0 || isolated.State.Bookmarks == nil {
		t.Fatal("account leak or capability missing", err)
	}
	if _, err = repo.ReaderState(ctx, source.Canonical.IssueID, "children", "zh-Hant", owner, nil, now); !errors.Is(err, bulletins.ErrNotFound) {
		t.Fatal("cross edition", err)
	}
	batch := make([]bulletinreader.Mutation, 98)
	for i := range batch {
		batch[i] = create(uuid.NewString())
	}
	if _, err = write(batch...); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	statuses := make(chan string, 2)
	failures := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			r, e := write(create(uuid.NewString()))
			if e != nil {
				failures <- e
				return
			}
			statuses <- r.Results[0].Status
		})
	}
	wg.Wait()
	close(statuses)
	close(failures)
	for e := range failures {
		t.Fatal(e)
	}
	counts := map[string]int{}
	for status := range statuses {
		counts[status]++
	}
	if counts["applied"] != 1 || counts["bookmark_limit"] != 1 {
		t.Fatal("limit raced", counts)
	}
	removed := makeMutation("deleteBookmark", 2, map[string]any{"bookmarkId": strings.ToUpper(id)})
	result, err = write(removed)
	if err != nil || !result.Results[0].Bookmark.Deleted {
		t.Fatal(result, err)
	}
	alias := create("{" + id + "}")
	if r, e := write(alias); e != nil || r.Results[0].Status != "bookmark_conflict" || r.Results[0].Bookmark == nil || !r.Results[0].Bookmark.Deleted {
		t.Fatal("alias revived tombstone", r, e)
	}
	removed.MutationID = uuid.NewString()
	result, err = write(removed)
	if err != nil || result.Results[0].Status != "bookmark_conflict" || !result.Results[0].Bookmark.Deleted {
		t.Fatal("delete revived", result, err)
	}
	if _, err = write(create(uuid.NewString())); err != nil {
		t.Fatal(err)
	}
}
