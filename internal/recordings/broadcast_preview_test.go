package recordings

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

func previewFixture(t *testing.T) (*Service, *broadcastReconcileRepository, context.Context, string, *time.Time) {
	t.Helper()
	now := time.Now().UTC()
	actor := platform.NewID()
	b := boundBroadcast()
	b.View.RecordingID, b.CaptureIntentID, b.Epoch = "recording", "intent", 1
	repo := &broadcastReconcileRepository{captureTestRepository: &captureTestRepository{capture: Capture{IntentID: "intent", RecordingID: "recording", BroadcastEpoch: 1, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: b.View.Binding.CaptureID, State: "uploading", ExpiresAt: now.Add(time.Hour)}}}}, broadcast: b}
	producer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in assetclient.BroadcastPreviewGrantInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
			return
		}
		json.NewEncoder(w).Encode(assetclient.BroadcastPreviewGrant{RecordingID: in.RecordingID, CaptureID: b.View.Binding.CaptureID, Epoch: in.Epoch, Purpose: "staff-preview", Scope: in.PlaybackScopeID, MediaPath: "/videos/recording/captures/" + b.View.Binding.CaptureID + "/previews/" + in.PlaybackScopeID + "/master.m3u8", ExchangeCredential: "synthetic", IssuedAt: now, ExpiresAt: in.ExpiresAt})
	}))
	t.Cleanup(producer.Close)
	svc := NewService(repo, "https://media.invalid", nil, func() time.Time { return now }).WithPackages(assetclient.New(producer.URL, "hhc-web-api", ""))
	return svc, repo, auditclient.WithProvenance(context.Background(), actor, "preview-test"), actor, &now
}

func TestPreviewRejectsStoppedOrRevokedAuthority(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		t.Run(map[bool]string{false: "revoked", true: "stopped"}[stopped], func(t *testing.T) {
			svc, repo, ctx, actor, now := previewFixture(t)
			if stopped {
				repo.capture.Status.StopAcceptedAt = now
			} else {
				repo.broadcast.Revoked = true
			}
			_, err := svc.BroadcastPreview(ctx, "recording", actor, repo.broadcast.View.Binding.CaptureID, 1, "")
			if !errors.Is(err, ErrNotFound) {
				t.Fatalf("invalid preview authority accepted: %v", err)
			}
		})
	}
}

func TestPreviewRenewsOnlyMatchingUnexpiredScope(t *testing.T) {
	for _, mode := range []string{"same", "unknown", "expired", "actor", "capture", "epoch", "purpose", "scope", "stopped", "revoked", "terminal", "rebound", "newer-grant"} {
		t.Run(mode, func(t *testing.T) {
			svc, repo, ctx, actor, now := previewFixture(t)
			capture := repo.broadcast.View.Binding.CaptureID
			first, err := svc.BroadcastPreview(ctx, "recording", actor, capture, 1, "")
			if err != nil {
				t.Fatal(err)
			}
			*now = now.Add(4 * time.Minute)
			scope := first.PlaybackScopeID
			prior := repo.broadcast.Previews[scope]
			switch mode {
			case "unknown":
				scope = platform.NewID()
			case "expired":
				*now = first.ExpiresAt
			case "actor":
				prior.ActorID = platform.NewID()
			case "capture":
				prior.CaptureID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
			case "epoch":
				prior.Epoch++
			case "purpose":
				prior.Purpose = "member"
			case "scope":
				prior.Scope = platform.NewID()
			case "stopped":
				repo.capture.Status.StopAcceptedAt = now
			case "revoked":
				repo.broadcast.Revoked = true
			case "terminal":
				repo.capture.Status.State = "failed"
			case "rebound":
				repo.capture.BroadcastEpoch++
			case "newer-grant":
				prior.ExpiresAt = now.Add(5*time.Minute + time.Second)
			}
			repo.broadcast.Previews[first.PlaybackScopeID] = prior
			renewed, err := svc.BroadcastPreview(ctx, "recording", actor, capture, 1, scope)
			if mode == "newer-grant" {
				if !errors.Is(err, ErrConflict) || !repo.broadcast.Previews[scope].ExpiresAt.Equal(prior.ExpiresAt) || renewed.ExchangeCredential != "" {
					t.Fatalf("older grant replaced newer renewal: %v", err)
				}
				return
			}
			if mode != "same" {
				if !errors.Is(err, ErrNotFound) {
					t.Fatalf("invalid reuse accepted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if renewed.PlaybackScopeID != first.PlaybackScopeID || renewed.MediaURL != first.MediaURL || !renewed.ExpiresAt.After(first.ExpiresAt) || renewed.ExpiresAt.Sub(*now) > 5*time.Minute || len(repo.broadcast.Previews) != 1 {
				t.Fatalf("scope changed or expiry invalid: first=%+v renewed=%+v", first, renewed)
			}
		})
	}
}
