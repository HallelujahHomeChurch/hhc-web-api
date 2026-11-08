package recordings

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

var ErrBroadcastStreamKey = fmt.Errorf("%w: broadcast stream key invalid", ErrForbidden)

type BroadcastStreamKeyInput struct {
	BroadcastMutationInput
	StreamKey string `json:"streamKey"`
}
type BroadcastStreamKeyBindInput struct {
	BroadcastBindInput
	StreamKey string `json:"streamKey"`
}
type BroadcastStreamKeyStatus struct {
	Configured bool    `json:"configured"`
	CanRotate  bool    `json:"canRotate"`
	Revision   int64   `json:"revision"`
	Suffix     *string `json:"suffix"`
}

func HashBroadcastStreamKey(id, key string) (string, string, error) {
	prefix := "hhc_live_" + id + "_"
	if !validLiveUUID(id) || len(key) != len(prefix)+43 || !strings.HasPrefix(key, prefix) {
		return "", "", ErrInvalid
	}
	encoded := key[len(prefix):]
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != encoded {
		return "", "", ErrInvalid
	}
	digest := sha256.Sum256([]byte(key))
	return hex.EncodeToString(digest[:]), key[len(key)-4:], nil
}
func (b Broadcast) StreamKeyStatus() BroadcastStreamKeyStatus {
	v := BroadcastStreamKeyStatus{Configured: b.StreamKeyHash != "", CanRotate: !b.SourceManaged && b.CanChangePolicy() && b.View.Boundaries.StartSequence == nil && !b.Revoked && (b.View.Phase == "draft" || b.View.Phase == "scheduled"), Revision: b.View.Revision}
	if v.Configured {
		suffix := b.StreamKeySuffix
		v.Suffix = &suffix
	}
	return v
}

// Empty hashes preserve the legacy binding only for events without a key.
func (b Broadcast) CheckStreamKey(hash string) error {
	if b.SourceManaged {
		return ErrBroadcastStreamKey
	}
	if b.StreamKeyHash == "" && hash == "" {
		return nil
	}
	if b.StreamKeyHash == "" || hash == "" || subtle.ConstantTimeCompare([]byte(b.StreamKeyHash), []byte(hash)) != 1 {
		return ErrBroadcastStreamKey
	}
	return nil
}
func (s *Service) GetBroadcastStreamKey(ctx context.Context, id string) (BroadcastStreamKeyStatus, error) {
	repo, err := s.broadcastRepository()
	if err != nil {
		return BroadcastStreamKeyStatus{}, err
	}
	b, err := repo.GetBroadcast(ctx, id)
	if err != nil {
		return BroadcastStreamKeyStatus{}, err
	}
	return b.StreamKeyStatus(), nil
}
func (s *Service) SetBroadcastStreamKey(ctx context.Context, id, actor string, in BroadcastStreamKeyInput) (BroadcastStreamKeyStatus, error) {
	if !s.broadcastWriters {
		return BroadcastStreamKeyStatus{}, assetclient.ErrUnavailable
	}
	hash, suffix, err := HashBroadcastStreamKey(id, in.StreamKey)
	if err != nil || in.ExpectedRevision < 1 {
		return BroadcastStreamKeyStatus{}, ErrInvalid
	}
	// Only the digest enters durable mutation intents and audit receipts.
	sanitized := struct {
		BroadcastMutationInput
		Hash   string `json:"keyHash"`
		Suffix string `json:"suffix"`
	}{in.BroadcastMutationInput, hash, suffix}
	_, err = s.mutateBroadcast(ctx, id, actor, in.OperationKey, "stream-key", in.ExpectedRevision, sanitized, func(ctx context.Context, b *Broadcast, _ *Recording, _ *Capture) error {
		if !b.StreamKeyStatus().CanRotate {
			return ErrConflict
		}
		b.StreamKeyHash, b.StreamKeySuffix = hash, suffix
		return nil
	})
	if err != nil {
		return BroadcastStreamKeyStatus{}, err
	}
	repo, err := s.broadcastRepository()
	if err != nil {
		return BroadcastStreamKeyStatus{}, err
	}
	current, err := repo.GetBroadcast(ctx, id)
	if err != nil {
		return BroadcastStreamKeyStatus{}, err
	}
	if current.CheckStreamKey(hash) != nil {
		return BroadcastStreamKeyStatus{}, ErrBroadcastOperation
	}
	return current.StreamKeyStatus(), nil
}

type broadcastStreamKeyRepository interface {
	PrepareBroadcastStreamKeyBinding(context.Context, string, string, BroadcastBindInput, string, time.Time) (Broadcast, Capture, error)
}

func (s *Service) BindBroadcastWithStreamKey(ctx context.Context, id, actor string, in BroadcastStreamKeyBindInput, canPublish bool) (BroadcastMutationResult, error) {
	hash, _, err := HashBroadcastStreamKey(id, in.StreamKey)
	if err != nil {
		return BroadcastMutationResult{}, ErrBroadcastStreamKey
	}
	return s.bindBroadcast(ctx, id, actor, in.BroadcastBindInput, canPublish, hash)
}
