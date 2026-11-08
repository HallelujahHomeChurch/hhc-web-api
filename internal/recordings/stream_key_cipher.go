package recordings

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

// Uses the Notification service envelope pattern with a CMS-owned keyring.
type StreamKeyCipher struct {
	active string
	keys   map[string]cipher.AEAD
}
type SealedStreamKey struct {
	KeyID      string `json:"-"`
	Ciphertext []byte `json:"-"`
}

var ErrStreamKeyUnavailable = errors.New("stream key has not been securely retained")

func NewStreamKeyCipher(active string, keys map[string][]byte) (*StreamKeyCipher, error) {
	c := &StreamKeyCipher{active: active, keys: map[string]cipher.AEAD{}}
	for id, key := range keys {
		if id == "" || len(key) != 32 {
			return nil, errors.New("stream key encryption requires named AES-256 keys")
		}
		block, e := aes.NewCipher(key)
		if e != nil {
			return nil, e
		}
		aead, e := cipher.NewGCM(block)
		if e != nil {
			return nil, e
		}
		c.keys[id] = aead
	}
	if c.keys[active] == nil {
		return nil, errors.New("active stream encryption key is missing")
	}
	return c, nil
}
func (c *StreamKeyCipher) Encrypt(hash, plain string) (SealedStreamKey, error) {
	if c == nil || hash == "" {
		return SealedStreamKey{}, ErrStreamKeyUnavailable
	}
	a := c.keys[c.active]
	nonce := make([]byte, a.NonceSize())
	if _, e := rand.Read(nonce); e != nil {
		return SealedStreamKey{}, e
	}
	return SealedStreamKey{KeyID: c.active, Ciphertext: a.Seal(nonce, nonce, []byte(plain), []byte("hhc:stream-key:v1:"+hash))}, nil
}
func (c *StreamKeyCipher) Decrypt(hash string, sealed SealedStreamKey) (string, error) {
	if c == nil || hash == "" {
		return "", ErrStreamKeyUnavailable
	}
	if sealed.KeyID == "" && len(sealed.Ciphertext) == 0 {
		return "", ErrStreamKeyUnavailable
	}
	a := c.keys[sealed.KeyID]
	if a == nil || len(sealed.Ciphertext) < a.NonceSize()+a.Overhead() {
		return "", assetclient.ErrUnavailable
	}
	plain, e := a.Open(nil, sealed.Ciphertext[:a.NonceSize()], sealed.Ciphertext[a.NonceSize():], []byte("hhc:stream-key:v1:"+hash))
	if e != nil {
		return "", assetclient.ErrUnavailable
	}
	return string(plain), nil
}
func (s *Service) WithStreamKeyCipher(c *StreamKeyCipher) *Service { s.streamKeyCipher = c; return s }
