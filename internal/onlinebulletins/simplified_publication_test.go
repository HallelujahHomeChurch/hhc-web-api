package onlinebulletins

import (
	"strings"
	"testing"
)

func TestSimplifiedRecoveryKeyGrammar(t *testing.T) {
	for _, key := range []string{"", " a", "a ", "a\n", "a\x00b", "a\x7fb", strings.Repeat("é", 65)} {
		if ValidSimplifiedRecoveryKey(key) {
			t.Fatalf("accepted invalid key %q", key)
		}
	}
	for _, key := range []string{"plain", "é", strings.Repeat("a", 128)} {
		if !ValidSimplifiedRecoveryKey(key) {
			t.Fatal("rejected valid key")
		}
	}
}
