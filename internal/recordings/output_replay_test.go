package recordings

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/accountclient"
)

type replayRepositoryFixture struct {
	mu sync.Mutex
	*ingestRepositoryFixture
	snapshot           BroadcastOutputReplaySnapshot
	reads, credentials int
	err                error
}

func (r *replayRepositoryFixture) ReadBroadcastOutputReplay(ctx context.Context, hash string, _ BroadcastOutputReplayInput) (BroadcastOutputReplaySnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reads++
	if err := ctx.Err(); err != nil {
		return BroadcastOutputReplaySnapshot{}, err
	}
	if hash != r.hash {
		return BroadcastOutputReplaySnapshot{}, ErrBroadcastStreamKey
	}
	return r.snapshot, r.err
}
func (r *replayRepositoryFixture) BroadcastIngestCredentials(ctx context.Context, id string) (string, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.credentials++
	return r.ingestRepositoryFixture.BroadcastIngestCredentials(ctx, id)
}
func replayFixture(t *testing.T) (*Service, *replayRepositoryFixture, BroadcastOutputReplayInput, func()) {
	t.Helper()
	_, base, now, session, key := ingestFixture(t)
	receipt := BroadcastReceipt{OperationKey: "018f0000-0000-7000-8000-000000000004.bind", Operation: "bind", State: "completed"}
	in := BroadcastOutputReplayInput{ProtocolVersion: BroadcastOutputReplayProtocolVersion, OutputSessionID: "018f0000-0000-7000-8000-000000000004", SourceID: "018f0000-0000-7000-8000-000000000003", RecordingID: session.RecordingID, CaptureID: session.CaptureID, Epoch: session.Epoch, StreamKey: key, Receipt: &receipt}
	base.capture.Status.CaptureID = session.CaptureID
	repo := &replayRepositoryFixture{ingestRepositoryFixture: base, snapshot: BroadcastOutputReplaySnapshot{Owner: base.capture.ActorID, Capture: base.capture, Receipt: receipt, State: "active"}}
	account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"allowed":true}`)) }))
	svc := NewService(repo, "", nil, func() time.Time { return *now }).WithPublicationAuthorizer(accountclient.New(account.URL, "hhc-web-api"))
	return svc, repo, in, account.Close
}
func TestOutputReplayOnlyRenewsOriginalAndNeverCompletesBinding(t *testing.T) {
	for _, state := range []string{"active", "terminal", "unknown"} {
		t.Run(state, func(t *testing.T) {
			svc, repo, in, close := replayFixture(t)
			defer close()
			repo.snapshot.State = state
			if state == "unknown" {
				repo.err = ErrBroadcastOutputReplayUnknown
			}
			out, err := svc.ReplayBroadcastOutput(context.Background(), "replay-test", in)
			if state == "unknown" {
				if !errors.Is(err, ErrBroadcastOutputReplayUnknown) || repo.credentials != 0 {
					t.Fatalf("unknown minted credentials: %v", err)
				}
				return
			}
			if err != nil || out.State != state || out.SourceID != in.SourceID || out.OutputSessionID != in.OutputSessionID || out.RecordingID != in.RecordingID || out.CaptureID != in.CaptureID || out.Epoch != in.Epoch || out.Receipt != *in.Receipt {
				t.Fatalf("replay changed identity: %v", err)
			}
			if state == "terminal" {
				if out.Session != nil || repo.credentials != 0 {
					t.Fatal("terminal minted a session")
				}
			} else if out.Session == nil || out.Session.RecordingID != in.RecordingID || out.Session.CaptureID != in.CaptureID || out.Session.Epoch != in.Epoch {
				t.Fatal("session changed original pins")
			}
			if repo.reads != 2 {
				t.Fatal("original authority not rechecked after authorizer")
			}
		})
	}
}
func TestOutputReplayFencesAuthorityChangesDuringAuthorization(t *testing.T) {
	for _, change := range []string{"source key", "terminal", "owner"} {
		t.Run(change, func(t *testing.T) {
			svc, repo, in, close := replayFixture(t)
			defer close()
			account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				repo.mu.Lock()
				switch change {
				case "source key":
					repo.hash = "changed"
				case "terminal":
					repo.snapshot.State = "terminal"
				case "owner":
					repo.snapshot.Owner = "018f0000-0000-7000-8000-000000000009"
				}
				repo.mu.Unlock()
				w.Write([]byte(`{"allowed":true}`))
			}))
			defer account.Close()
			svc.WithPublicationAuthorizer(accountclient.New(account.URL, "hhc-web-api"))
			out, err := svc.ReplayBroadcastOutput(context.Background(), "replay-test", in)
			if change == "terminal" {
				if err != nil || out.State != "terminal" || out.Session != nil {
					t.Fatalf("late release not fenced: %v", err)
				}
			} else if err == nil || repo.credentials != 0 {
				t.Fatal("late authority change issued session")
			}
		})
	}
}
func TestOutputReplayDeniedOrCancelledCannotIssueSession(t *testing.T) {
	for _, deny := range []bool{true, false} {
		t.Run(fmt.Sprint(deny), func(t *testing.T) {
			svc, repo, in, close := replayFixture(t)
			defer close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if deny {
				account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"allowed":false}`)) }))
				defer account.Close()
				svc.WithPublicationAuthorizer(accountclient.New(account.URL, "hhc-web-api"))
			} else {
				cancel()
			}
			if _, err := svc.ReplayBroadcastOutput(ctx, "replay-test", in); err == nil || repo.credentials != 0 {
				t.Fatal("denied replay issued session")
			}
		})
	}
}
