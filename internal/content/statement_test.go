package content

import (
	"encoding/json"
	"testing"
	"time"
)

func TestStatementWindowBoundaries(t *testing.T) {
	start := time.Date(2026, 9, 7, 10, 1, 16, 0, time.UTC)
	end := start.Add(14 * 24 * time.Hour)
	for _, tc := range []struct {
		now  time.Time
		want bool
	}{{start.Add(-time.Second), false}, {start, true}, {end.Add(-time.Second), true}, {end, false}} {
		if StatementWindowActive(&start, &end, tc.now) != tc.want {
			t.Fatalf("boundary %s", tc.now)
		}
	}
	if StatementWindowActive(nil, nil, start) {
		t.Fatal("unset window")
	}
}
func TestStatementWriteOmissionAndClear(t *testing.T) {
	for _, tc := range []struct {
		body string
		set  bool
	}{{`{}`, false}, {`{"popupStartsAt":null,"popupEndsAt":null}`, true}} {
		var in WriteInput
		if err := json.Unmarshal([]byte(tc.body), &in); err != nil {
			t.Fatal(err)
		}
		if in.PopupWindowSet != tc.set {
			t.Fatal("window presence lost")
		}
	}
}
func TestStatementManualSummaryAndValidation(t *testing.T) {
	in := WriteInput{Kind: "statement", Slug: "statement-1", DisplayDate: "2026-09-07", Translations: []Translation{{Locale: "zh-Hant", Title: "聲明", Body: "原文", Summary: "人工摘要"}}}
	in = normalizeNews(in, "")
	if in.Translations[0].Summary != "人工摘要" {
		t.Fatal("manual summary removed")
	}
	if !valid(ModuleNews, in) {
		t.Fatal("valid statement rejected")
	}
	start := time.Now()
	in.PopupStartsAt = &start
	if valid(ModuleNews, in) {
		t.Fatal("half window accepted")
	}
	in.PopupStartsAt = nil
	in.Kind = "unknown"
	if valid(ModuleNews, in) {
		t.Fatal("unknown kind accepted")
	}
}
