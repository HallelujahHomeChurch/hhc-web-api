package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinreader"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/google/uuid"
)

func TestPrivateReaderAccountCleanupPreservesOtherSubject(t *testing.T) {
	for _, action := range []string{"erase", "restriction"} {
		t.Run(action, func(t *testing.T) {
			db := accessDB(t)
			ctx := context.Background()
			now := time.Now().UTC().Truncate(time.Microsecond)
			repo, source, revision := seedReaderPublication(t, db, now)
			owner, other := uuid.NewString(), uuid.NewString()
			t.Cleanup(func() {
				_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_reader_migration_state WHERE document_id=$1`, source.DocumentID)
			})
			for _, account := range []string{owner, other} {
				for _, query := range []string{
					`INSERT INTO hhc_web.bulletin_reader_migration_state(account_id,document_id,applied_revision,conflicts,highlight_history) VALUES($1,$2,$3,'[{"quote":"private conflict"}]','[{"id":"consumed"},{"id":"retained","highlight":{"quote":"private historical quote"}}]')`,
					`INSERT INTO hhc_web.bulletin_reader_highlight(account_id,document_id,sentence_id,color,quote,version,updated_at) VALUES($1,$2,'sentence','yellow','private quote',$3,now())`,
					`INSERT INTO hhc_web.bulletin_reader_note(account_id,document_id,id,body,sentence_ids,inactive_anchors,quote,version,created_at,updated_at) VALUES($1,$2,gen_random_uuid(),'private note','["sentence"]','["removed"]','private quote',$3,now(),now())`,
					`INSERT INTO hhc_web.bulletin_reader_bookmark(account_id,document_id,id,name,sentence_id,scalar_offset,page_id,document_revision,version,created_at,updated_at) VALUES($1,$2,gen_random_uuid(),'private bookmark','sentence',0,'page',$3,1,now(),now())`,
					`INSERT INTO hhc_web.bulletin_reader_progress(account_id,document_id,page_id,recorded_at,updated_at) VALUES($1,$2,$3::bigint::text,now(),now())`,
					`INSERT INTO hhc_web.bulletin_reader_processed_mutation(account_id,document_id,mutation_id,request_hash,result_json,created_at,expires_at) VALUES($1,$2,gen_random_uuid(),repeat('a',64),jsonb_build_object('revision',$3::bigint),now(),now()+interval '2184 hours')`,
				} {
					if _, err := db.Exec(query, account, source.DocumentID, revision); err != nil {
						t.Fatal(err)
					}
				}
			}
			expectedRows := int64(0)
			if action == "restriction" {
				if err := seedWatermarkRestriction(ctx, db, owner); err != nil {
					t.Fatal(err)
				}
				expectedRows = 6
			} else {
				key := uuid.NewString()
				count, remaining, err := repo.CleanupWatermarkAccount(ctx, owner, key)
				if err != nil || count != 6 || remaining != 0 {
					t.Fatalf("coverage count=%d remaining=%d error=%v", count, remaining, err)
				}
				if _, _, err = repo.CleanupWatermarkAccount(ctx, owner, key); err != nil {
					t.Fatal(err)
				}
			}
			mutation := bulletinreader.Mutation{MutationID: uuid.NewString(), CreatedAt: now, DocumentRevision: revision, Kind: "createBookmark", Payload: json.RawMessage(`{"bookmarkId":"49f10a14-688e-4726-b46f-51b68f950128","name":"late","sentenceId":"sentence","offset":0,"pageId":"page"}`)}
			if _, err := repo.MutateReader(ctx, source.Canonical.IssueID, "general", "zh-Hant", owner, []bulletinreader.Mutation{mutation}, now); !errors.Is(err, bulletins.ErrNotFound) {
				t.Fatal("account cleanup write fence bypassed", err)
			}
			assertPrivateReaderRows(t, db, owner, expectedRows)
			assertPrivateReaderRows(t, db, other, 6)
			if _, remaining, err := repo.CleanupWatermarkAccount(ctx, uuid.NewString(), uuid.NewString()); err != nil || remaining != 0 {
				t.Fatal("zero-row cleanup failed", err)
			}
		})
	}
}

func TestPrivateReaderMutationRetentionWithoutFurtherWrites(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC().Truncate(time.Microsecond)
	repo, source, revision := seedReaderPublication(t, db, now)
	account := uuid.NewString()
	if _, err := db.Exec(`INSERT INTO hhc_web.bulletin_reader_migration_state(account_id,document_id,applied_revision) VALUES($1,$2,$3)`, account, source.DocumentID, revision); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM hhc_web.bulletin_reader_migration_state WHERE account_id=$1`, account)
	})
	for _, expiry := range []time.Time{now, now.Add(time.Hour)} {
		if _, err := db.Exec(`INSERT INTO hhc_web.bulletin_reader_processed_mutation(account_id,document_id,mutation_id,request_hash,result_json,created_at,expires_at) VALUES($1,$2,gen_random_uuid(),repeat('a',64),'{}',$3::timestamptz-interval '2184 hours',$3)`, account, source.DocumentID, expiry); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.PurgeWatermarkRecords(context.Background(), now, 100); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM hhc_web.bulletin_reader_processed_mutation WHERE account_id=$1`, account).Scan(&count); err != nil || count != 1 {
		t.Fatalf("retained mutation count=%d error=%v", count, err)
	}
}
