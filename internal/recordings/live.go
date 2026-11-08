package recordings

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/google/uuid"
)

// The server-accepted capture start anchors the verified media timeline. Advancing
// a backlog does not imply a live stream until that timeline catches up.
func (c *Capture) DeriveLiveState(now time.Time) {
	if c.Terminal() {
		c.Status.LiveState = c.Status.State
		return
	}
	if c.Status.LiveState == "aborted" {
		return
	}
	p := c.Status.Progress
	if c.MediaExpiresAt != nil && !now.Before(*c.MediaExpiresAt) || !now.Before(c.Status.ExpiresAt) || c.Status.ReplayUntil != nil && !now.Before(*c.Status.ReplayUntil) {
		c.Status.LiveState = "expired"
		c.Status.LiveEnabled = false
		return
	}
	if c.Status.StopAcceptedAt != nil {
		c.Status.LiveState = "ending"
		if c.NormalEnd && p.Ended && p.EndedAt != nil {
			c.Status.LiveState = "ended"
		}
		return
	}
	advanced := c.Status.CreatedAt
	if p.LastAdvancedAt != nil {
		advanced = *p.LastAdvancedAt
	}
	switch {
	case !now.Before(advanced.Add(90 * time.Second)):
		c.Status.LiveState = "interrupted"
	case p.LastSequence < 2:
		c.Status.LiveState = "starting"
	// Match the player's 90-second verified buffer and three-minute live latency target.
	case c.Status.CreatedAt.IsZero() || now.Sub(c.Status.CreatedAt).Seconds()-p.MediaEndSeconds+90 > 180:
		c.Status.LiveState = "recovering"
	default:
		c.Status.LiveState = "live"
	}
}
func (c Capture) LiveAvailable(now time.Time) bool {
	return (c.MediaExpiresAt == nil || now.Before(*c.MediaExpiresAt)) && c.Status.CaptureID != "" && !c.Terminal() && c.Status.LiveEnabled && now.Before(c.Status.ExpiresAt) && (c.Status.ReplayUntil == nil || now.Before(*c.Status.ReplayUntil))
}

type AdminCaptureSummary struct {
	CaptureID   string `json:"captureId"`
	LiveEnabled bool   `json:"liveEnabled"`
	LiveState   string `json:"liveState"`
	AutoPublish string `json:"autoPublish"`
}
type LiveRecording struct {
	CoverMode      string                            `json:"coverMode,omitempty"`
	PublicID       string                            `json:"publicId,omitempty"`
	StartedAt      *time.Time                        `json:"startedAt,omitempty"`
	ID             string                            `json:"id"`
	CaptureID      string                            `json:"captureId"`
	Title          string                            `json:"title"`
	Description    string                            `json:"description,omitempty"`
	LiveState      string                            `json:"liveState"`
	CreatedAt      time.Time                         `json:"createdAt"`
	StopAcceptedAt *time.Time                        `json:"stopAcceptedAt"`
	Progress       assetclient.RecordingLiveProgress `json:"progress"`
	CoverRevision  string                            `json:"coverRevision,omitempty"`
}
type LivePlayback struct {
	RecordingID        string                            `json:"recordingId"`
	CaptureID          string                            `json:"captureId"`
	PlaybackScopeID    string                            `json:"playbackScopeId"`
	MediaURL           string                            `json:"mediaUrl"`
	ExchangeCredential string                            `json:"exchangeCredential"`
	IssuedAt           time.Time                         `json:"issuedAt"`
	ExpiresAt          time.Time                         `json:"expiresAt"`
	ServerNow          time.Time                         `json:"serverNow"`
	CaptureExpiresAt   time.Time                         `json:"captureExpiresAt"`
	StopAcceptedAt     *time.Time                        `json:"stopAcceptedAt"`
	ReplayUntil        *time.Time                        `json:"replayUntil"`
	TerminalReason     *string                           `json:"terminalReason"`
	LiveState          string                            `json:"liveState"`
	Progress           assetclient.RecordingLiveProgress `json:"progress"`
	WatermarkCode      string                            `json:"watermarkCode"`
}
type LiveRepository interface {
	ListLiveCaptures(context.Context, time.Time) ([]Capture, error)
	LatestRecordingCapture(context.Context, string) (Capture, error)
	GetLiveCapture(context.Context, string, string) (Capture, error)
	InspectCaptureScope(context.Context, string, string, string, string, time.Time, bool) (Capture, error)
	WithCaptureScope(context.Context, string, string, string, string, time.Time, bool, func(Capture, string) error) error
}

