package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/engagementclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

func TestInitialNotificationAcceptedResponseLostReplaysOneStableCommand(t *testing.T) {
	db, repo, source, _, now := seedJourney(t)
	id := platform.NewID()
	key := "notification:v2:bulletin:" + source.Canonical.IssueID + ":general:g0"
	payload := publication.NotificationSubmissionPayload{Name: "Synthetic bulletin", Source: &publication.BulletinSource{Kind: "bulletin", IssueID: source.Canonical.IssueID, Series: "general", NotificationGeneration: 0}, ActorID: postgresBulletinAuditActor, RequestID: id, IdempotencyKey: key, Notifications: []publication.NotificationCommand{{Channel: "email", AudienceType: "bulletin_members"}}}
	raw, _ := json.Marshal(payload)
	if _, e := db.Exec(`DELETE FROM hhc_web.outbox_event`); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO hhc_web.outbox_event(id,destination,event_type,aggregate_type,aggregate_id,aggregate_version,payload_json,idempotency_key,status,next_attempt_at,created_at,updated_at)VALUES($1,'engagement-api','bulletin.notification.submit','bulletin',$2,97,$3,$4,'pending',$5,$5,$5)`, id, source.Canonical.IssueID, raw, key, now); e != nil {
		t.Fatal(e)
	}
	var mu sync.Mutex
	requests, accepted := 0, 0
	var expected [32]byte
	var protocolError string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, e := io.ReadAll(r.Body)
		mu.Lock()
		requests++
		attempt := requests
		hash := sha256.Sum256(body)
		if e != nil || r.Method != "POST" || r.URL.Path != "/priv/campaign-submissions" || r.Header.Get("Idempotency-Key") != key || r.Header.Get("X-HHC-Request-ID") != id || r.Header.Get("X-HHC-Actor-ID") != postgresBulletinAuditActor {
			protocolError = "command identity changed"
		}
		if attempt == 1 {
			expected = hash
			accepted++
		} else if hash != expected {
			protocolError = "command payload changed"
		}
		mu.Unlock()
		if attempt == 1 {
			connection, _, e := w.(http.Hijacker).Hijack()
			if e == nil {
				connection.Close()
			}
			return
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- publication.NewWorker(repo, nil, 5, engagementclient.New(server.URL, "hhc-web-api")).Run(ctx)
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	delivered := false
loop:
	for {
		select {
		case <-ctx.Done():
			break loop
		case <-ticker.C:
			var status string
			if e := db.QueryRow(`SELECT status FROM hhc_web.outbox_event WHERE id=$1`, id).Scan(&status); e != nil {
				t.Fatal(e)
			}
			if status == "delivered" {
				delivered = true
				break loop
			}
		}
	}
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if !delivered || requests != 2 || accepted != 1 || protocolError != "" {
		t.Fatalf("fixture acceptance delivered=%v requests=%d accepted=%d error=%s", delivered, requests, accepted, protocolError)
	}
	var count int
	if e := db.QueryRow(`SELECT count(*) FROM hhc_web.outbox_event WHERE aggregate_id=$1 AND event_type='bulletin.notification.submit'`, source.Canonical.IssueID).Scan(&count); e != nil || count != 1 {
		t.Fatal("duplicate initial command", count, e)
	}
	state, e := repo.OnlineBulletinState(context.Background(), source.Canonical.IssueID, "general", "zh-Hans")
	if e != nil || state.Notification == nil || state.Notification.Status != "accepted" || state.Notification.CanonicalVersion != nil {
		t.Fatal(fmt.Sprintf("notification projection %#v %v", state.Notification, e))
	}
}
