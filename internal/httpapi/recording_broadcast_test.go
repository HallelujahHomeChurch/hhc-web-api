package httpapi

import (
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBroadcastRoutesAuthenticateAndFailClosed(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).Routes()
	for _, path := range []string{"/api/admin/broadcast-capabilities", "/api/admin/broadcasts", "/api/admin/broadcasts/0199a000-0000-4000-8000-000000000003"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		trustedBulletinAudit(r, "cms:recordings:read")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 503 || !strings.Contains(w.Body.String(), "unavailable") {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		if w.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("cache boundary missing")
		}
	}
}
func TestBroadcastCannotUseServiceIdentity(t *testing.T) {
	h := NewWithContent(nil, nil, nil, nil, "api-gateway", "", true).Routes()
	r := serviceRequest(http.MethodPost, "/api/admin/broadcasts")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("service accepted %d", w.Code)
	}
}
func TestBroadcastAckRejectsMissingOrNullMarkerBoundary(t *testing.T) {
	for _, body := range []string{`{"operationKey":"ack","captureId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","epoch":1}`, `{"operationKey":"ack","captureId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","epoch":1,"boundarySequence":null}`} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		var in recordings.BroadcastMarkerAckInput
		if broadcastDecode(w, r, &in, 2048) {
			t.Fatalf("inferred marker sequence0 from %s", body)
		}
		if w.Code != 400 || !strings.Contains(w.Body.String(), "broadcast_invalid") {
			t.Fatal(w.Body.String())
		}
	}
}

func TestBroadcastAdminPaginationAcceptsCanonicalAll(t *testing.T) {
	for _, filter := range []string{"all", "selectable"} {
		r := httptest.NewRequest(http.MethodGet, "/api/admin/broadcasts?filter="+filter, nil)
		if _, _, e := broadcastPagination(r); e != nil {
			t.Fatalf("canonical filter=%s rejected: %v", filter, e)
		}
	}
}

func TestBroadcastConflictCodesMatchCanonicalWire(t *testing.T) {
	for _, tc := range []struct {
		err  error
		code string
	}{{recordings.ErrBroadcastEpoch, "broadcast_epoch_mismatch"}, {recordings.ErrBroadcastBoundary, "broadcast_boundary_conflict"}, {recordings.ErrBroadcastOperation, "broadcast_operation_conflict"}, {recordings.ErrBroadcastBound, "broadcast_already_bound"}} {
		w := httptest.NewRecorder()
		broadcastError(w, tc.err)
		if w.Code != 409 || !strings.Contains(w.Body.String(), tc.code) {
			t.Fatalf("code=%s status=%d body=%s", tc.code, w.Code, w.Body)
		}
	}
}

func TestBroadcastConflictDiagnosticsStayPrivate(t *testing.T) {
	w := httptest.NewRecorder()
	broadcastError(w, fmt.Errorf("broadcast reconcile stage=projection_poll recording_ref=private-diagnostic: %w", assetclient.ErrConflict))
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "broadcast_state_conflict") || strings.Contains(w.Body.String(), "projection_poll") || strings.Contains(w.Body.String(), "private-diagnostic") {
		t.Fatalf("private diagnostic escaped generic API error: %d %s", w.Code, w.Body.String())
	}
}
