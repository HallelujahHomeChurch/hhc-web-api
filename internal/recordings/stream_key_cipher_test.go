package recordings

import (
	"bytes"
	"testing"
)

func TestStreamKeyCipher(t *testing.T) {
	keys := map[string][]byte{"v1": bytes.Repeat([]byte{1}, 32), "v2": bytes.Repeat([]byte{2}, 32)}
	old, err := NewStreamKeyCipher("v1", keys)
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := old.Encrypt("hash-one", "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	current, _ := NewStreamKeyCipher("v2", keys)
	plain, err := current.Decrypt("hash-one", sealed)
	if err != nil || plain != "test-secret" {
		t.Fatal("versioned round trip failed")
	}
	if _, err = current.Decrypt("hash-two", sealed); err == nil {
		t.Fatal("accepted another source hash")
	}
	sealed.Ciphertext[len(sealed.Ciphertext)-1] ^= 1
	if _, err = current.Decrypt("hash-one", sealed); err == nil {
		t.Fatal("accepted tampering")
	}
	if _, err = NewStreamKeyCipher("missing", keys); err == nil {
		t.Fatal("accepted missing active key")
	}
	if _, err = NewStreamKeyCipher("bad", map[string][]byte{"bad": {1}}); err == nil {
		t.Fatal("accepted weak key")
	}
}
