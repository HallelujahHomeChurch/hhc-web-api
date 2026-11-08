package recordings

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/accountclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type ingestRepositoryFixture struct {
	Repository
	BroadcastRepository
	CaptureRepository
	BroadcastIngestRepository
	hash    string
	secret  string
	b       Broadcast
	capture Capture
}

func (r *ingestRepositoryFixture) BroadcastIngestCredentials(context.Context, string) (string, string, error) {
	return r.hash, r.secret, nil
}
func (r *ingestRepositoryFixture) GetBroadcast(context.Context, string) (Broadcast, error) {
	return r.b, nil
}
func (r *ingestRepositoryFixture) BroadcastIngestCapture(context.Context, string, string) (Capture, error) {
	return r.capture, nil
}
func ingestFixture(t *testing.T) (*Service, *ingestRepositoryFixture, *time.Time, BroadcastIngestSession, string) {
	t.Helper()
	now := time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	key := "hhc_stream_" + strings.Repeat("A", 43)
	hash, _, _ := HashBroadcastSourceKey(key)
	actor := "018f0000-0000-7000-8000-000000000001"
	id := "018f0000-0000-7000-8000-000000000002"
	capture := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	r := &ingestRepositoryFixture{hash: hash, secret: strings.Repeat("1", 64), b: Broadcast{SourceManaged: true, Epoch: 1, CaptureIntentID: "intent", View: BroadcastView{Binding: &BroadcastBinding{CaptureID: capture, Epoch: 1, ActorID: actor}}}, capture: Capture{IntentID: "intent", ActorID: actor, ActorType: "user", BroadcastEpoch: 1, Status: CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{ExpiresAt: now.Add(24 * time.Hour)}}}}
	s := NewService(r, "", nil, func() time.Time { return now })
	session, e := s.issueIngestSession(ingestSigningKey(hash, r.secret), ingestClaims{RecordingID: id, CaptureID: capture, Epoch: 1, ActorID: actor}, r.capture)
	if e != nil {
		t.Fatal(e)
	}
	return s, r, &now, session, key
}
func TestBroadcastIngestSessionScopeAndRevocation(t *testing.T) {
	for _, kind := range []string{"valid", "bad signature", "wrong event", "wrong capture", "expired", "key reset", "revoked", "legacy capture", "new epoch", "wrong owner", "wrong intent", "capture expired"} {
		t.Run(kind, func(t *testing.T) {
			s, r, now, session, _ := ingestFixture(t)
			token, id, capture := session.Token, session.RecordingID, session.CaptureID
			switch kind {
			case "bad signature":
				token = token[:len(token)-2] + "AA"
			case "wrong event":
				id = "018f0000-0000-7000-8000-000000000003"
			case "wrong capture":
				capture = strings.Repeat("b", 32)
			case "expired":
				*now = now.Add(15 * time.Minute)
			case "key reset":
				r.hash = strings.Repeat("f", 64)
			case "revoked":
				r.b.Revoked = true
			case "legacy capture":
				r.b.SourceManaged = false
			case "new epoch":
				r.b.Epoch++
			case "wrong owner":
				r.capture.ActorID = "other"
			case "wrong intent":
				r.capture.IntentID = "other"
			case "capture expired":
				r.capture.Status.ExpiresAt = *now
			}
			_, _, e := s.AuthorizeBroadcastIngest(context.Background(), token, id, capture, false)
			if kind == "valid" {
				if e != nil {
					t.Fatal(e)
				}
			} else if kind == "revoked" {
				if !errors.Is(e, ErrBroadcastIngestRevoked) {
					t.Fatal(e)
				}
			} else if !errors.Is(e, ErrBroadcastIngestSession) {
				t.Fatalf("boundary accepted %v", e)
			}
		})
	}
}
func TestBroadcastIngestRenewExpiredPinsOriginalCaptureAndChecksPermission(t *testing.T) {
	s, r, now, session, key := ingestFixture(t)
	allowed := true
	fail := false
	calls := 0
	account := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if fail {
			w.WriteHeader(503)
			return
		}
		if allowed {
			w.Write([]byte(`{"allowed":true}`))
		} else {
			w.Write([]byte(`{"allowed":false}`))
		}
	}))
	defer account.Close()
	s.WithPublicationAuthorizer(accountclient.New(account.URL, "hhc-web-api"))
	*now = now.Add(time.Hour)
	renewed, e := s.RenewBroadcastIngest(context.Background(), BroadcastIngestRenewInput{key, session.Token})
	if e != nil || renewed.RecordingID != session.RecordingID || renewed.CaptureID != session.CaptureID || renewed.Epoch != session.Epoch {
		t.Fatalf("renew changed destination %v", e)
	}
	if _, _, e = s.AuthorizeBroadcastIngest(context.Background(), renewed.Token, session.RecordingID, session.CaptureID, false); e != nil {
		t.Fatal(e)
	}
	// The current source target is intentionally absent from session validation.
	r.b.View.Phase = "processing"
	if _, _, e = s.AuthorizeBroadcastIngest(context.Background(), renewed.Token, session.RecordingID, session.CaptureID, false); e != nil {
		t.Fatal(e)
	}
	allowed = false
	if _, e = s.RenewBroadcastIngest(context.Background(), BroadcastIngestRenewInput{key, session.Token}); !errors.Is(e, ErrForbidden) {
		t.Fatal(e)
	}
	fail = true
	if _, e = s.RenewBroadcastIngest(context.Background(), BroadcastIngestRenewInput{key, session.Token}); !errors.Is(e, assetclient.ErrUnavailable) {
		t.Fatal(e)
	}
	if calls != 3 {
		t.Fatal(calls)
	}
}

