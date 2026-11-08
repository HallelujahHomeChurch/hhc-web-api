package recordings

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"strings"
	"time"
)

var ErrBroadcastSourceBusy = fmt.Errorf("%w: broadcast source busy", ErrConflict)
var ErrBroadcastSourceNotPrepared = fmt.Errorf("%w: broadcast source not prepared", ErrConflict)

type BroadcastSource struct {
	Revision          int64   `json:"revision"`
	Configured        bool    `json:"configured"`
	CanRotate         bool    `json:"canRotate"`
	InputBusy         bool    `json:"inputBusy"`
	Suffix            *string `json:"suffix"`
	TargetRecordingID *string `json:"targetRecordingId"`
}
type BroadcastSourceStreamKeyInput struct {
	OperationKey           string `json:"operationKey"`
	ExpectedSourceRevision int64  `json:"expectedSourceRevision"`
	StreamKey              string `json:"streamKey"`
}
type BroadcastSourceTargetInput struct {
	OperationKey           string `json:"operationKey"`
	ExpectedSourceRevision int64  `json:"expectedSourceRevision"`
	RecordingID            string `json:"recordingId"`
	ExpectedRevision       int64  `json:"expectedRevision"`
}
type BroadcastSourceResolveInput struct {
	StreamKey string `json:"streamKey"`
}
type BroadcastSourceResolved struct {
	SourceRevision    int64  `json:"sourceRevision"`
	RecordingID       string `json:"recordingId"`
	BroadcastRevision int64  `json:"broadcastRevision"`
}
type BroadcastSourceBindInput struct {
	BroadcastBindInput
	StreamKey              string `json:"streamKey"`
	ExpectedSourceRevision int64  `json:"expectedSourceRevision"`
	RecordingID            string `json:"recordingId"`
}
type BroadcastSourceRepository interface {
	GetBroadcastSource(context.Context) (BroadcastSource, error)
	SetBroadcastSourceKey(context.Context, string, BroadcastSourceStreamKeyInput, string, string, time.Time) (BroadcastSource, error)
	SetBroadcastSourceTarget(context.Context, string, BroadcastSourceTargetInput, time.Time) (BroadcastSource, error)
	ResolveBroadcastSource(context.Context, string) (BroadcastSourceResolved, error)
	PrepareBroadcastSourceBinding(context.Context, string, BroadcastSourceBindInput, string, bool, time.Time) (Broadcast, Capture, error)
}

func HashBroadcastSourceKey(key string) (string, string, error) {
	const prefix = "hhc_stream_"
	if len(key) != len(prefix)+43 || !strings.HasPrefix(key, prefix) {
		return "", "", ErrInvalid
	}
	encoded := key[len(prefix):]
	raw, e := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if e != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != encoded {
		return "", "", ErrInvalid
	}
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:]), key[len(key)-4:], nil
}
func (s *Service) sourceRepository() (BroadcastSourceRepository, error) {
	repo, e := s.broadcastRepository()
	if e != nil {
		return nil, e
	}
	r, ok := repo.(BroadcastSourceRepository)
	if !ok {
		return nil, assetclient.ErrUnavailable
	}
	return r, nil
}
func (s *Service) GetBroadcastSource(ctx context.Context) (BroadcastSource, error) {
	r, e := s.sourceRepository()
	if e != nil {
		return BroadcastSource{}, e
	}
	return r.GetBroadcastSource(ctx)
}
func (s *Service) SetBroadcastSourceStreamKey(ctx context.Context, actor string, in BroadcastSourceStreamKeyInput) (BroadcastSource, error) {
	if !s.broadcastWriters {
		return BroadcastSource{}, assetclient.ErrUnavailable
	}
	if e := broadcastActor(ctx, actor); e != nil {
		return BroadcastSource{}, e
	}
	hash, suffix, e := HashBroadcastSourceKey(in.StreamKey)
	if e != nil || !ValidOperationKey(in.OperationKey) || in.ExpectedSourceRevision < 1 {
		return BroadcastSource{}, ErrInvalid
	}
	in.StreamKey = ""
	r, e := s.sourceRepository()
	if e != nil {
		return BroadcastSource{}, e
	}
	return r.SetBroadcastSourceKey(ctx, actor, in, hash, suffix, s.now().UTC())
}
func (s *Service) SetBroadcastSourceTarget(ctx context.Context, actor string, in BroadcastSourceTargetInput) (BroadcastSource, error) {
	if !s.broadcastWriters {
		return BroadcastSource{}, assetclient.ErrUnavailable
	}
	if e := broadcastActor(ctx, actor); e != nil {
		return BroadcastSource{}, e
	}
	if !ValidOperationKey(in.OperationKey) || in.ExpectedSourceRevision < 1 || in.ExpectedRevision < 1 || !validLiveUUID(in.RecordingID) {
		return BroadcastSource{}, ErrInvalid
	}
	r, e := s.sourceRepository()
	if e != nil {
		return BroadcastSource{}, e
	}
	return r.SetBroadcastSourceTarget(ctx, actor, in, s.now().UTC())
}
func (s *Service) ResolveBroadcastSource(ctx context.Context, in BroadcastSourceResolveInput) (BroadcastSourceResolved, error) {
	hash, _, e := HashBroadcastSourceKey(in.StreamKey)
	if e != nil {
		return BroadcastSourceResolved{}, ErrBroadcastStreamKey
	}
	r, e := s.sourceRepository()
	if e != nil {
		return BroadcastSourceResolved{}, e
	}
	return r.ResolveBroadcastSource(ctx, hash)
}
func (s *Service) BindBroadcastSource(ctx context.Context, actor string, in BroadcastSourceBindInput, canPublish bool) (BroadcastMutationResult, error) {
	if !s.broadcastWriters {
		return BroadcastMutationResult{}, assetclient.ErrUnavailable
	}
	if e := broadcastActor(ctx, actor); e != nil {
		return BroadcastMutationResult{}, e
	}
	hash, _, e := HashBroadcastSourceKey(in.StreamKey)
	if e != nil {
		return BroadcastMutationResult{}, ErrBroadcastStreamKey
	}
	if in.ProtocolVersion != BroadcastProtocolVersion {
		return BroadcastMutationResult{}, ErrBroadcastProtocol
	}
	if !ValidOperationKey(in.OperationKey) || in.ExpectedSourceRevision < 1 || in.ExpectedRevision < 1 || !validLiveUUID(in.RecordingID) {
		return BroadcastMutationResult{}, ErrInvalid
	}
	in.StreamKey = ""
	r, e := s.sourceRepository()
	if e != nil {
		return BroadcastMutationResult{}, e
	}
	_, c, e := r.PrepareBroadcastSourceBinding(ctx, actor, in, hash, canPublish, s.now().UTC())
	if e != nil {
		return BroadcastMutationResult{}, e
	}
	return s.completeBroadcastBinding(ctx, in.RecordingID, actor, in.BroadcastBindInput, c)
}
