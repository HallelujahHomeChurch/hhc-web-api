package recordings

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestBroadcastSourceCredentialCanonical(t *testing.T) {
	key := "hhc_stream_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	hash, suffix, e := HashBroadcastSourceKey(key)
	if e != nil || len(hash) != 64 || suffix != "AAAA" {
		t.Fatalf("hash %q %q %v", hash, suffix, e)
	}
	for _, bad := range []string{"", key + "=", strings.Replace(key, "stream", "live", 1), key[:len(key)-1] + "B"} {
		if _, _, e := HashBroadcastSourceKey(bad); e == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	b := Broadcast{SourceManaged: true}
	if b.CheckStreamKey("") == nil || b.StreamKeyStatus().CanRotate {
		t.Fatal("source ownership bypass")
	}
}
