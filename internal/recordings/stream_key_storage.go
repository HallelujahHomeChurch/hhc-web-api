package recordings

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

type StreamKeyRepository interface {
	ReadStreamKey(context.Context, string) (string, SealedStreamKey, error)
	RetainStreamKey(context.Context, string, SealedStreamKey) error
}

func (s *Service) RevealStudioStreamKey(ctx context.Context, id, actor string) (string, error) {
	if !validLiveUUID(id) {
		return "", ErrNotFound
	}
	if e := broadcastActor(ctx, actor); e != nil {
		return "", e
	}
	r, ok := s.repo.(StreamKeyRepository)
	if !ok || s.streamKeyCipher == nil {
		return "", assetclient.ErrUnavailable
	}
	hash, sealed, e := r.ReadStreamKey(ctx, id)
	if e != nil {
		return "", e
	}
	key, e := s.streamKeyCipher.Decrypt(hash, sealed)
	if e != nil {
		return "", e
	}
	actual, _, e := HashBroadcastSourceKey(key)
	if e != nil || actual != hash {
		return "", assetclient.ErrUnavailable
	}
	return key, nil
}
func (s *Service) retainAuthenticatedStreamKey(ctx context.Context, hash, key string) error {
	if s.streamKeyCipher == nil {
		return nil
	}
	r, ok := s.repo.(StreamKeyRepository)
	if !ok {
		return assetclient.ErrUnavailable
	}
	sealed, e := s.streamKeyCipher.Encrypt(hash, key)
	if e != nil {
		return e
	}
	return r.RetainStreamKey(ctx, hash, sealed)
}
