package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinreader"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/google/uuid"
)

func TestPrivateReaderMutationsDedupeAndCurrentAuthority(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	repo, source, revision := seedReaderPublication(t, db, now)
	owner := uuid.NewString()
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM hhc_web.bulletin_reader_migration_state WHERE document_id=$1`, source.DocumentID); err != nil {
			t.Error(err)
		}
	})
	state, err := repo.ReaderState(ctx, source.Canonical.IssueID, "general", "zh-Hant", owner, nil, now)
	if err != nil || state.State.AppliedRevision != revision {
		t.Fatal(err)
	}
	// The controlled publication fixture's welcome sentence is server-owned.
	var raw []byte
	if err = db.QueryRow(`SELECT document_json FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=$2`, source.DocumentID, revision).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components []struct {
			Cover *struct {
				Welcome []struct {
					Sentences []struct {
						ID string `json:"id"`
					} `json:"sentences"`
				} `json:"welcome"`
			} `json:"cover"`
		} `json:"components"`
	}
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	sentence := doc.Components[0].Cover.Welcome[0].Sentences[0].ID
	mutation := bulletinreader.Mutation{MutationID: uuid.NewString(), CreatedAt: now, DocumentRevision: revision, Kind: "setHighlight", Payload: json.RawMessage(`{"sentenceIds":["` + sentence + `"],"color":"yellow"}`)}
	write := func(actor string, ops []bulletinreader.Mutation, at time.Time) (bulletinreader.MutationResponse, error) {
		return repo.MutateReader(ctx, source.Canonical.IssueID, "general", "zh-Hant", actor, ops, at)
	}
	first, err := write(owner, []bulletinreader.Mutation{mutation, mutation}, now)
	if err != nil || len(first.Results) != 2 || len(first.State.Highlights) != 1 || first.State.Highlights[0].Version != 1 {
		t.Fatal("duplicate applied", err)
	}
	replay, err := write(owner, []bulletinreader.Mutation{mutation}, now.Add(90*24*time.Hour))
	if err != nil || replay.Results[0].Status != "applied" {
		t.Fatal("retained replay", err)
	}
	changed := mutation
	changed.Payload = json.RawMessage(`{"color":"blue","sentenceIds":["` + sentence + `"]}`)
	if _, err = write(owner, []bulletinreader.Mutation{changed}, now); !errors.Is(err, bulletinreader.ErrMutationReuse) {
		t.Fatal("different reuse", err)
	}
	mutation.Payload = json.RawMessage(`{"color":"yellow", "sentenceIds":["` + sentence + `"]}`)
	if _, err = write(owner, []bulletinreader.Mutation{mutation}, now); err != nil {
		t.Fatal("semantic fingerprint", err)
	}
	invalid := mutation
	invalid.MutationID = uuid.NewString()
	invalid.Payload = json.RawMessage(`{"sentenceIds":["foreign"],"color":"red"}`)
	changed.MutationID = uuid.NewString()
	if _, err = write(owner, []bulletinreader.Mutation{changed, invalid}, now); !errors.Is(err, bulletinreader.ErrInvalid) {
		t.Fatal("invalid group", err)
	}
	state, err = repo.ReaderState(ctx, source.Canonical.IssueID, "general", "zh-Hant", owner, nil, now)
	if err != nil || state.State.Highlights[0].Color != "yellow" {
		t.Fatal("partial batch persisted", err)
	}
	different, err := repo.ReaderState(ctx, source.Canonical.IssueID, "general", "zh-Hant", uuid.NewString(), nil, now)
	if err != nil || len(different.State.Highlights) != 0 {
		t.Fatal("cross-account state", err)
	}
	if _, err = repo.ReaderState(ctx, source.Canonical.IssueID, "children", "zh-Hant", owner, nil, now); !errors.Is(err, bulletins.ErrNotFound) {
		t.Fatal("cross-edition state", err)
	}
	stale := mutation
	stale.MutationID = uuid.NewString()
	stale.DocumentRevision = revision - 1
	result, err := write(owner, []bulletinreader.Mutation{stale}, now)
	if err != nil || result.Results[0].Status != "revision_changed" {
		t.Fatal(err)
	}
	stale.DocumentRevision = revision
	if result, err = write(owner, []bulletinreader.Mutation{stale}, now); err != nil || result.Results[0].Status != "applied" {
		t.Fatal("nonterminal consumed ID", err)
	}
	concurrent := mutation
	concurrent.MutationID = uuid.NewString()
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, failure := write(owner, []bulletinreader.Mutation{concurrent}, now)
			failures <- failure
		}()
	}
	wg.Wait()
	close(failures)
	for failure := range failures {
		if failure != nil {
			t.Fatal("concurrent replay", failure)
		}
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM hhc_web.bulletin_reader_processed_mutation WHERE account_id=$1 AND mutation_id=$2`, owner, concurrent.MutationID).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate receipts", count, err)
	}
	aged := mutation
	aged.MutationID = uuid.NewString()
	aged.CreatedAt = now.Add(-bulletinreader.ReplayWindow - time.Second)
	if result, err = write(owner, []bulletinreader.Mutation{aged}, now); err != nil || result.Results[0].Status != "recovery_required" {
		t.Fatal("aged unseen operation applied", err)
	}
	if result, err = write(owner, []bulletinreader.Mutation{mutation}, now.Add(bulletinreader.MutationRetention)); err != nil || result.Results[0].Status != "recovery_required" {
		t.Fatal("expired dedupe extended replay", err)
	}
	if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE id=$1`, source.DocumentID); err != nil {
		t.Fatal(err)
	}
	if _, err = write(owner, []bulletinreader.Mutation{mutation}, now); !errors.Is(err, bulletins.ErrNotFound) {
		t.Fatal("dedupe bypassed publication authority", err)
	}
}

func TestPrivateReaderFortyPublishedTransitionsAndMissingHistory(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	repo, source, first := seedReaderPublication(t, db, now)
	owner := uuid.NewString()
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM hhc_web.bulletin_reader_migration_state WHERE document_id=$1`, source.DocumentID); err != nil {
			t.Error(err)
		}
	})
	if _, err := repo.ReaderState(ctx, source.Canonical.IssueID, "general", "zh-Hant", owner, nil, now); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := db.QueryRow(`SELECT document_json FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=$2`, source.DocumentID, first).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var document onlinebulletins.Document
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := onlinebulletins.PublicationMappings(document, document, nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	for n := int64(1); n <= 40; n++ {
		// Leave revision-number gaps to model drafts. Only publications are visited.
		current := first + n*34
		previous := current - 34
		if _, err = db.Exec(`INSERT INTO hhc_web.online_bulletin_revision(document_id,issue_id,series,content_locale,revision,document_json,source_asset_checksum,schema_version,template_version,extractor_version,content_hash,renderer_version,renderer_artifact_sha256,layout_validation_hash,created_by)
 SELECT document_id,issue_id,series,content_locale,$3,document_json,source_asset_checksum,schema_version,template_version,extractor_version,content_hash,renderer_version,renderer_artifact_sha256,layout_validation_hash,'test' FROM hhc_web.online_bulletin_revision WHERE document_id=$1 AND revision=$2`, source.DocumentID, first, current); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`INSERT INTO hhc_web.online_bulletin_publication(document_id,revision,canonical_metadata,created_by) SELECT document_id,$3,canonical_metadata,'test' FROM hhc_web.online_bulletin_publication WHERE document_id=$1 AND revision=$2`, source.DocumentID, first, current); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(`INSERT INTO hhc_web.online_bulletin_mapping(document_id,from_revision,to_revision,from_sentence_id,status,to_sentence_ids) SELECT $1,$2,$3,"fromSentenceId",status,"toSentenceIds" FROM jsonb_to_recordset($4::jsonb) AS m("fromSentenceId" text,status text,"toSentenceIds" jsonb)`, source.DocumentID, previous, current, encoded); err != nil {
			t.Fatal(err)
		}
	}
	current := first + 40*34
	if _, err = db.Exec(`UPDATE hhc_web.online_bulletin_document SET published_revision=$2 WHERE id=$1`, source.DocumentID, current); err != nil {
		t.Fatal(err)
	}
	result, err := repo.ReaderState(ctx, source.Canonical.IssueID, "general", "zh-Hant", owner, &first, now)
	if err != nil || result.State.AppliedRevision != current || result.State.CurrentRevision != current || len(result.Mappings) != len(checkpoint) {
		t.Fatal("publication count cut off", err, result.State.AppliedRevision)
	}
	again, err := repo.ReaderState(ctx, source.Canonical.IssueID, "general", "zh-Hant", owner, &first, now)
	if err != nil || again.State.AppliedRevision != current {
		t.Fatal("repeat migration", err)
	}
	if _, err = db.Exec(`DELETE FROM hhc_web.online_bulletin_mapping WHERE document_id=$1 AND from_revision=$2 AND from_sentence_id=$3`, source.DocumentID, first, checkpoint[0].FromSentenceID); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.ReaderState(ctx, source.Canonical.IssueID, "general", "zh-Hant", owner, &first, now); !errors.Is(err, bulletinreader.ErrMapping) {
		t.Fatal("missing mapping guessed", err)
	}
}

