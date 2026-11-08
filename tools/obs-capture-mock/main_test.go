package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestMockSealAcceptsActualFractionalFrameRateSegments(t *testing.T) {
	h, err := newMock("../../openapi.yaml", "../../docs/obs-capture/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../../docs/obs-capture/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []fixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, f := range fixtures {
		if f.Name != "seal_2997_eight_actual_segments" {
			continue
		}
		body, err := json.Marshal(f.Value)
		if err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest("POST", "/api/admin/recordings/018f0c1f-18d0-7e81-9f6f-69c456db7003/captures/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/seal", strings.NewReader(string(body)))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 202 {
			t.Fatalf("seal schema regression: %d %s", w.Code, w.Body)
		}
		return
	}
	t.Fatal("29.97fps seal fixture missing")
}

func TestMockUsesCanonicalContractAndRejectsInvalidInput(t *testing.T) {
	h, err := newMock("../../openapi.yaml", "../../docs/obs-capture/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body, scenario string
		want           int
	}{
		{`{"operationKey":"start-1","expectedVersion":1,"autoPublish":true,"liveEnabled":false}`, "", 201},
		{`{"operationKey":"start-1","expectedVersion":1}`, "", 400},
		{`{"operationKey":"start-1","expectedVersion":1,"autoPublish":false,"liveEnabled":true}`, "conflicting_key", 409},
		{`{"operationKey":"start-1","expectedVersion":1,"autoPublish":false,"liveEnabled":true}`, "temporary_failure", 503},
	} {
		r := httptest.NewRequest("POST", "/api/admin/recordings/018f0c1f-18d0-7e81-9f6f-69c456db7003/captures", strings.NewReader(tc.body))
		r.Header.Set("X-OBS-Mock-Scenario", tc.scenario)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("want %d got %d %s", tc.want, w.Code, w.Body)
		}
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("cacheable fixture")
		}
	}
}

func TestMockRejectsMalformedPlaybackScopeUUID(t *testing.T) {
	h, err := newMock("../../openapi.yaml", "../../docs/obs-capture/fixtures.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		scope string
		want  int
	}{
		{"018f0c1f-18d0-7e81-9f6f-69c456db7004", 200},
		{"not-a-uuid", 400},
	} {
		t.Run(tc.scope, func(t *testing.T) {
			body := `{"captureId":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","playbackScopeId":"` + tc.scope + `"}`
			r := httptest.NewRequest("POST", "/api/member/recordings/018f0c1f-18d0-7e81-9f6f-69c456db7003/live/playback", strings.NewReader(body))
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("want %d got %d %s", tc.want, w.Code, w.Body)
			}
		})
	}
}
