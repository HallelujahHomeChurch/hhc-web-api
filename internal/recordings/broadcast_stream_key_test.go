package recordings

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestBroadcastStreamKeyCanonical(t *testing.T) {
	id := "0199a000-0000-4000-8000-000000000003"
	key := "hhc_live_" + id + "_" + base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	hash, suffix, err := HashBroadcastStreamKey(id, key)
	if err != nil || len(hash) != 64 || suffix != key[len(key)-4:] {
		t.Fatalf("hash=%q suffix=%q err=%v", hash, suffix, err)
	}
	for _, value := range []string{"", key + "=", strings.Replace(key, id, "0199a000-0000-4000-8000-000000000004", 1), key[:len(key)-1] + "B", key[:len(key)-1], strings.ToUpper(key)} {
		if _, _, err := HashBroadcastStreamKey(id, value); !errors.Is(err, ErrInvalid) {
			t.Fatalf("accepted noncanonical key %q: %v", value, err)
		}
	}
	b := Broadcast{StreamKeyHash: hash, StreamKeySuffix: suffix, View: BroadcastView{Phase: "draft", Revision: 1}}
	if !b.StreamKeyStatus().Configured || !b.StreamKeyStatus().CanRotate {
		t.Fatal("draft key metadata")
	}
	raw, _ := json.Marshal(b.View)
	if strings.Contains(string(raw), hash) || strings.Contains(string(raw), suffix) {
		t.Fatal("key data leaked into B1 view")
	}
	if err := b.CheckStreamKey(hash); err != nil {
		t.Fatal(err)
	}
	for _, h := range []string{"", strings.Repeat("b", 64)} {
		if !errors.Is(b.CheckStreamKey(h), ErrBroadcastStreamKey) {
			t.Fatal("wrong key accepted")
		}
	}
	if err := (Broadcast{}).CheckStreamKey(""); err != nil {
		t.Fatal("legacy bind denied")
	}
	if !errors.Is((Broadcast{}).CheckStreamKey(hash), ErrBroadcastStreamKey) {
		t.Fatal("keyed bind without configured key")
	}
}
func TestBroadcastStreamKeyRotationBoundary(t *testing.T) {
	for _, phase := range []string{"draft", "scheduled"} {
		b := Broadcast{View: BroadcastView{Phase: phase}}
		if !b.StreamKeyStatus().CanRotate {
			t.Fatal(phase)
		}
	}
	start := 0
	cases := []Broadcast{
		{View: BroadcastView{Phase: "draft", Boundaries: BroadcastBoundaries{StartSequence: &start}}},
		{View: BroadcastView{Phase: "cancelled"}}, {View: BroadcastView{Phase: "failed"}},
		{View: BroadcastView{Phase: "archived"}}, {View: BroadcastView{Phase: "preview"}},
		{View: BroadcastView{Phase: "draft"}, CaptureIntentID: "pending"},
		{View: BroadcastView{Phase: "draft"}, Epoch: 1}, {View: BroadcastView{Phase: "draft"}, Revoked: true},
	}
	for _, b := range cases {
		if b.StreamKeyStatus().CanRotate {
			t.Fatalf("rotatable %+v", b)
		}
	}
}
