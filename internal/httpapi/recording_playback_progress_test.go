package httpapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func TestProgressClaimBody(t *testing.T) {
	const valid = `{"sessionId":"e690dce0-babd-44d5-b502-3fd10b6962c4","claimId":"e690dce0-babd-44d5-b502-3fd10b6962c4","expectedGeneration":null,"claimToken":"test","mediaVersion":"vod:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","startKind":"restart","observedPositionSeconds":0}`
	for _, tc := range []struct {
		name, body string
		want       bool
	}{
		{"first claim", valid, true},
		{"missing generation", strings.Replace(valid, `"expectedGeneration":null,`, "", 1), false},
		{"missing observed position", strings.Replace(valid, `,"observedPositionSeconds":0`, "", 1), false},
		{"unknown account", strings.Replace(valid, "{", `{"accountId":"attacker",`, 1), false},
		{"too large", valid + strings.Repeat(" ", 4096), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", "/api/member/recordings/id/progress/claim", strings.NewReader(tc.body))
			var input recordings.ProgressClaim
			if got := broadcastDecode(httptest.NewRecorder(), r, &input, 4096); got != tc.want {
				t.Fatalf("accepted=%t, want %t", got, tc.want)
			}
		})
	}
}
