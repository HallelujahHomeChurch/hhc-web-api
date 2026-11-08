package recordings

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/accountclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/diagnostics"
)

var scopePattern = regexp.MustCompile(`^[a-zA-Z0-9-]{1,80}$`)

type Repository interface {
	EnsureRecordingWatermark(context.Context, string, string, string, time.Time) (WatermarkReceipt, error)
	LookupRecordingWatermark(context.Context, string, string, string, time.Time) (WatermarkReceipt, error)
	Delete(context.Context, string, int64, string, time.Time, bool) error
	ListPendingDeletions(context.Context) ([]string, error)
	MarkDeletionAttempt(context.Context, string) error
	MarkDeletionDelivered(context.Context, string) error
	AddSource(context.Context, string, string, time.Time) error
	RecordSourceReady(context.Context, string, string, string, time.Time) (Recording, error)
	AddPackage(context.Context, string, string, time.Time) error
	RecordPackageReady(context.Context, string, string, time.Time) (Recording, error)
	Create(context.Context, string, string, string, time.Time) (Recording, error)
	Get(context.Context, string) (Recording, error)
	ListVisible(context.Context, time.Time) ([]Recording, error)
	ListVisiblePage(context.Context, time.Time, *time.Time, string, int, string) ([]Recording, error)
	ListAdmin(context.Context, string) ([]Recording, error)
	ListPendingCompletions(context.Context) ([]Recording, error)
	UpdateTitle(context.Context, string, int64, string, string, time.Time, bool, *string) (Recording, error)
	SetExposure(context.Context, string, int64, bool, bool, string, time.Time) (Recording, error)
	Publish(context.Context, string, int64, string, time.Time) (Recording, error)
	GetPublishOperation(context.Context, string, int64, string, string, time.Time) (PublishResult, error)
	PublishOperation(context.Context, string, int64, string, string, time.Time) (PublishResult, error)
	Unpublish(context.Context, string, int64, string, time.Time) (Recording, error)
}
type Service struct {
	streamKeyCipher       *StreamKeyCipher
	broadcastWriters      bool
	studioWriters         bool
	videoInteractions     bool
	videoAnalytics        bool
	publicationAuthorizer *accountclient.Client
	repo                  Repository
	packages              *assetclient.Client
	mediaBaseURL          string
	watermarkKey          []byte
	watermarkTraceEnabled bool
	now                   func() time.Time
}

