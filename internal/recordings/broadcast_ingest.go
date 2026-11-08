package recordings

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
)

const BroadcastIngestProtocolVersion = "k3-2026-10-14.rc1"
const ingestSessionLifetime = 15 * time.Minute

var ErrBroadcastIngestSession = errors.New("broadcast ingest session invalid or expired")
var ErrBroadcastIngestRevoked = errors.New("broadcast ingest revoked; cleanup only")

type BroadcastIngestRepository interface {
	BroadcastIngestCredentials(context.Context, string) (string, string, error)
	BroadcastIngestCapture(context.Context, string, string) (Capture, error)
	BroadcastIngestOwner(context.Context, string, string, string) (string, error)
}

type BroadcastIngestSession struct {
	Token       string    `json:"token"`
	ExpiresAt   time.Time `json:"expiresAt"`
	RecordingID string    `json:"recordingId"`
	CaptureID   string    `json:"captureId"`
	Epoch       int64     `json:"epoch"`
}
type BroadcastIngestBinding struct {
	BroadcastMutationResult
	Session BroadcastIngestSession `json:"session"`
}
type BroadcastIngestRenewInput struct {
	StreamKey    string `json:"streamKey"`
	SessionToken string `json:"sessionToken"`
}

// The signed owner is the Console preparation authorizer, not an OBS login.
// Tokens never enter durable operation inputs, and do not authorize Admin APIs.
type ingestClaims struct {
	Version     string `json:"v"`
	RecordingID string `json:"r"`
	CaptureID   string `json:"c"`
	ActorID     string `json:"a"`
	Epoch       int64  `json:"e"`
	Expires     int64  `json:"x"`
}

