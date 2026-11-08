package httpapi

import (
	"bytes"
	"context"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type revealRepository struct {
	recordings.Repository
	hash   string
	sealed recordings.SealedStreamKey
}

func (r revealRepository) ReadStreamKey(context.Context, string) (string, recordings.SealedStreamKey, error) {
	return r.hash, r.sealed, nil
}
func (r revealRepository) RetainStreamKey(context.Context, string, recordings.SealedStreamKey) error {
	return nil
}
func TestStudioRevealPermissionAuditAndPrivacy(t *testing.T) {
	key := "hhc_stream_" + strings.Repeat("A", 43)
	hash, _, err := recordings.HashBroadcastSourceKey(key)
	if err != nil {
		t.Fatal(err)
	}
	cipher, err := recordings.NewStreamKeyCipher("v1", map[string][]byte{"v1": bytes.Repeat([]byte{1}, 32)})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := cipher.Encrypt(hash, key)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		scope     string
		audit     bool
		status    int
		failAudit bool
	}{{"read", true, 403, false}, {"write", false, 503, false}, {"write", true, 200, false}, {"write", true, 503, true}} {
		outbox := &bulletinAuditOutbox{}
		if tc.failAudit {
			outbox.err = errors.New("audit storage unavailable")
		}
		handler := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).WithRecordings(recordings.NewService(revealRepository{hash: hash, sealed: sealed}, "", nil, time.Now).WithStreamKeyCipher(cipher))
		if tc.audit {
			handler.WithAuditOutbox(outbox, time.Now)
		}
		req := httptest.NewRequest("POST", "/api/admin/broadcast-sources/2c58a390-1906-4d62-b962-777f7f40bbb5/stream-key/reveal", nil)
		trustedBulletinAudit(req, "cms:recordings:"+tc.scope)
		res := httptest.NewRecorder()
		handler.Routes().ServeHTTP(res, req)
		if res.Code != tc.status {
			t.Fatalf("scope %s audit %v status %d", tc.scope, tc.audit, res.Code)
		}
		if tc.status == 200 {
			if len(outbox.events) != 1 || outbox.events[0].Action != "cms.broadcast_source.key.reveal" || outbox.events[0].ResourceID != "2c58a390-1906-4d62-b962-777f7f40bbb5" || outbox.events[0].RetentionClass != "security" || !strings.Contains(res.Body.String(), key) || !strings.Contains(res.Header().Get("Cache-Control"), "no-store") {
				t.Fatal("missing audited non-cacheable reveal")
			}
		} else if strings.Contains(res.Body.String(), key) {
			t.Fatal("leaked key")
		}
	}
}