func TestReaderFingerprintRejectsTrailingPayload(t *testing.T) {
	m := bulletinreader.Mutation{MutationID: uuid.NewString(), CreatedAt: time.Now(), DocumentRevision: 1, Kind: "clearHighlight", Payload: json.RawMessage(`{"sentenceIds":["a"]} {"accountId":"other"}`)}
	if _, err := readerMutationFingerprint("document", m); !errors.Is(err, bulletinreader.ErrInvalid) {
		t.Fatal("fingerprint ignored trailing input", err)
	}
}

func TestPrivateReaderPublishedSplitMergeRemovalSurvivesRepeatedReads(t *testing.T) {
	db := accessDB(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	repo, source, revision := seedReaderPublication(t, db, now)
	owner := uuid.NewString()
	t.Cleanup(func() {
		if _, err := db.Exec(`DELETE FROM hhc_web.bulletin_reader_migration_state WHERE document_id=$1`, source.DocumentID); err != nil {
			t.Error(err)
		}
	})
	draft, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
	if err != nil {
		t.Fatal(err)
	}
	original := draft.Local.Document.Components[1].BodySection.Blocks[0].Sentences[0].ID
	mutate := func(kind string, payload any) bulletinreader.MutationResponse {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		result, err := repo.MutateReader(ctx, source.Canonical.IssueID, "general", "zh-Hant", owner, []bulletinreader.Mutation{{MutationID: uuid.NewString(), CreatedAt: now, DocumentRevision: revision, Kind: kind, Payload: raw}}, now)
		if err != nil || result.Results[0].Status != "applied" {
			t.Fatalf("mutation %s: %v %#v", kind, err, result.Results)
		}
		return result
	}
	mutate("setHighlight", map[string]any{"sentenceIds": []string{original}, "color": "red"})
	note := mutate("createNote", map[string]any{"noteId": uuid.NewString(), "sentenceIds": []string{original}, "text": "保留原文"}).State.Notes[0]
	publish := func(ids []string, decisions []onlinebulletins.SentenceMapping) {
		t.Helper()
		draft, err := repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
		if err != nil {
			t.Fatal(err)
		}
		sentences := []onlinebulletins.Sentence{}
		for _, id := range ids {
			sentences = append(sentences, onlinebulletins.Sentence{ID: id, Spans: []onlinebulletins.Span{{Text: "正文。", FontRole: "body"}}})
		}
		draft.Local.Document.Components[1].BodySection.Blocks[0].Sentences = sentences
		if _, err = repo.SaveOnlineBulletinDraft(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.SaveDraftInput{CanonicalVersion: 1, OnlineVersion: draft.Version, Components: draft.Local.Document.Components}, "admin", now); err != nil {
			t.Fatal("save", err)
		}
		job, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
		if err != nil || job == nil {
			t.Fatal("claim", err)
		}
		if err = repo.CompleteOnlineBulletinLayout(ctx, *job, measuredLayout(t, *job), now); err != nil {
			t.Fatal("layout", err)
		}
		draft, err = repo.OnlineBulletinState(ctx, source.Canonical.IssueID, "general", "zh-Hant")
		if err != nil {
			t.Fatal(err)
		}
		confirmed, err := repo.ConfirmOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.ConfirmInput{CanonicalVersion: 1, OnlineVersion: draft.Version, Revision: draft.Local.Revision, PageCount: len(draft.Local.Document.Pages), LayoutValidationHash: draft.Local.Document.LayoutManifest.LayoutValidationHash, AcceptedIssues: draft.Local.ReviewIssues}, "admin", now)
		if err != nil {
			t.Fatal("confirm", err)
		}
		if _, err = repo.PublishOnlineBulletin(ctx, source.Canonical.IssueID, "general", "zh-Hant", onlinebulletins.PublishInput{CanonicalVersion: 1, OnlineVersion: confirmed.Version, Revision: confirmed.Revision, Mappings: decisions}, "admin", now); err != nil {
			t.Fatal("publish", err)
		}
		revision = confirmed.Revision
	}
	read := func() bulletinreader.State {
		t.Helper()
		value, err := repo.ReaderState(ctx, source.Canonical.IssueID, "general", "zh-Hant", owner, nil, now)
		if err != nil {
			t.Fatal("read", err)
		}
		again, err := repo.ReaderState(ctx, source.Canonical.IssueID, "general", "zh-Hant", owner, nil, now)
		if err != nil || !reflect.DeepEqual(value.State, again.State) {
			t.Fatal("migration repeated effects", err)
		}
		return value.State
	}
	publish([]string{"split-a", "split-b"}, []onlinebulletins.SentenceMapping{{FromSentenceID: original, Status: "split", ToSentenceIDs: []string{"split-a", "split-b"}}})
	state := read()
	if len(state.Highlights) != 2 || !reflect.DeepEqual(state.Notes[0].SentenceIDs, []string{"split-a", "split-b"}) {
		t.Fatal("split lost state")
	}
	mutate("setHighlight", map[string]any{"sentenceIds": []string{"split-b"}, "color": "blue"})
	publish([]string{"merged"}, []onlinebulletins.SentenceMapping{{FromSentenceID: "split-a", Status: "merged", ToSentenceIDs: []string{"merged"}}, {FromSentenceID: "split-b", Status: "merged", ToSentenceIDs: []string{"merged"}}})
	state = read()
	if len(state.Conflicts) != 1 {
		t.Fatal("differing colors overwritten")
	}
	for _, h := range state.Highlights {
		if h.Active {
			t.Fatal("conflict evidence remained active")
		}
	}
	mutate("resolveHighlightMigrationConflict", map[string]any{"conflictId": state.Conflicts[0].ID, "chosenColor": "yellow", "currentRevision": revision})
	publish([]string{"replacement"}, []onlinebulletins.SentenceMapping{{FromSentenceID: "merged", Status: "removed", ToSentenceIDs: []string{}}})
	state = read()
	if state.Notes[0].Text != note.Text || state.Notes[0].Quote != note.Quote || len(state.Notes[0].SentenceIDs) != 0 || len(state.Notes[0].InactiveAnchors) != 1 {
		t.Fatal("removed private quote lost")
	}
	version := state.Notes[0].Version
	payload, err := json.Marshal(map[string]any{"noteId": note.ID, "sentenceIds": []string{"replacement"}, "ranges": []map[string]any{{"sentenceId": "replacement", "start": 0, "end": 2}}})
	if err != nil {
		t.Fatal(err)
	}
	reanchor := bulletinreader.Mutation{MutationID: uuid.NewString(), CreatedAt: now, DocumentRevision: revision, Kind: "reanchorNote", BaseVersion: &version, Payload: payload}
	for i := 0; i < 2; i++ {
		result, err := repo.MutateReader(ctx, source.Canonical.IssueID, "general", "zh-Hant", owner, []bulletinreader.Mutation{reanchor}, now)
		if err != nil || len(result.Results) != 1 || result.Results[0].Status != "applied" {
			t.Fatalf("reanchor or replay failed: %v %#v", err, result.Results)
		}
	}
	state = read()
	relinked := state.Notes[0]
	if len(state.Notes) != 1 || relinked.ID != note.ID || relinked.Text != note.Text || relinked.Quote != note.Quote || relinked.Version != version+1 || relinked.ReanchorRequired || len(relinked.InactiveAnchors) != 0 || !reflect.DeepEqual(relinked.SentenceIDs, []string{"replacement"}) || len(relinked.Ranges) != 1 || relinked.Ranges[0].End != 2 {
		t.Fatalf("reanchor not durably preserved: %#v", relinked)
	}
	reanchor.MutationID = uuid.NewString()
	stale, err := repo.MutateReader(ctx, source.Canonical.IssueID, "general", "zh-Hant", owner, []bulletinreader.Mutation{reanchor}, now)
	if err != nil || stale.Results[0].Status != "note_conflict" {
		t.Fatal("stale reanchor bypassed CAS", err)
	}
	foreign, err := repo.MutateReader(ctx, source.Canonical.IssueID, "general", "zh-Hant", uuid.NewString(), []bulletinreader.Mutation{reanchor}, now)
	if err != nil || foreign.Results[0].Status != "note_conflict" || len(foreign.State.Notes) != 0 {
		t.Fatal("reanchor exposed another account's note", err)
	}
	state = read()
	if len(state.HighlightHistory) == 0 {
		t.Fatal("migration lost historical highlights")
	}
	for i, entry := range state.HighlightHistory {
		kind := "discardHighlightHistory"
		body := map[string]any{"historyId": entry.ID}
		if i == 0 {
			kind = "restoreHighlight"
			body["sentenceIds"] = []string{"replacement"}
			body["ranges"] = []map[string]any{{"sentenceId": "replacement", "start": 0, "end": 2}}
			body["color"] = "blue"
		}
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		action := bulletinreader.Mutation{MutationID: uuid.NewString(), CreatedAt: now, DocumentRevision: revision, Kind: kind, Payload: payload}
		for replay := 0; replay < 2; replay++ {
			result, err := repo.MutateReader(ctx, source.Canonical.IssueID, "general", "zh-Hant", owner, []bulletinreader.Mutation{action}, now)
			if err != nil || len(result.Results) != 1 || result.Results[0].Status != "applied" {
				t.Fatal("historical action or replay failed", kind, err, result.Results)
			}
		}
		action.MutationID = uuid.NewString()
		result, err := repo.MutateReader(ctx, source.Canonical.IssueID, "general", "zh-Hant", owner, []bulletinreader.Mutation{action}, now)
		if err != nil || result.Results[0].Status != "conflict_not_found" {
			t.Fatal("consumed history was reused", err)
		}
	}
	state = read()
	for _, entry := range state.HighlightHistory {
		if entry.Highlight != nil {
			t.Fatal("consumed history retained original quote")
		}
	}
	if len(state.Highlights) != 1 || !state.Highlights[0].Active || state.Highlights[0].Color != "blue" || len(state.Highlights[0].Segments) != 1 || state.Highlights[0].Segments[0].End != 2 {
		t.Fatal("restored highlight was not durable", state.Highlights)
	}
}
