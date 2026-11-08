package publication

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"
)

func TestCleanupProvenanceWarningRequiresGrantRevocation(t *testing.T) {
	for _, test := range []struct {
		name, eventType, payload string
		warn                     bool
	}{
		{"owner_only", "asset.owner.delete", `{"assetId":"private-asset"}`, false},
		{"owner_with_grant", "asset.owner.delete", `{"assetId":"private-asset","grantId":"grant-1"}`, true},
		{"grant_revoke", "asset.grant.revoke", `{"assetId":"private-asset","grantId":"grant-1"}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var logs bytes.Buffer
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			repo := &workerRepository{event: Event{ID: "cleanup-event", EventType: test.eventType, Payload: []byte(test.payload), Attempts: 1}}
			assets := &workerAssets{}
			ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}}))
			if trace.SpanContextFromContext(ctx).IsSampled() {
				t.Fatal("test context must be unsampled")
			}
			if _, err := NewWorker(repo, assets, 5).processNext(ctx); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(logs.String(), "publication cleanup provenance unavailable") != test.warn {
				t.Fatal("wrong audit-specific warning for cleanup operation")
			}
			if test.warn && !strings.Contains(logs.String(), `"request_id":"cleanup-event"`) {
				t.Fatal("unsampled background warning lost technical work correlation")
			}
			if repo.completeCount != 1 {
				t.Fatal("cleanup completion behavior changed")
			}
			if test.eventType == "asset.owner.delete" && assets.deletedAsset != "private-asset" {
				t.Fatal("owner deletion behavior changed")
			}
			if strings.Contains(logs.String(), "private-asset") {
				t.Fatal("asset reference logged")
			}
		})
	}
}

func TestCleanupBranchesPreserveOriginalActor(t *testing.T) {
	for _, kind := range []string{"bulletin.unpublish.revoke_asset", "bulletin.asset.retire", "asset.owner.delete", "asset.grant.revoke"} {
		t.Run(kind, func(t *testing.T) {
			event := Event{ID: "cleanup-event", EventType: kind, ActorID: "018f0000-0000-7000-8000-000000000009", Payload: []byte(`{"issueId":"issue-1","locale":"zh-Hant","assetId":"asset-1","grantId":"grant-1","aggregateVersion":4}`), Attempts: 1}
			repo := &workerRepository{event: event}
			assets := &workerAssets{}
			worker := NewWorker(repo, assets, 5)
			if _, err := worker.processNext(context.Background()); err != nil {
				t.Fatal(err)
			}
			if assets.revokeActorID != event.ActorID || assets.revokeRequestID != event.ID {
				t.Fatalf("source lost: actor=%q request=%q", assets.revokeActorID, assets.revokeRequestID)
			}
		})
	}
}
