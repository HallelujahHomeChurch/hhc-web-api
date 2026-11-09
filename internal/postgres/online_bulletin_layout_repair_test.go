package postgres

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

func TestLayoutRepairPersistsOnceAndIsFencedByCurrentAutomaticAuthority(t *testing.T) {
	for _, scenario := range []string{"resume", "cancelled", "lease"} {
		t.Run(scenario, func(t *testing.T) {
			db := accessDB(t)
			ctx, now := context.Background(), time.Now().UTC()
			repo, source := seedAutomaticUpdate(t, db, now)
			update, err := repo.ClaimOnlineBulletinUpdate(ctx, now, time.Minute, 3)
			if err != nil || update == nil {
				t.Fatal(err)
			}
			proof := verifiedUpdateForTest(t, update.Input)
			for _, a := range proof.Attempts {
				if err = repo.SaveOnlineBulletinUpdateAttempt(ctx, *update, a, now); err != nil {
					t.Fatal(err)
				}
			}
			if err = repo.CompleteOnlineBulletinUpdate(ctx, *update, proof, now); err != nil {
				t.Fatal(err)
			}
			layout, err := repo.ClaimOnlineBulletinLayout(ctx, now, time.Minute, 3)
			if err != nil || layout == nil || !layout.Automatic {
				t.Fatal("lost exact update authority", err)
			}
			issues := []onlinebulletins.ReviewIssue{{Code: "layout_validation_failed", ComponentID: "body", BlockID: "body-text", SourcePage: 2, Blocking: true}}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				plan := onlinebulletins.LayoutRepairPlan{Styles: []onlinebulletins.LayoutStyleDecision{{BlockID: "body-text", FontSize: 14, LineHeight: 20}}}
				json.NewEncoder(w).Encode(map[string]any{"status": "completed", "model": onlinebulletins.RecognitionModelName, "usage": map[string]any{"input_tokens": 100, "output_tokens": 100}, "output": []any{map[string]any{"type": "message", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": string(updateTestJSON(t, plan))}}}}})
			}))
			t.Cleanup(server.Close)
			client, err := onlinebulletins.NewAzureExtractor(server.URL, "test", "test", "", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			repair, err := client.PlanLayoutRepair(ctx, *layout, issues)
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "cancelled" {
				if _, err = db.Exec(`UPDATE hhc_web.bulletin_version SET auto_publish=false WHERE issue_id=$1`, source.Canonical.IssueID); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "lease" {
				now = now.Add(2 * time.Minute)
			}
			err = repo.SaveOnlineBulletinLayoutRepair(ctx, *layout, repair, now)
			if scenario != "resume" {
				if err == nil {
					t.Fatal("stale/cancelled repair persisted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = repo.SaveOnlineBulletinLayoutRepair(ctx, *layout, repair, now); err != nil {
				t.Fatal("non-idempotent repair checkpoint", err)
			}
			reclaimed, err := repo.ClaimOnlineBulletinLayout(ctx, now.Add(61*time.Second), time.Minute, 3)
			if err != nil || reclaimed == nil || reclaimed.Repair == nil {
				t.Fatal("repair would be paid twice", err)
			}
			if _, err = onlinebulletins.ApplyLayoutRepairEvidence(*reclaimed, issues, *reclaimed.Repair); err != nil {
				t.Fatal("repair checkpoint lost its binding", err)
			}
		})
	}
}
