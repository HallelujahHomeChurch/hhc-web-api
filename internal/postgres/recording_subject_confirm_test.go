package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSubjectReadsDoNotStarveConcurrentConfirm(t *testing.T) {
	repo, b, ctx, now := subjectLiveFixture(t)
	repo.db.SetMaxOpenConns(10)
	repo.db.SetMaxIdleConns(10)
	actor, _ := auditclient.ProvenanceFromContext(ctx)
	capture := b.View.Binding.CaptureID
	c, e := repo.GetCapture(ctx, capture, b.View.RecordingID)
	if e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in assetclient.RecordingCaptureConfirmInput
		if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
			t.Error(e)
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(assetclient.RecordingCaptureResult{Capture: c.Status.RecordingCaptureStatus, Receipt: assetclient.RecordingCaptureReceipt{OperationKey: in.OperationKey, Operation: "confirm", CaptureID: capture, AcceptedAt: now}})
	}))
	defer server.Close()
	svc := recordings.NewService(repo, "", nil, func() time.Time { return now }).WithPackages(assetclient.New(server.URL, "hhc-web-api", ""))
	failed := 0
	for round := 0; round < 3; round++ {
		request, cancel := context.WithTimeout(ctx, 2*time.Second)
		start := make(chan struct{})
		result := make(chan error, 50)
		for i := 0; i < 50; i++ {
			go func() {
				<-start
				_, e := svc.ResolveRecordingWatch(request, b.View.RecordingID, platform.NewID())
				result <- e
			}()
		}
		confirm := make(chan error, 1)
		go func() {
			<-start
			_, e := svc.ConfirmCapture(request, b.View.RecordingID, capture, actor.ActorID, assetclient.RecordingCaptureConfirmInput{OperationKey: fmt.Sprintf("concurrent-confirm-%d", round), Paths: []string{"1080p/seg-000000.m4s"}})
			confirm <- e
		}()
		close(start)
		for i := 0; i < 50; i++ {
			if e := <-result; e != nil {
				t.Errorf("watch: %v", e)
			}
		}
		e := <-confirm
		cancel()
		t.Logf("round=%d confirm=%v", round, e)
		if e != nil {
			failed++
		}
	}
	if failed != 0 {
		t.Fatalf("%d/3 concurrent confirms failed", failed)
	}
}