func (s *Service) liveRepository() (LiveRepository, error) {
	r, ok := s.repo.(LiveRepository)
	if !ok {
		return nil, assetclient.ErrUnavailable
	}
	return r, nil
}
func (s *Service) AdminCaptureSummary(ctx context.Context, id string) (*AdminCaptureSummary, error) {
	repo, ok := s.repo.(LiveRepository)
	if !ok {
		return nil, nil
	}
	c, err := repo.LatestRecordingCapture(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c, err = s.refreshLiveCapture(ctx, c)
	if err != nil {
		return nil, err
	}
	if c.Status.CaptureID == "" {
		return nil, nil
	}
	autoPublish := c.Status.AutoPublish
	if c.BroadcastEpoch > 0 && autoPublish == "cancelled" && !c.Terminal() {
		br, err := s.broadcastRepository()
		if err != nil {
			return nil, err
		}
		b, err := br.GetBroadcast(ctx, id)
		if err != nil {
			return nil, err
		}
		if b.Epoch != c.BroadcastEpoch || b.View.Binding == nil || b.View.Binding.CaptureID != c.Status.CaptureID || b.View.Binding.Epoch != c.BroadcastEpoch {
			return nil, ErrConflict
		}
		// B1 defers C1 activation until the public archive is ready.
		if b.View.Policy.AutoPublish && !b.Revoked && b.View.Phase != "failed" && b.View.Phase != "cancelled" && !b.AutoPublicationActivated {
			autoPublish = "pending"
			for _, op := range c.Operations {
				if op.Operation == "cancel_auto_publish" {
					autoPublish = "cancelled"
					break
				}
			}
		}
	}
	return &AdminCaptureSummary{c.Status.CaptureID, c.Status.LiveEnabled, c.Status.LiveState, autoPublish}, nil
}
func (s *Service) refreshLiveCapture(ctx context.Context, c Capture) (Capture, error) {
	var err error
	if !c.LiveAvailable(s.now()) {
		repo, e := s.captureRepository()
		if e != nil {
			return c, e
		}
		// Expiry/seal recovery needs the complete durable journal.
		c, err = repo.GetCapture(ctx, c.IntentID, c.RecordingID)
		if err != nil {
			return c, err
		}
	}
	c, err = s.expireCapture(ctx, c)
	if err != nil {
		return c, err
	}
	repo, _ := s.captureRepository()
	if c.LiveAvailable(s.now()) {
		value, err := s.readLiveCaptureStatus(ctx, c)
		if err != nil {
			return c, err
		}
		if value.State == "failed" || value.State == "expired" || value.State == "aborted" {
			c, err = repo.UpdateCapture(ctx, c.IntentID, func(current *Capture) error { return mergeCaptureStatus(current, value, s.now().UTC()) })
		} else {
			// Viewers observe progress; producer/reconciliation owns its persistence.
			err = mergeCaptureStatus(&c, value, s.now().UTC())
		}
		if err != nil {
			return c, err
		}
	}
	return c, nil
}

func (s *Service) readLiveCaptureStatus(ctx context.Context, c Capture) (assetclient.RecordingCaptureStatus, error) {
	var empty assetclient.RecordingCaptureStatus
	if s.packages == nil {
		return empty, assetclient.ErrUnavailable
	}
	ownerCtx := auditclient.WithPrincipalProvenance(ctx, c.ActorType, c.ActorID, c.RequestID)
	value, err := s.packages.GetRecordingCapture(ownerCtx, c.Status.CaptureID, "", 1)
	if err != nil {
		return empty, err
	}
	// This endpoint exposes only the producer's published common waterline.
	progress, err := s.packages.GetRecordingLiveProgress(ownerCtx, c.Status.CaptureID)
	if err != nil {
		return empty, err
	}
	if progress.Revision < value.Progress.Revision {
		return empty, ErrConflict
	}
	value.Progress = progress
	return value, nil
}
func (s *Service) ListLive(ctx context.Context, query string) ([]LiveRecording, error) {
	query, err := NormalizeSearch(query)
	if err != nil {
		return nil, err
	}
	repo, err := s.liveRepository()
	if err != nil {
		return nil, err
	}
	captures, err := repo.ListLiveCaptures(ctx, s.now().UTC())
	if err != nil {
		return nil, err
	}
	result := []LiveRecording{}
	for _, c := range captures {
		c, err = s.refreshLiveCapture(ctx, c)
		if err != nil {
			return nil, err
		}
		// Ended/ending are only reachable by an already registered viewer scope.
		if !c.LiveAvailable(s.now()) || c.Status.StopAcceptedAt != nil {
			continue
		}
		value, err := s.repo.Get(ctx, c.RecordingID)
		if err != nil {
			return nil, err
		}
		if value.Expired(s.now()) || !searchMatches(query, value.Title, value.Description) {
			continue
		}
		cover := c.LiveCover
		if c.BroadcastEpoch > 0 {
			// Capture readiness can precede the readable broadcast snapshot.
			broadcasts, err := s.broadcastRepository()
			if err != nil {
				return nil, err
			}
			b, err := broadcasts.GetBroadcast(ctx, c.RecordingID)
			if err != nil {
				return nil, err
			}
			cover = &b.CoverSnapshot
		}
		result = append(result, LiveRecording{PublicID: value.PublicID, ID: value.ID, StartedAt: value.BroadcastStartedAt, CaptureID: c.Status.CaptureID, Title: value.Title, Description: value.Description, LiveState: c.Status.LiveState, CreatedAt: c.Status.CreatedAt, StopAcceptedAt: c.Status.StopAcceptedAt, Progress: c.Status.Progress, CoverRevision: liveCoverRevision(cover), CoverMode: liveCoverMode(cover)})
	}
	return result, nil
}
func validLiveUUID(value string) bool {
	parsed, err := uuid.Parse(value)
	return err == nil && len(value) == 36 && parsed.String() == value
}
func (s *Service) LivePlayback(ctx context.Context, id, capture, scope, user string) (LivePlayback, error) {
	if s.mediaBaseURL == "" || len(s.watermarkKey) < 32 || !validLiveUUID(id) || !validLiveUUID(scope) || !validLiveUUID(user) || !packageIDPattern.MatchString(capture) {
		return LivePlayback{}, ErrInvalid
	}
	repo, err := s.liveRepository()
	if err != nil {
		return LivePlayback{}, err
	}
	captures, _ := s.captureRepository()
	c, err := repo.GetLiveCapture(ctx, capture, id)
	if err != nil {
		logLivePlaybackFailure(ctx, "capture_lookup", err)
		return LivePlayback{}, err
	}
	var snapshot *assetclient.RecordingCaptureStatus
	if c.LiveAvailable(s.now()) {
		value, readErr := s.readLiveCaptureStatus(ctx, c)
		err = readErr
		snapshot = &value
		if err == nil && (value.State == "failed" || value.State == "expired" || value.State == "aborted") {
			_, err = captures.UpdateCapture(ctx, c.IntentID, func(current *Capture) error { return mergeCaptureStatus(current, value, s.now().UTC()) })
		}
	} else {
		// Preserve expiry/seal recovery; active viewers do not rewrite the aggregate.
		_, err = s.refreshLiveCapture(ctx, c)
	}
	if err != nil {
		logLivePlaybackFailure(ctx, "capture_refresh", err)
		return LivePlayback{}, err
	}
	prepare := func(current Capture) (Capture, time.Time, error) {
		now := s.now().UTC()
		if snapshot != nil {
			if err := mergeCaptureStatus(&current, *snapshot, now); err != nil {
				return current, time.Time{}, err
			}
		}
		current.DeriveLiveState(now)
		if !current.LiveAvailable(now) {
			return current, time.Time{}, ErrNotFound
		}
		if current.Status.Progress.LastSequence < 2 {
			return current, time.Time{}, ErrConflict
		}
		deadline := now.Add(5 * time.Minute)
		for _, until := range []*time.Time{&current.Status.ExpiresAt, current.MediaExpiresAt, current.Status.ReplayUntil} {
			if until != nil && until.Before(deadline) {
				deadline = *until
			}
		}
		return current, deadline, nil
	}
	current, err := repo.InspectCaptureScope(ctx, id, capture, user, scope, s.now().UTC(), s.watermarkTraceEnabled)
	if err != nil {
		logLivePlaybackFailure(ctx, "scope_inspection", err)
		return LivePlayback{}, err
	}
	_, deadline, err := prepare(current)
	if err != nil {
		return LivePlayback{}, err
	}
	grant, err := s.packages.IssueRecordingLiveGrant(ctx, capture, id, user, scope, deadline)
	if err != nil {
		logLivePlaybackFailure(ctx, "issue_grant", err)
		return LivePlayback{}, err
	}

	var result LivePlayback
	stage := "scope_transaction"
	err = repo.WithCaptureScope(ctx, id, capture, user, scope, s.now().UTC(), s.watermarkTraceEnabled, func(current Capture, code string) error {
		current, currentDeadline, err := prepare(current)
		if err != nil {
			return err
		}
		stage = "validate_grant"
		now := s.now().UTC()
		if grant.ExchangeCredential == "" || !grant.ExpiresAt.After(now) || grant.ExpiresAt.After(deadline) || grant.ExpiresAt.After(currentDeadline) || len(grant.ExchangeCredential) > 8192 {
			return ErrConflict
		}
		if code == "" {
			mac := hmac.New(sha256.New, s.watermarkKey)
			mac.Write([]byte(user + ":" + id))
			code = strings.ToUpper(hex.EncodeToString(mac.Sum(nil)[:4]))
		}
		path := "/videos/" + url.PathEscape(id) + "/captures/" + capture + "/sessions/" + scope + "/master.m3u8"
		result = LivePlayback{id, capture, scope, s.mediaBaseURL + path, grant.ExchangeCredential, now, grant.ExpiresAt.UTC(), now, current.Status.ExpiresAt, current.Status.StopAcceptedAt, current.Status.ReplayUntil, current.Status.TerminalReason, current.Status.LiveState, current.Status.Progress, code}
		stage = "scope_commit"
		return nil
	})
	if err != nil {
		logLivePlaybackFailure(ctx, stage, err)
		return LivePlayback{}, err
	}
	return result, nil
}
func validLiveProgress(p assetclient.RecordingLiveProgress, now time.Time) bool {
	return p.Revision >= 0 && p.FirstSequence == 0 && p.LastSequence >= -1 && p.MediaEndSeconds >= 0 && !math.IsNaN(p.MediaEndSeconds) && !math.IsInf(p.MediaEndSeconds, 0) && p.MediaEndSeconds <= 24*60*60 && (p.LastAdvancedAt == nil || !p.LastAdvancedAt.After(now)) && (p.EndedAt == nil || !p.EndedAt.After(now))
}

func liveCoverRevision(cover *CaptureLiveCover) string {
	if cover != nil && cover.Settings.CoverState == "ready" {
		return cover.Settings.CoverID
	}
	return ""
}

func liveCoverMode(cover *CaptureLiveCover) string {
	if cover != nil {
		switch cover.Settings.Selection.Mode {
		case "auto", "custom":
			return cover.Settings.Selection.Mode
		}
	}
	return ""
}