func (s *Service) AdminList(ctx context.Context, origin string) ([]Recording, error) {
	if origin != "" && origin != "upload" && origin != "live" {
		return nil, ErrInvalid
	}
	if err := s.syncLifecycle(ctx); err != nil {
		return nil, err
	}
	return s.repo.ListAdmin(ctx, origin)
}
func (s *Service) NotificationAvailable(ctx context.Context, id string) (bool, error) {
	value, err := s.repo.Get(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if repo, ok := s.repo.(LifecycleRepository); ok && value.PackageID != "" && !value.MediaTerminal {
		bindings := []assetclient.RecordingLifecycleBinding{{RecordingID: value.ID, PackageID: value.PackageID}}
		if _, err := s.syncLifecycleBindings(ctx, repo, bindings, nil); err != nil {
			return false, err
		}
		value, err = s.repo.Get(ctx, id)
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
	}
	if value.Broadcast {
		repo, err := s.broadcastRepository()
		if err != nil {
			return false, err
		}
		b, err := repo.GetBroadcast(ctx, id)
		if err != nil {
			return false, err
		}
		if b.CaptureIntentID == "" || b.View.Phase != "live" || value.Expired(s.now()) {
			return false, nil
		}
		live, err := s.liveRepository()
		if err != nil {
			return false, err
		}
		c, err := live.GetLiveCapture(ctx, b.CaptureIntentID, id)
		if err != nil {
			return false, err
		}
		c, err = s.refreshLiveCapture(ctx, c)
		if err != nil {
			return false, err
		}
		if !b.Revoked && b.View.Phase == "live" && (b.RangePending || c.Status.LiveState == "interrupted") {
			return false, assetclient.ErrUnavailable
		}
		return b.LiveNotificationReady(c, s.now()), nil
	}
	if !value.Visible(s.now()) {
		return false, nil
	}
	_, err = s.readyPackage(ctx, value)
	return err == nil, err
}
func (s *Service) Get(ctx context.Context, id string) (Recording, error) {
	bound, err := s.repo.Get(ctx, id)
	if err != nil {
		return Recording{}, err
	}
	var sources []assetclient.RecordingSourceLifecycleBinding
	if bound.SourceID != "" && bound.PackageID == "" && !bound.MediaTerminal {
		sources = []assetclient.RecordingSourceLifecycleBinding{{RecordingID: bound.ID, SourceID: bound.SourceID}}
	}
	snapshot, err := s.syncLifecycleSnapshot(ctx, sources)
	if err != nil {
		return Recording{}, err
	}
	value, err := s.repo.Get(ctx, id)
	if err != nil {
		return Recording{}, err
	}
	value, err = s.syncCompletion(ctx, value)
	if err != nil {
		return Recording{}, err
	}
	if value.ReadyAt == nil && !value.MediaTerminal {
		for _, p := range snapshot.Items {
			if p.PackageID == value.PackageID && p.RecordingID == value.ID && (p.State == "freezing" || p.State == "validating") {
				value.ProcessingProgress = p.ProcessingProgress
			}
		}
		for _, source := range snapshot.SourceItems {
			if source.SourceID == value.SourceID && source.RecordingID == value.ID && (source.State == "finalizing" || source.State == "queued" || source.State == "processing") {
				value.ProcessingProgress = source.ProcessingProgress
			}
		}
	}
	return value, nil
}

func (s *Service) syncCompletion(ctx context.Context, value Recording) (Recording, error) {
	if value.SourceID != "" && value.PackageID == "" {
		return s.syncSourceReady(ctx, value)
	}
	if value.PackageID != "" {
		if value.ReadyAt != nil {
			return value, nil
		}
		_, err := s.readyPackage(ctx, value)
		if errors.Is(err, assetclient.ErrForbidden) || errors.Is(err, assetclient.ErrNotFound) || errors.Is(err, ErrConflict) {
			return value, nil
		}
		if err != nil {
			return Recording{}, err
		}
		return s.repo.Get(ctx, value.ID)
	}
	return value, nil
}

func (s *Service) ReconcileCompletions(ctx context.Context) error {
	values, err := s.repo.ListPendingCompletions(ctx)
	if err != nil {
		return err
	}
	var failures error
	for _, value := range values {
		if _, err := s.syncCompletion(ctx, value); err != nil {
			failures = errors.Join(failures, err)
		}
	}
	return failures
}

func (s *Service) UpdateTitle(ctx context.Context, id string, expected int64, title, actor string, canPublish bool, description *string) (Recording, error) {
	title = strings.TrimSpace(title)
	if expected < 1 || !ValidTitle(title) || actor == "" || (description != nil && !ValidDescription(*description)) {
		return Recording{}, ErrInvalid
	}
	if err := s.syncLifecycle(ctx); err != nil {
		return Recording{}, err
	}
	return s.repo.UpdateTitle(ctx, id, expected, title, actor, s.now(), canPublish, description)
}

func (s *Service) SetExposure(ctx context.Context, id string, expected int64, featured, hidden bool, actor string) (Recording, error) {
	if expected < 1 || actor == "" {
		return Recording{}, ErrInvalid
	}
	return s.repo.SetExposure(ctx, id, expected, featured, hidden, actor, s.now())
}

func (s *Service) Publish(ctx context.Context, id string, expected int64, actor string) (Recording, error) {
	if err := s.readyToPublish(ctx, id, expected, actor); err != nil {
		return Recording{}, err
	}
	return s.repo.Publish(ctx, id, expected, actor, s.now())
}

func (s *Service) PublishOperation(ctx context.Context, id string, expected int64, actor, key string) (PublishResult, error) {
	if expected < 1 || actor == "" || !ValidOperationKey(key) {
		return PublishResult{}, ErrInvalid
	}
	// Replay is resolved before current-version/readiness checks. An immutable
	// receipt must survive a later unpublish, expiry or unavailable Asset API.
	result, err := s.repo.GetPublishOperation(ctx, id, expected, actor, key, s.now())
	if !errors.Is(err, ErrNotFound) {
		return result, err
	}
	if err := s.readyToPublish(ctx, id, expected, actor); err != nil {
		return PublishResult{}, err
	}
	return s.repo.PublishOperation(ctx, id, expected, actor, key, s.now())
}

func (s *Service) readyToPublish(ctx context.Context, id string, expected int64, actor string) error {
	if expected < 1 || actor == "" {
		return ErrInvalid
	}
	value, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if value.Expired(s.now()) {
		return ErrNotFound
	}
	if value.Version != expected {
		return ErrPrecondition
	}
	if value.Broadcast {
		repo, e := s.broadcastRepository()
		if e != nil {
			return e
		}
		b, e := repo.GetBroadcast(ctx, id)
		if e != nil {
			return e
		}
		if b.Revoked || b.RangePending || b.Projection == nil || b.Projection.State != "ready" || b.View.Boundaries.StartSequence == nil || b.View.Boundaries.EndSequenceExclusive == nil {
			return ErrConflict
		}
	}
	if value.PackageID == "" || value.ReadyAt == nil || !ValidTitle(value.Title) {
		return ErrConflict
	}
	_, err = s.readyPackage(ctx, value)
	return err
}

func (s *Service) Unpublish(ctx context.Context, id string, expected int64, actor string) (Recording, error) {
	if expected < 1 || actor == "" {
		return Recording{}, ErrInvalid
	}
	if err := s.syncLifecycle(ctx); err != nil {
		return Recording{}, err
	}
	return s.repo.Unpublish(ctx, id, expected, actor, s.now())
}

func NewService(repo Repository, mediaBaseURL string, watermarkKey []byte, now func() time.Time) *Service {
	return &Service{repo: repo, mediaBaseURL: strings.TrimRight(mediaBaseURL, "/"), watermarkKey: watermarkKey, now: now}
}

func (s *Service) Create(ctx context.Context, title, actor, key string) (Recording, error) {
	title = strings.TrimSpace(title)
	if !ValidTitle(title) || actor == "" || key == "" {
		return Recording{}, ErrInvalid
	}
	return s.repo.Create(ctx, title, actor, key, s.now())
}

func (s *Service) List(ctx context.Context) ([]Recording, error) {
	if err := s.syncLifecycle(ctx); err != nil {
		return nil, err
	}
	queryDone := diagnostics.StartRequestPhase(ctx, "list_query")
	values, err := s.repo.ListVisible(ctx, s.now())
	queryDone()
	if err != nil {
		return nil, err
	}
	result := make([]Recording, 0, len(values))
	for _, value := range values {
		if value.Visible(s.now()) {
			if value.PackageID != "" {
				duration, err := s.memberDuration(ctx, value)
				if errors.Is(err, assetclient.ErrNotFound) || errors.Is(err, assetclient.ErrForbidden) || errors.Is(err, ErrConflict) {
					continue
				}
				if err != nil {
					return nil, err
				}
				value.DurationSeconds = duration
				result = append(result, value)
				continue
			}
		}
	}
	return result, nil
}

type PlaybackAudio struct {
	Version      string `json:"version"`
	PlaylistPath string `json:"playlistPath"`
}

type Playback struct {
	AudioState         string                           `json:"audioState,omitempty"`
	Audio              *PlaybackAudio                   `json:"audio,omitempty"`
	PackageID          string                           `json:"packageId,omitempty"`
	Renditions         []assetclient.RecordingRendition `json:"renditions,omitempty"`
	MediaURL           string                           `json:"mediaUrl"`
	ExchangeCredential string                           `json:"exchangeCredential"`
	ExpiresAt          time.Time                        `json:"expiresAt"`
	AssetVersionID     string                           `json:"assetVersionId"`
	WatermarkCode      string                           `json:"watermarkCode"`
}

func (s *Service) Playback(ctx context.Context, id, scopeID, expectedVersion, userID string) (Playback, error) {
	if !scopePattern.MatchString(scopeID) || userID == "" || s.mediaBaseURL == "" || len(s.watermarkKey) < 32 {
		return Playback{}, ErrInvalid
	}
	if err := s.syncLifecycle(ctx); err != nil {
		return Playback{}, err
	}
	value, err := s.repo.Get(ctx, id)
	if err != nil {
		return Playback{}, err
	}
	if !value.Visible(s.now()) {
		return Playback{}, ErrNotFound
	}
	return s.packagePlayback(ctx, value, scopeID, expectedVersion, userID)
}
