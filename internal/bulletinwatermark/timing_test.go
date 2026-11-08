package bulletinwatermark

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPhaseTimerRecordsOrderedClampedPhases(t *testing.T) {
	base := time.Unix(1000, 0)
	now := base
	timer := newPhaseTimer(base, func() time.Time { return now })
	now = base.Add(150 * time.Millisecond)
	timer.Mark("dequeue")
	timer.MarkAt("skewed_past", base)                  // clamps to the previous mark
	timer.MarkAt("skewed_future", base.Add(time.Hour)) // clamps to now
	now = base.Add(400 * time.Millisecond)
	timer.Mark("claim")
	timer.Set("outcome", "ready")
	timer.Set("kind", "download")
	got := timer.Attrs()
	want := []any{"kind", "download", "outcome", "ready", "ms_dequeue", int64(150), "ms_skewed_past", int64(0), "ms_skewed_future", int64(0), "ms_claim", int64(250), "ms_total", int64(400)}
	if len(got) != len(want) {
		t.Fatalf("attrs = %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("attrs[%d] = %#v, want %#v (all %#v)", i, got[i], want[i], got)
		}
	}
}

func TestPhaseTimerIsOptional(t *testing.T) {
	markPhase(context.Background(), "claim")
	setPhaseAttr(context.Background(), "outcome", "ready")
	markHelperPhases(context.Background(), t.TempDir())
	var timer *PhaseTimer
	timer.Log(context.Background())
}

func TestMarkHelperPhasesIgnoresInvalidMarkers(t *testing.T) {
	for name, content := range map[string]string{"missing": "", "invalid": "{", "oversized": `{"ready":` + strings.Repeat(" ", 2048) + `1}`} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			if content != "" {
				if err := os.WriteFile(filepath.Join(dir, "phases"), []byte(content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			timer := NewPhaseTimer(time.Now())
			markHelperPhases(WithPhaseTimer(context.Background(), timer), dir)
			if attrs := timer.Attrs(); len(attrs) != 4 || attrs[0] != "ms_helper_exit" {
				t.Fatalf("attrs = %#v", attrs)
			}
		})
	}
}

func TestMarkHelperPhasesUsesHelperWallClock(t *testing.T) {
	start := time.Now().Add(-4 * time.Second)
	dir := t.TempDir()
	seconds := func(d time.Duration) float64 { return float64(start.Add(d).UnixNano()) / float64(time.Second) }
	marks, _ := json.Marshal(map[string]float64{"ready": seconds(time.Second), "model": seconds(2 * time.Second), "encoded": seconds(3 * time.Second), "verified": seconds(3500 * time.Millisecond)})
	if err := os.WriteFile(filepath.Join(dir, "phases"), marks, 0600); err != nil {
		t.Fatal(err)
	}
	timer := NewPhaseTimer(start)
	markHelperPhases(WithPhaseTimer(context.Background(), timer), dir)
	attrs := timer.Attrs()
	names := []string{}
	for i := 0; i < len(attrs); i += 2 {
		names = append(names, attrs[i].(string))
	}
	if strings.Join(names, ",") != "ms_helper_boot,ms_helper_model,ms_helper_encode,ms_helper_verify,ms_helper_exit,ms_total" {
		t.Fatalf("names = %v", names)
	}
	for i, want := range []int64{1000, 1000, 1000, 500} {
		if got := attrs[2*i+1].(int64); got < want-5 || got > want+5 {
			t.Fatalf("%s = %d, want about %d", names[i], got, want)
		}
	}
}

func TestPhaseLogContainsNoIdentifiers(t *testing.T) {
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buffer, nil)))
	defer slog.SetDefault(previous)
	queue := &workQueueStub{message: LeasedMessage{WorkMessage: WorkMessage{Kind: "download", ID: "11111111-1111-4111-8111-111111111111"}, MessageID: "message-secret", PopReceipt: "pop-secret", DequeueCount: 4}}
	repository := &downloadWorkerRepo{}
	ctx := WithPhaseTimer(context.Background(), NewPhaseTimer(time.Now()))
	if processed, err := ProcessOne(ctx, queue, NewDownloadWorker(repository, nil, nil, "privacy")); !processed || err != nil {
		t.Fatalf("processed=%v err=%v", processed, err)
	}
	line := buffer.String()
	for _, secret := range []string{"11111111-1111-4111-8111-111111111111", "message-secret", "pop-secret"} {
		if strings.Contains(line, secret) {
			t.Fatalf("log line leaks %q: %s", secret, line)
		}
	}
	var record map[string]any
	if err := json.Unmarshal(buffer.Bytes(), &record); err != nil {
		t.Fatal(err)
	}
	if record["kind"] != "download" || record["outcome"] != "exhausted" || record["ms_total"] == nil {
		t.Fatalf("record = %v", record)
	}
}
