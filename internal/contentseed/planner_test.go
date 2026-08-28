package contentseed

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

type plannerTestPayload struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

func TestPlanClassifiesInsertSkipConflictAndCounts(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const recordHash = "546f0aea68747d39bc62764aaac27b9bf0a182123f0454c0142e89a215c5aeae"
	const provenanceQuery = `(?s)SELECT EXISTS\(.*FROM hhc_web\.content_seed_source source.*JOIN hhc_web\.content_seed_run run.*WHERE run\.status='succeeded'.*source\.target_kind=\$1.*source\.source_key=\$2.*source\.record_sha256=\$3.*source\.target_id=\$4`
	mock.ExpectQuery("SELECT target_id FROM test_target").WithArgs("insert").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT target_id FROM test_target").WithArgs("skip").WillReturnRows(sqlmock.NewRows([]string{"target_id"}).AddRow("target-skip"))
	mock.ExpectQuery(provenanceQuery).WithArgs("location", "skip", recordHash, "target-skip").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectQuery("SELECT target_id FROM test_target").WithArgs("conflict").WillReturnRows(sqlmock.NewRows([]string{"target_id"}).AddRow("target-conflict"))
	mock.ExpectQuery(provenanceQuery).WithArgs("location", "conflict", recordHash, "target-conflict").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))

	records := make([]Record, 0, 3)
	for _, key := range []string{"insert", "skip", "conflict"} {
		records = append(records, Record{Kind: "location", SourceKey: key, Payload: json.RawMessage(`{"enabled":true,"name":"alpha"}`)})
	}
	report, err := plan(context.Background(), db, Manifest{Records: records}, plannerTestKinds())
	if err != nil {
		t.Fatal(err)
	}
	if report.InsertCount != 1 || report.SkipCount != 1 || report.ConflictCount != 1 {
		t.Fatalf("report counters = %#v", report)
	}
	wantActions := []Action{ActionInsert, ActionSkip, ActionConflict}
	for i, item := range report.Records {
		if item.Action != wantActions[i] || item.RecordSHA256 != recordHash {
			t.Fatalf("record %d = %#v", i, item)
		}
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPlanRecordHashIgnoresSourceHashAndJSONFormatting(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT target_id FROM test_target").WithArgs("same").WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery("SELECT target_id FROM test_target").WithArgs("same").WillReturnError(sql.ErrNoRows)
	first := Manifest{
		Sources: []Source{{Path: "source.json", SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
		Records: []Record{{Kind: "location", SourceKey: "same", Payload: json.RawMessage(`{"enabled":true,"name":"alpha"}`)}},
	}
	second := Manifest{
		Sources: []Source{{Path: "source.json", SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}},
		Records: []Record{{Kind: "location", SourceKey: "same", Payload: json.RawMessage("{\n  \"name\": \"alpha\", \"enabled\": true\n}")}},
	}
	firstReport, err := plan(context.Background(), db, first, plannerTestKinds())
	if err != nil {
		t.Fatal(err)
	}
	secondReport, err := plan(context.Background(), db, second, plannerTestKinds())
	if err != nil {
		t.Fatal(err)
	}
	if firstReport.Records[0].RecordSHA256 != secondReport.Records[0].RecordSHA256 {
		t.Fatalf("record hashes differ: %q != %q", firstReport.Records[0].RecordSHA256, secondReport.Records[0].RecordSHA256)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestPlanInvokesTypedPayloadValidation(t *testing.T) {
	kinds := plannerTestKinds()
	kind := kinds["location"]
	kind.decode = func(json.RawMessage) (any, error) { return nil, errors.New("invalid location payload") }
	kinds["location"] = kind
	_, err := plan(context.Background(), nil, Manifest{Records: []Record{{Kind: "location", SourceKey: "bad", Payload: json.RawMessage(`{}`)}}}, kinds)
	if err == nil {
		t.Fatal("expected payload validation error")
	}
}

func TestPlanFailsClosedUntilRecordKindIsReleased(t *testing.T) {
	if report, err := Plan(context.Background(), nil, Manifest{}); err != nil || len(report.Records) != 0 {
		t.Fatalf("empty plan = %#v, %v", report, err)
	}
	_, err := Plan(context.Background(), nil, Manifest{Records: []Record{{Kind: "location", SourceKey: "one", Payload: json.RawMessage(`{}`)}}})
	if err == nil {
		t.Fatal("expected unreleased record kind error")
	}
}

func TestInventoryReturnsCountsAndDeterministicKeyHashes(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT issue_number::text FROM hhc_web.bulletin_issue").WillReturnRows(sqlmock.NewRows([]string{"issue_number"}).AddRow("1732").AddRow("10"))
	mock.ExpectQuery("SELECT slug FROM hhc_web.news_item").WillReturnRows(sqlmock.NewRows([]string{"slug"}).AddRow("z").AddRow("a"))
	mock.ExpectQuery("SELECT sort_order::text FROM hhc_web.history_event").WillReturnRows(sqlmock.NewRows([]string{"sort_order"}).AddRow("2").AddRow("10"))
	mock.ExpectQuery("SELECT youtube_video_id FROM hhc_web.video_item").WillReturnRows(sqlmock.NewRows([]string{"youtube_video_id"}).AddRow("bbb").AddRow("aaa"))

	report, err := Inventory(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	want := InventoryReport{
		Bulletins: ModuleInventory{Count: 2, KeySetSHA256: "ca1adbb0cbc98df1af085ef2570b44cc344bef69c3f4d0e1cd9a49bf2d1da78d"},
		News:      ModuleInventory{Count: 2, KeySetSHA256: "6e797633128ae4d28a36e783f981611a14cf25a1aa988147efa439741295cefe"},
		History:   ModuleInventory{Count: 2, KeySetSHA256: "642b68fa2677cda2804e23d765700d175ee68268cf4620fd91712760d62d551f"},
		Videos:    ModuleInventory{Count: 2, KeySetSHA256: "56b45eead554196f6aaf8b7b8d7eb26b33836e8be3a74761f8dbe5615072ee4f"},
	}
	gotJSON, _ := json.Marshal(report)
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("inventory = %s, want %s", gotJSON, wantJSON)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func plannerTestKinds() map[string]plannerKind {
	return map[string]plannerKind{
		"location": {
			decode: func(payload json.RawMessage) (any, error) {
				var value plannerTestPayload
				decoder := json.NewDecoder(bytes.NewReader(payload))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&value); err != nil {
					return nil, err
				}
				return value, nil
			},
			lookupTarget: func(ctx context.Context, db *sql.DB, sourceKey string) (string, bool, error) {
				var targetID string
				if err := db.QueryRowContext(ctx, "SELECT target_id FROM test_target WHERE source_key=$1", sourceKey).Scan(&targetID); errors.Is(err, sql.ErrNoRows) {
					return "", false, nil
				} else if err != nil {
					return "", false, err
				}
				return targetID, true, nil
			},
		},
	}
}
