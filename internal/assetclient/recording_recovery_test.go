package assetclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBroadcastCaptureRecoveryOptInWire(t *testing.T) {
	for _, version := range []string{"", "common-watermark-v1"} {
		t.Run(version, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var in map[string]any
				if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
					t.Fatal(e)
				}
				value, present := in["recoveryVersion"]
				if version == "" && present || version != "" && value != version {
					t.Errorf("recovery wire field=%v present=%v", value, present)
				}
				if in["broadcastEpoch"] != float64(1) || in["segmentFrames"] != float64(300) {
					t.Error("existing capture fields changed")
				}
				w.Write([]byte(`{}`))
			}))
			defer server.Close()
			_, e := New(server.URL, "hhc-web-api", "").CreateRecordingBroadcastCapture(context.Background(), "recording", "operation", 1, 300, version)
			if e != nil {
				t.Fatal(e)
			}
		})
	}
}