func (s *Service) ingestRepository() (BroadcastIngestRepository, error) {
	r, ok := s.repo.(BroadcastIngestRepository)
	if !ok {
		return nil, assetclient.ErrUnavailable
	}
	return r, nil
}
func (s *Service) ingestAuthorizer(ctx context.Context, actor string) error {
	if s.publicationAuthorizer == nil {
		return assetclient.ErrUnavailable
	}
	allowed, e := s.publicationAuthorizer.VerifyRecordingPublication(ctx, actor)
	if e != nil {
		return assetclient.ErrUnavailable
	}
	if !allowed {
		return ErrForbidden
	}
	return nil
}
func ingestSigningKey(hash, secret string) string {
	sum := sha256.Sum256([]byte(secret + "\x00" + hash))
	return hex.EncodeToString(sum[:])
}
func ingestMAC(hash, payload string) ([]byte, error) {
	key, e := hex.DecodeString(hash)
	if e != nil || len(key) != 32 {
		return nil, ErrBroadcastIngestSession
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("hhc-broadcast-ingest-v1\x00" + payload))
	return mac.Sum(nil), nil
}
func (s *Service) issueIngestSession(hash string, c ingestClaims, capture Capture) (BroadcastIngestSession, error) {
	expiry := s.now().UTC().Add(ingestSessionLifetime)
	if capture.Status.ExpiresAt.Before(expiry) {
		expiry = capture.Status.ExpiresAt
	}
	if !expiry.After(s.now()) {
		return BroadcastIngestSession{}, ErrBroadcastIngestSession
	}
	c.Version, c.Expires = BroadcastIngestProtocolVersion, expiry.Unix()
	raw, e := json.Marshal(c)
	if e != nil {
		return BroadcastIngestSession{}, e
	}
	payload := base64.RawURLEncoding.EncodeToString(raw)
	sig, e := ingestMAC(hash, payload)
	if e != nil {
		return BroadcastIngestSession{}, e
	}
	return BroadcastIngestSession{"hhc_ingest_" + payload + "." + base64.RawURLEncoding.EncodeToString(sig), time.Unix(c.Expires, 0).UTC(), c.RecordingID, c.CaptureID, c.Epoch}, nil
}
func (s *Service) checkIngestSession(ctx context.Context, token string, allowExpired bool) (ingestClaims, Capture, string, error) {
	var c ingestClaims
	var empty Capture
	if len(token) > 2048 || !strings.HasPrefix(token, "hhc_ingest_") {
		return c, empty, "", ErrBroadcastIngestSession
	}
	parts := strings.Split(strings.TrimPrefix(token, "hhc_ingest_"), ".")
	if len(parts) != 2 {
		return c, empty, "", ErrBroadcastIngestSession
	}
	raw, e := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	if e != nil {
		return c, empty, "", ErrBroadcastIngestSession
	}
	sig, e := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	if e != nil {
		return c, empty, "", ErrBroadcastIngestSession
	}
	r, e := s.ingestRepository()
	if e != nil {
		return c, empty, "", e
	}
	// Only use the bounded, syntactically valid recording ID for key lookup.
	// The claims remain untrusted until the source-specific MAC is verified.
	if json.Unmarshal(raw, &c) != nil || !validLiveUUID(c.RecordingID) {
		return c, empty, "", ErrBroadcastIngestSession
	}
	hash, secret, e := r.BroadcastIngestCredentials(ctx, c.RecordingID)
	if e != nil {
		return c, empty, "", e
	}
	want, e := ingestMAC(ingestSigningKey(hash, secret), parts[0])
	if e != nil || !hmac.Equal(sig, want) || json.Unmarshal(raw, &c) != nil || c.Version != BroadcastIngestProtocolVersion || !validLiveUUID(c.RecordingID) || !packageIDPattern.MatchString(c.CaptureID) || !auditclient.ValidActorRequest(c.ActorID, "ingest") || c.Epoch < 1 || c.Expires <= 0 || !allowExpired && c.Expires <= s.now().Unix() {
		return c, empty, "", ErrBroadcastIngestSession
	}
	br, e := s.broadcastRepository()
	if e != nil {
		return c, empty, "", e
	}
	b, e := br.GetBroadcast(ctx, c.RecordingID)
	if errors.Is(e, ErrNotFound) {
		return c, empty, "", ErrBroadcastIngestSession
	}
	if e != nil {
		return c, empty, "", e
	}
	if !b.SourceManaged || b.View.Binding == nil || b.View.Binding.ActorID != c.ActorID || b.View.Binding.CaptureID != c.CaptureID || b.View.Binding.Epoch != c.Epoch || b.Epoch != c.Epoch {
		return c, empty, "", ErrBroadcastIngestSession
	}
	capture, e := r.BroadcastIngestCapture(ctx, c.CaptureID, c.RecordingID)
	if errors.Is(e, ErrNotFound) {
		return c, empty, "", ErrBroadcastIngestSession
	}
	if e != nil {
		return c, empty, "", e
	}
	if capture.ActorID != c.ActorID || capture.ActorType != "user" || capture.BroadcastEpoch != c.Epoch || capture.IntentID != b.CaptureIntentID || capture.Status.State == "expired" || !s.now().Before(capture.Status.ExpiresAt) {
		return c, empty, "", ErrBroadcastIngestSession
	}
	if b.Revoked {
		return c, capture, hash, ErrBroadcastIngestRevoked
	}
	return c, capture, hash, nil
}
func (s *Service) BindBroadcastIngest(ctx context.Context, requestID string, in BroadcastSourceBindInput) (BroadcastIngestBinding, error) {
	var out BroadcastIngestBinding
	hash, _, e := HashBroadcastSourceKey(in.StreamKey)
	if e != nil {
		return out, ErrBroadcastStreamKey
	}
	if !validLiveUUID(in.RecordingID) || !ValidOperationKey(in.OperationKey) {
		return out, ErrInvalid
	}
	r, e := s.ingestRepository()
	if e != nil {
		return out, e
	}
	owner, e := r.BroadcastIngestOwner(ctx, hash, in.RecordingID, in.OperationKey)
	if e != nil {
		return out, e
	}
	if !auditclient.ValidActorRequest(owner, requestID) {
		return out, ErrForbidden
	}
	if e = s.ingestAuthorizer(ctx, owner); e != nil {
		return out, e
	}
	ctx = auditclient.WithProvenance(ctx, owner, requestID)
	out.BroadcastMutationResult, e = s.BindBroadcastSource(ctx, owner, in, true)
	if e != nil {
		return out, e
	}
	binding := out.Broadcast.Binding
	if binding == nil {
		return out, assetclient.ErrUnavailable
	}
	cr, e := s.captureRepository()
	if e != nil {
		return out, e
	}
	capture, e := cr.GetCapture(ctx, binding.CaptureID, in.RecordingID)
	if e != nil {
		return out, e
	}
	current, secret, e := r.BroadcastIngestCredentials(ctx, in.RecordingID)
	if e != nil {
		return out, e
	}
	if !hmac.Equal([]byte(hash), []byte(current)) {
		return out, ErrBroadcastStreamKey
	}
	out.Session, e = s.issueIngestSession(ingestSigningKey(current, secret), ingestClaims{RecordingID: in.RecordingID, CaptureID: binding.CaptureID, ActorID: owner, Epoch: binding.Epoch}, capture)
	return out, e
}
func (s *Service) RenewBroadcastIngest(ctx context.Context, in BroadcastIngestRenewInput) (BroadcastIngestSession, error) {
	hash, _, e := HashBroadcastSourceKey(in.StreamKey)
	if e != nil {
		return BroadcastIngestSession{}, ErrBroadcastStreamKey
	}
	c, capture, current, e := s.checkIngestSession(ctx, in.SessionToken, true)
	if e != nil && !errors.Is(e, ErrBroadcastIngestRevoked) {
		return BroadcastIngestSession{}, e
	}
	if !hmac.Equal([]byte(hash), []byte(current)) {
		return BroadcastIngestSession{}, ErrBroadcastStreamKey
	}
	// Permanent Console revocation permits only status/abort, including renewal
	// to release the reserved input after an offline session or authorizer removal.
	if !errors.Is(e, ErrBroadcastIngestRevoked) {
		if e = s.ingestAuthorizer(ctx, c.ActorID); e != nil {
			return BroadcastIngestSession{}, e
		}
	}
	r, e := s.ingestRepository()
	if e != nil {
		return BroadcastIngestSession{}, e
	}
	latest, secret, e := r.BroadcastIngestCredentials(ctx, c.RecordingID)
	if e != nil {
		return BroadcastIngestSession{}, e
	}
	if !hmac.Equal([]byte(hash), []byte(latest)) {
		return BroadcastIngestSession{}, ErrBroadcastStreamKey
	}
	return s.issueIngestSession(ingestSigningKey(latest, secret), c, capture)
}
func (s *Service) AuthorizeBroadcastIngest(ctx context.Context, token, id, capture string, cleanup bool) (string, int64, error) {
	c, _, _, e := s.checkIngestSession(ctx, token, false)
	if e != nil && !(cleanup && errors.Is(e, ErrBroadcastIngestRevoked)) {
		return "", 0, e
	}
	if c.RecordingID != id || capture != "" && c.CaptureID != capture {
		return "", 0, ErrBroadcastIngestSession
	}
	return c.ActorID, c.Epoch, nil
}
