package postgres

import (
	"context"
	"testing"
)

func TestSimplifiedLegacySourceFromSavedProof(t *testing.T) {
	for _, fault := range []string{"none", "event metadata", "conversion link", "layout link", "assessment", "source hash", "layout metadata", "renderer", "recorded mismatch", "withdrawn"} {
		t.Run(fault, func(t *testing.T) {
			db, repo, input, _ := readyHansRecovery(t)
			var issue string
			if err := db.QueryRow(`SELECT issue_id::text FROM hhc_web.online_bulletin_document WHERE id=$1`, input.TargetDocumentID).Scan(&issue); err != nil {
				t.Fatal(err)
			}
			// Recreate an existing terminal event predating the tracking migration.
			if _, err := db.Exec(`UPDATE hhc_web.outbox_event SET status='failed',last_error='source_publication_changed',claimed_until=NULL WHERE id=$1;`, input.PublicationOperationID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`DELETE FROM hhc_web.online_bulletin_simplified_operation WHERE operation_id=$1`, input.PublicationOperationID); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "event metadata":
				_, err := db.Exec(`UPDATE hhc_web.outbox_event SET payload_json=jsonb_set(payload_json,'{sourceMetadata,title}','"different"') WHERE id=$1`, input.PublicationOperationID)
				if err != nil {
					t.Fatal(err)
				}
			case "conversion link":
				_, err := db.Exec(`UPDATE hhc_web.online_bulletin_conversion_job SET publication_event_id=NULL WHERE publication_event_id=$1`, input.PublicationOperationID)
				if err != nil {
					t.Fatal(err)
				}
			case "layout link":
				_, err := db.Exec(`UPDATE hhc_web.online_bulletin_document SET local_revision=(SELECT result_revision FROM hhc_web.online_bulletin_conversion_job WHERE publication_event_id=$1) WHERE id=$2`, input.PublicationOperationID, input.TargetDocumentID)
				if err != nil {
					t.Fatal(err)
				}
			case "assessment":
				_, err := db.Exec(`UPDATE hhc_web.online_bulletin_conversion_job SET assessment_json='[]' WHERE publication_event_id=$1`, input.PublicationOperationID)
				if err != nil {
					t.Fatal(err)
				}
			case "source hash":
				_, err := db.Exec(`UPDATE hhc_web.online_bulletin_conversion_job SET source_content_hash=repeat('0',64) WHERE publication_event_id=$1`, input.PublicationOperationID)
				if err != nil {
					t.Fatal(err)
				}
			case "layout metadata":
				_, err := db.Exec(`INSERT INTO hhc_web.online_bulletin_layout_job SELECT (jsonb_populate_record(NULL::hhc_web.online_bulletin_layout_job,to_jsonb(j)||jsonb_build_object('id',gen_random_uuid(),'created_at',j.created_at+interval '1 second','canonical_metadata',jsonb_set(j.canonical_metadata,'{title}','"different"')))).* FROM hhc_web.online_bulletin_layout_job j WHERE document_id=$1 AND result_revision=$2`, input.TargetDocumentID, input.Revision)
				if err != nil {
					t.Fatal(err)
				}
			case "renderer":
				_, err := db.Exec(`INSERT INTO hhc_web.online_bulletin_layout_job SELECT (jsonb_populate_record(NULL::hhc_web.online_bulletin_layout_job,to_jsonb(j)||jsonb_build_object('id',gen_random_uuid(),'created_at',j.created_at+interval '1 second','renderer_artifact_sha256',repeat('0',64)))).* FROM hhc_web.online_bulletin_layout_job j WHERE document_id=$1 AND result_revision=$2`, input.TargetDocumentID, input.Revision)
				if err != nil {
					t.Fatal(err)
				}
			case "recorded mismatch":
				_, err := db.Exec(`INSERT INTO hhc_web.online_bulletin_simplified_operation(operation_id,source_document_id,source_revision,source_content_hash,coverage) VALUES($1,$2,$3,repeat('0',64),'partial')`, input.PublicationOperationID, input.Source.DocumentID, input.Source.Revision)
				if err != nil {
					t.Fatal(err)
				}
			case "withdrawn":
				_, err := db.Exec(`UPDATE hhc_web.online_bulletin_document SET published_revision=NULL WHERE id=$1`, input.Source.DocumentID)
				if err != nil {
					t.Fatal(err)
				}
			}
			before := journeyEffects(t, db, issue)
			notifications := journeyNotificationSnapshot(t, db, issue)
			state, err := repo.OnlineBulletinState(context.Background(), issue, "general", "zh-Hans")
			if err != nil {
				t.Fatal(err)
			}
			op := state.DerivedPublication
			if op == nil {
				t.Fatal("lost historical operation")
			}
			if fault == "none" {
				if state.Layout == nil || state.Layout.ResultRevision == nil || *state.Layout.ResultRevision != input.Revision {
					t.Fatal("missing final layout revision binding")
				}
				if op.SourceCurrent == nil || !*op.SourceCurrent || op.Source.ContentHash == nil || *op.Source.ContentHash != input.Source.ContentHash || op.Source.MetadataHash == nil {
					t.Fatalf("lost qualified legacy source: %#v", op)
				}
				if op.HistoryCoverage != "unknown" || op.Attempts != nil || op.FailureCount != nil || len(op.History) != 0 {
					t.Fatalf("invented legacy history: %#v", op)
				}
			} else if op.SourceCurrent != nil && *op.SourceCurrent {
				t.Fatalf("unproven source accepted (%s): %#v", fault, op)
			}
			if op.Status != "superseded" || op.ErrorCode != "source_publication_changed" {
				t.Fatalf("rewrote historical reason: %#v", op)
			}
			if journeyEffects(t, db, issue) != before || journeyNotificationSnapshot(t, db, issue) != notifications {
				t.Fatal("state read mutated effects or notifications")
			}
		})
	}
}