func TestBroadcastIngestSenderCannotMintSessionFromStreamKey(t *testing.T) {
	s, r, _, session, _ := ingestFixture(t)
	claims := ingestClaims{Version: BroadcastIngestProtocolVersion, RecordingID: session.RecordingID, CaptureID: session.CaptureID, ActorID: r.capture.ActorID, Epoch: 1, Expires: session.ExpiresAt.Unix()}
	raw, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(raw)
	// A sender knows the stream key and can compute its hash, but not the server seed.
	sig, _ := ingestMAC(r.hash, payload)
	forged := "hhc_ingest_" + payload + "." + base64.RawURLEncoding.EncodeToString(sig)
	if _, _, e := s.AuthorizeBroadcastIngest(context.Background(), forged, session.RecordingID, session.CaptureID, false); !errors.Is(e, ErrBroadcastIngestSession) {
		t.Fatal("sender minted a session", e)
	}
}

func TestBroadcastIngestSessionAcceptsSmallReplicaClockDifference(t *testing.T) {
	s, _, now, session, _ := ingestFixture(t)
	*now = now.Add(-time.Second)
	if _, _, e := s.AuthorizeBroadcastIngest(context.Background(), session.Token, session.RecordingID, session.CaptureID, false); e != nil {
		t.Fatal("fresh server-issued session rejected by a lagging replica", e)
	}
}

func TestBroadcastIngestRevocationPermitsOnlyScopedCleanup(t *testing.T) {
	s, r, _, session, _ := ingestFixture(t)
	r.b.Revoked = true
	if _, _, e := s.AuthorizeBroadcastIngest(context.Background(), session.Token, session.RecordingID, session.CaptureID, false); !errors.Is(e, ErrBroadcastIngestRevoked) {
		t.Fatal(e)
	}
	if _, _, e := s.AuthorizeBroadcastIngest(context.Background(), session.Token, session.RecordingID, session.CaptureID, true); e != nil {
		t.Fatal("cleanup blocked", e)
	}
	if _, _, e := s.AuthorizeBroadcastIngest(context.Background(), session.Token, session.RecordingID, strings.Repeat("b", 32), true); !errors.Is(e, ErrBroadcastIngestSession) {
		t.Fatal("cleanup escaped scope", e)
	}
}

func TestBroadcastIngestRevokedCleanupCanRenewWithoutRestoringTransport(t *testing.T) {
	s, r, now, session, key := ingestFixture(t)
	r.b.Revoked = true
	*now = now.Add(time.Hour)
	renewed, e := s.RenewBroadcastIngest(context.Background(), BroadcastIngestRenewInput{key, session.Token})
	if e != nil {
		t.Fatal("cleanup renewal required the revoked authorizer", e)
	}
	if _, _, e = s.AuthorizeBroadcastIngest(context.Background(), renewed.Token, session.RecordingID, session.CaptureID, false); !errors.Is(e, ErrBroadcastIngestRevoked) {
		t.Fatal("renewal restored transfer", e)
	}
	if _, _, e = s.AuthorizeBroadcastIngest(context.Background(), renewed.Token, session.RecordingID, session.CaptureID, true); e != nil {
		t.Fatal(e)
	}
}
