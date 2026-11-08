package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestExtractionCompletionFencesFrozenTemplateSnapshot(t *testing.T) {
	db := accessDB(t)
	now := time.Now().UTC()
	id := seedOnlineExtraction(t, db, now)
	frozen := onlinebulletins.TemplateSettings{Version: 3, TemplateText: onlinebulletins.TemplateText{VisionMission: "遍地華人興起", VisionFellowship: "共同生活", VisionCommitment: "宣教主導"}}
	raw, _ := json.Marshal(frozen)
	if _, err := db.Exec(`UPDATE hhc_web.online_bulletin_extraction_job SET template_snapshot_json=$2 WHERE id=$1`, id, raw); err != nil {
		t.Fatal(err)
	}
	repo := New(db)
	job, err := repo.ClaimOnlineBulletinExtraction(context.Background(), now, time.Minute, 3)
	if err != nil || job == nil {
		t.Fatal("claim", err)
	}
	candidate := extractionCandidate(*job)
	candidate.Document.LayoutManifest.RendererVersion = "v9"
	candidate.Document.TemplateSnapshot = &frozen
	changed := frozen
	changed.VisionMission = "不能換成新設定"
	candidate.Document.TemplateSnapshot = &changed
	if err := repo.CompleteOnlineBulletinExtraction(context.Background(), *job, candidate, nil, now.Add(time.Second)); !errors.Is(err, onlinebulletins.ErrExtractionJob) {
		t.Fatalf("rewritten frozen input accepted: %v", err)
	}
	forged := *job
	forged.TemplateSnapshot = &changed
	if err := repo.CompleteOnlineBulletinExtraction(context.Background(), forged, candidate, nil, now.Add(time.Second)); !errors.Is(err, onlinebulletins.ErrExtractionJob) {
		t.Fatalf("job payload replaced database snapshot: %v", err)
	}
	candidate.Document.TemplateSnapshot = &frozen
	if err := repo.CompleteOnlineBulletinExtraction(context.Background(), *job, candidate, nil, now.Add(time.Second)); err != nil {
		t.Fatal("correct snapshot rejected", err)
	}
	state, err := repo.OnlineBulletinState(context.Background(), job.Canonical.IssueID, "general", "zh-Hant")
	if err != nil || state.Local == nil || state.Local.Document.TemplateSnapshot == nil || *state.Local.Document.TemplateSnapshot != frozen {
		t.Fatal("saved revision lost snapshot", err)
	}
}
