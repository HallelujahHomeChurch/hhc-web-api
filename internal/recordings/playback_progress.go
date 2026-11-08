package recordings

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

// ProgressPosition bounds VOD progress to its trusted duration. Live has no VOD duration.
func ProgressPosition(position float64, duration *float64) (float64, error) {
	if math.IsNaN(position) || math.IsInf(position, 0) || position < 0 {
		return 0, ErrInvalid
	}
	if duration == nil {
		return position, nil
	}
	if position > *duration+1 {
		return 0, ErrInvalid
	}
	return min(position, *duration), nil
}

var (
	ErrProgressConflict   = errors.New("playback progress claim conflict")
	ErrProgressSuperseded = errors.New("playback progress owner superseded")
)

type ProgressSnapshot struct {
	Generation      string    `json:"generation"`
	MediaVersion    string    `json:"mediaVersion"`
	PositionSeconds float64   `json:"positionSeconds"`
	Completed       bool      `json:"completed"`
	Mode            string    `json:"mode"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type ProgressReadResult struct {
	ClaimToken   string            `json:"claimToken"`
	Progress     *ProgressSnapshot `json:"progress"`
	MediaVersion string            `json:"mediaVersion"`
}

type ProgressClaim struct {
	ClaimToken              string  `json:"claimToken"`
	SessionID               string  `json:"sessionId"`
	ClaimID                 string  `json:"claimId"`
	ExpectedGeneration      *string `json:"expectedGeneration"`
	MediaVersion            string  `json:"mediaVersion"`
	StartKind               string  `json:"startKind"`
	ObservedPositionSeconds float64 `json:"observedPositionSeconds"`
}

type ProgressUpdate struct {
	SessionID       string  `json:"sessionId"`
	Generation      string  `json:"generation"`
	Sequence        int64   `json:"sequence"`
	MediaVersion    string  `json:"mediaVersion"`
	PositionSeconds float64 `json:"positionSeconds"`
	State           string  `json:"state"`
}

func (v ProgressClaim) Validate() error {
	if !validLiveUUID(v.SessionID) || !validLiveUUID(v.ClaimID) ||
		v.ExpectedGeneration != nil && !validLiveUUID(*v.ExpectedGeneration) ||
		!validProgressVersion(v.MediaVersion) {
		return ErrInvalid
	}
	switch v.StartKind {
	case "resume", "restart", "at":
		_, err := ProgressPosition(v.ObservedPositionSeconds, nil)
		return err
	default:
		return ErrInvalid
	}
}

func (v ProgressUpdate) Validate() error {
	if !validLiveUUID(v.SessionID) || !validLiveUUID(v.Generation) ||
		v.Sequence < 1 || v.Sequence > 1<<53-1 || !validProgressVersion(v.MediaVersion) {
		return ErrInvalid
	}
	switch v.State {
	case "playing", "paused", "waiting", "ended":
		_, err := ProgressPosition(v.PositionSeconds, nil)
		return err
	default:
		return ErrInvalid
	}
}

func validProgressVersion(value string) bool {
	mode, media, ok := strings.Cut(value, ":")
	id, revision, archived := strings.Cut(media, ":")
	if !ok || !packageIDPattern.MatchString(id) {
		return false
	}
	if !archived {
		return mode == "vod" || mode == "live"
	}
	rangeRevision, err := strconv.ParseInt(revision, 10, 64)
	return mode == "vod" && err == nil && rangeRevision >= 0 && strconv.FormatInt(rangeRevision, 10) == revision
}

// A published live capture can change its public range without replacing its package.
func (b Broadcast) VODProgressVersion(packageID string) string {
	return "vod:" + packageID + ":" + strconv.FormatInt(b.RangeApplied, 10)
}

type ProgressOwner struct {
	Generation      string  `json:"generation"`
	MediaVersion    string  `json:"mediaVersion"`
	PositionSeconds float64 `json:"positionSeconds"`
	Sequence        int64   `json:"sequence"`
}

type ProgressWriteResult struct {
	Accepted bool  `json:"accepted"`
	Sequence int64 `json:"sequence"`
}

type ResumeItem struct {
	SessionID       string    `json:"sessionId"`
	RecordingID     string    `json:"recordingId"`
	PublicID        string    `json:"publicId"`
	Title           string    `json:"title"`
	Mode            string    `json:"mode"`
	CaptureID       string    `json:"captureId,omitempty"`
	MediaVersion    string    `json:"mediaVersion"`
	PositionSeconds float64   `json:"positionSeconds"`
	DurationSeconds *float64  `json:"durationSeconds,omitempty"`
	Generation      string    `json:"generation"`
	ClaimedAt       time.Time `json:"claimedAt"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

type playbackProgressRepository interface {
	GetContinueWatching(context.Context, string, time.Time) (*ResumeItem, error)
	GetPlaybackProgress(context.Context, string, string, time.Time) (ProgressReadResult, error)
	ClaimPlaybackProgress(context.Context, string, string, ProgressClaim, float64, time.Time) (ProgressOwner, error)
	UpdatePlaybackProgress(context.Context, string, string, ProgressUpdate, time.Time) (ProgressWriteResult, error)
}

func (s *Service) GetPlaybackProgress(ctx context.Context, id, user string) (ProgressReadResult, error) {
	if !validLiveUUID(id) || !validLiveUUID(user) {
		return ProgressReadResult{}, ErrInvalid
	}
	repo, ok := s.repo.(playbackProgressRepository)
	if !ok {
		return ProgressReadResult{}, assetclient.ErrUnavailable
	}
	if len(s.watermarkKey) < 32 {
		return ProgressReadResult{}, assetclient.ErrUnavailable
	}
	result, err := repo.GetPlaybackProgress(ctx, id, user, s.now())
	if err != nil {
		return result, err
	}
	var generation *string
	if result.Progress != nil {
		generation = &result.Progress.Generation
	}
	result.ClaimToken = s.progressIntent(id, user, result.MediaVersion, generation, s.now().Add(2*time.Minute).Unix())
	return result, nil
}

func (s *Service) progressIntent(id, user, version string, generation *string, expires int64) string {
	g := ""
	if generation != nil {
		g = *generation
	}
	mac := hmac.New(sha256.New, s.watermarkKey)
	fmt.Fprintf(mac, "recording-progress-intent-v1\x00%s\x00%s\x00%s\x00%s\x00%d", id, user, version, g, expires)
	return strconv.FormatInt(expires, 10) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func (s *Service) validProgressIntent(id, user, version string, generation *string, token string) bool {
	if len(s.watermarkKey) < 32 || len(token) > 80 {
		return false
	}
	expiry, _, ok := strings.Cut(token, ".")
	expires, err := strconv.ParseInt(expiry, 10, 64)
	return ok && err == nil && s.now().Unix() < expires &&
		hmac.Equal([]byte(token), []byte(s.progressIntent(id, user, version, generation, expires)))
}

func (s *Service) ClaimPlaybackProgress(ctx context.Context, id, user string, input ProgressClaim) (ProgressOwner, error) {
	if err := input.Validate(); err != nil {
		return ProgressOwner{}, err
	}
	repo, ok := s.repo.(playbackProgressRepository)
	if !ok {
		return ProgressOwner{}, assetclient.ErrUnavailable
	}
	if !validLiveUUID(id) || !validLiveUUID(user) {
		return ProgressOwner{}, ErrInvalid
	}
	if !s.validProgressIntent(id, user, input.MediaVersion, input.ExpectedGeneration, input.ClaimToken) {
		return ProgressOwner{}, ErrProgressConflict
	}
	watch, err := s.ResolveRecordingWatch(ctx, id, user)
	if err != nil {
		return ProgressOwner{}, err
	}
	var version string
	var duration float64
	switch {
	case watch.View == "vod" && watch.Recording != nil:
		version = watch.progressVersion
		duration = watch.Recording.DurationSeconds
	case watch.View == "live" && watch.Live != nil:
		version = "live:" + watch.Live.CaptureID
	default:
		return ProgressOwner{}, ErrNotFound
	}
	if version != input.MediaVersion {
		return ProgressOwner{}, ErrProgressConflict
	}
	return repo.ClaimPlaybackProgress(ctx, id, user, input, duration, s.now())
}

func (s *Service) UpdatePlaybackProgress(ctx context.Context, id, user string, input ProgressUpdate) (ProgressWriteResult, error) {
	if !validLiveUUID(id) || !validLiveUUID(user) {
		return ProgressWriteResult{}, ErrInvalid
	}
	repo, ok := s.repo.(playbackProgressRepository)
	if !ok {
		return ProgressWriteResult{}, assetclient.ErrUnavailable
	}
	return repo.UpdatePlaybackProgress(ctx, id, user, input, s.now())
}

func (s *Service) GetContinueWatching(ctx context.Context, user string) (*ResumeItem, error) {
	if !validLiveUUID(user) {
		return nil, ErrInvalid
	}
	repo, ok := s.repo.(playbackProgressRepository)
	if !ok {
		return nil, assetclient.ErrUnavailable
	}
	return repo.GetContinueWatching(ctx, user, s.now())
}
