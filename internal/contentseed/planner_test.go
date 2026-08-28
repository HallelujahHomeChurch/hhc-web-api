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

func TestInventoryCountsDistinctPublishedNaturalKeys(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// DISTINCT collapses five locale projections; the join excludes draft-only aggregates.
	mock.ExpectQuery(`(?s)SELECT DISTINCT bulletin\.issue_number::text.*FROM hhc_web\.bulletin_issue bulletin.*JOIN hhc_web\.public_projection projection.*projection\.resource_type='bulletin_issue'.*projection\.resource_id=bulletin\.id.*bulletin\.issue_number IS NOT NULL`).WillReturnRows(sqlmock.NewRows([]string{"issue_number"}).AddRow("1732"))
	mock.ExpectQuery(`(?s)SELECT DISTINCT news\.slug.*FROM hhc_web\.news_item news.*JOIN hhc_web\.public_projection projection.*projection\.resource_type='news'.*projection\.resource_id=news\.entry_id`).WillReturnRows(sqlmock.NewRows([]string{"slug"}).AddRow("slug"))
	mock.ExpectQuery(`(?s)SELECT DISTINCT history\.sort_order::text.*FROM hhc_web\.history_event history.*JOIN hhc_web\.public_projection projection.*projection\.resource_type='history'.*projection\.resource_id=history\.entry_id`).WillReturnRows(sqlmock.NewRows([]string{"sort_order"}).AddRow("2"))
	mock.ExpectQuery(`(?s)SELECT DISTINCT video\.youtube_video_id.*FROM hhc_web\.video_item video.*JOIN hhc_web\.public_projection projection.*projection\.resource_type='videos'.*projection\.resource_id=video\.entry_id`).WillReturnRows(sqlmock.NewRows([]string{"youtube_video_id"}).AddRow("video-id"))

	report, err := Inventory(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	want := InventoryReport{
		Bulletins: ModuleInventory{Count: 1, KeySetSHA256: "c10502c4c464153d0d889fbbfc84a026db15107f99f029cb5724b8b550209c64"},
		News:      ModuleInventory{Count: 1, KeySetSHA256: "27018d99df96711e1da801450a5b1270d61f36e1c01f31a73ac687393e892e55"},
		History:   ModuleInventory{Count: 1, KeySetSHA256: "7c1ac2d094aaec5a8507a06b9878167f1d654a3423b0427015c4ac9f5fc44d1b"},
		Videos:    ModuleInventory{Count: 1, KeySetSHA256: "6cbbbe84546bc85f35edd258d8e888a36433e3c45874172d4fe56b4611468dbf"},
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

func TestInventoryModuleHashesModuleAndBytewiseSortedKeysWithNewlines(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	mock.ExpectQuery("SELECT natural_key").WillReturnRows(sqlmock.NewRows([]string{"key"}).AddRow("z").AddRow("a"))

	got, err := inventoryModule(context.Background(), db, "news", "SELECT natural_key")
	if err != nil {
		t.Fatal(err)
	}
	want := ModuleInventory{Count: 2, KeySetSHA256: "6e797633128ae4d28a36e783f981611a14cf25a1aa988147efa439741295cefe"}
	if got != want {
		t.Fatalf("inventory = %#v, want %#v", got, want)
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
			lookupTarget: func(ctx context.Context, db seedQuerier, sourceKey string) (string, bool, error) {
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
