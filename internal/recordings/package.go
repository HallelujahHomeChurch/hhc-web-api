package recordings

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/diagnostics"
)

func (s *Service) readyPackage(ctx context.Context, value Recording) (assetclient.RecordingPackageStatus, error) {
	defer diagnostics.StartRequestPhase(ctx, "package_calls")()
	if s.packages == nil {
		return assetclient.RecordingPackageStatus{}, assetclient.ErrUnavailable
	}
	pkg, err := s.packages.GetReadyRecordingPackage(ctx, value.PackageID, value.ID)
	if err != nil {
		return pkg, err
	}
	if pkg.State != "ready" || pkg.MediaExpiresAt == nil || !s.now().Before(*pkg.MediaExpiresAt) {
		return pkg, ErrConflict
	}
	_, err = s.projectPackageReady(ctx, value.ID, value.PackageID, pkg)
	return pkg, err
}

func (s *Service) packagePlayback(ctx context.Context, value Recording, scope, expected, user string) (Playback, error) {
	if expected != "" && expected != value.PackageID {
		return Playback{}, ErrPrecondition
	}
	pkg, err := s.readyPackage(ctx, value)
	if err != nil {
		return Playback{}, err
	}
	grant, err := s.packages.IssueRecordingPackageGrant(ctx, value.PackageID, value.ID, scope, *pkg.MediaExpiresAt)
	if err != nil {
		return Playback{}, err
	}
	now := s.now()
	if grant.ExchangeCredential == "" || !grant.ExpiresAt.After(now) || grant.ExpiresAt.After(*pkg.MediaExpiresAt) || grant.ExpiresAt.After(now.Add(time.Hour)) {
		provenance, _ := auditclient.ProvenanceFromContext(ctx)
		slog.WarnContext(ctx, "recording_playback_grant_rejected",
			"request_id", provenance.RequestID,
			"credential_missing", grant.ExchangeCredential == "",
			"expired", !grant.ExpiresAt.After(now),
			"exceeds_media_expiry", grant.ExpiresAt.After(*pkg.MediaExpiresAt),
			"exceeds_max_lifetime", grant.ExpiresAt.After(now.Add(time.Hour)),
			"lifetime_excess_ms", grant.ExpiresAt.Sub(now.Add(time.Hour)).Milliseconds(),
			"lifetime_excess_ns", grant.ExpiresAt.Sub(now.Add(time.Hour)).Nanoseconds())
		return Playback{}, ErrConflict
	}
	// Provider issuance crosses a network boundary. Do not expose a credential
	// if deletion/unpublication committed while that call was in flight.
	current, err := s.repo.Get(ctx, value.ID)
	if err != nil {
		return Playback{}, err
	}
	if !current.Visible(s.now()) || current.PackageID != value.PackageID {
		return Playback{}, ErrNotFound
	}
	// Preserve the existing non-persisted marker until the recording privacy
	// notice is approved and the trace-receipt rollout is explicitly enabled.
	mac := hmac.New(sha256.New, s.watermarkKey)
	mac.Write([]byte(user + ":" + value.ID))
	code := strings.ToUpper(hex.EncodeToString(mac.Sum(nil)[:4]))
	if s.watermarkTraceEnabled {
		receipt, err := s.repo.EnsureRecordingWatermark(ctx, user, value.ID, value.PackageID, s.now())
		if err != nil {
			return Playback{}, err
		}
		code = receipt.Code
	}
	path := "/videos/" + url.PathEscape(value.ID) + "/packages/" + value.PackageID + "/sessions/" + url.PathEscape(scope) + "/master.m3u8"
	var audio *PlaybackAudio
	var audioState string
	if pkg.Audio != nil && pkg.Audio.Version == "aac-hls-v1" {
		switch pkg.Audio.State {
		case "pending", "processing", "ready", "failed":
			audioState = pkg.Audio.State
		}
	}
	if pkg.Audio != nil && pkg.Audio.State == "ready" && pkg.Audio.Version == "aac-hls-v1" && pkg.Audio.PlaylistPath == "audio/aac-hls-v1/index.m3u8" {
		audio = &PlaybackAudio{Version: "aac-hls-v1", PlaylistPath: "audio/aac-hls-v1/index.m3u8"}
	}
	return Playback{AudioState: audioState, Audio: audio, MediaURL: s.mediaBaseURL + path, ExchangeCredential: grant.ExchangeCredential, ExpiresAt: grant.ExpiresAt, PackageID: value.PackageID, Renditions: pkg.Renditions, WatermarkCode: code}, nil
}

var packageIDPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (s *Service) WithPackages(client *assetclient.Client) *Service { s.packages = client; return s }

func (s *Service) CreatePackage(ctx context.Context, id string, inventory assetclient.RecordingPackageInventory, key string) (assetclient.RecordingPackage, error) {
	if s.packages == nil {
		return assetclient.RecordingPackage{}, assetclient.ErrUnavailable
	}
	if !ValidOperationKey(key) {
		return assetclient.RecordingPackage{}, ErrInvalid
	}
	value, err := s.repo.Get(ctx, id)
	if err != nil {
		return assetclient.RecordingPackage{}, err
	}
	if value.Expired(s.now()) {
		return assetclient.RecordingPackage{}, ErrNotFound
	}
	if value.PrimaryAssetVersionID != "" || value.SourceID != "" {
		return assetclient.RecordingPackage{}, ErrConflict
	}
	pkg, err := s.packages.CreateRecordingPackage(ctx, id, inventory, key)
	if err != nil {
		return assetclient.RecordingPackage{}, err
	}
	if pkg.RecordingID != id || !packageIDPattern.MatchString(pkg.PackageID) || pkg.SessionID != pkg.PackageID || pkg.Inventory.InventoryDigest != inventory.InventoryDigest {
		return assetclient.RecordingPackage{}, ErrConflict
	}
	if err := s.repo.AddPackage(ctx, id, pkg.PackageID, s.now()); err != nil {
		return assetclient.RecordingPackage{}, err
	}
	return pkg, nil
}

func (s *Service) packageRecording(ctx context.Context, id, pkg string) (Recording, error) {
	if s.packages == nil {
		return Recording{}, assetclient.ErrUnavailable
	}
	if !packageIDPattern.MatchString(pkg) {
		return Recording{}, ErrInvalid
	}
	value, err := s.repo.Get(ctx, id)
	if err != nil {
		return Recording{}, err
	}
	if value.PackageID != pkg {
		return Recording{}, ErrNotFound
	}
	// Asset owns current expiry, including policy extension after this projection.
	if value.MediaTerminal {
		return Recording{}, ErrNotFound
	}
	return value, nil
}

func (s *Service) projectPackageReady(ctx context.Context, id, pkg string, status assetclient.RecordingPackageStatus) (Recording, error) {
	if status.PackageID != pkg || status.RecordingID != id || status.SessionID != pkg {
		return Recording{}, ErrConflict
	}
	if status.State != "ready" {
		return s.repo.Get(ctx, id)
	}
	if !s.validReadyPackage(id, pkg, status) {
		return Recording{}, ErrConflict
	}
	if repo, ok := s.repo.(LifecycleRepository); ok && status.UploadedAt != nil {
		if err := repo.ApplyLifecycle(ctx, assetclient.RecordingLifecycleSnapshot{Items: []assetclient.RecordingPackageStatus{status}}); err != nil {
			return Recording{}, err
		}
		return s.repo.Get(ctx, id)
	}
	return s.repo.RecordPackageReady(ctx, id, pkg, *status.ReadyAt)
}

func (s *Service) validReadyPackage(id, pkg string, status assetclient.RecordingPackageStatus) bool {
	if status.PackageID != pkg || status.SessionID != pkg || status.RecordingID != id || status.State != "ready" || status.ReadyAt == nil || status.MediaExpiresAt == nil || status.ReadyAt.IsZero() || status.ReadyAt.After(s.now()) {
		return false
	}
	if status.UploadedAt == nil {
		return status.MediaExpiresAt.Equal(status.ReadyAt.Add(720 * time.Hour))
	} // Producer-first compatibility.
	return status.RetentionRevision > 0 && !status.UploadedAt.IsZero() && !status.UploadedAt.After(*status.ReadyAt) && status.MediaExpiresAt.After(*status.UploadedAt) && !status.MediaExpiresAt.After(status.UploadedAt.Add(365*24*time.Hour))
}

func (s *Service) PackageStatus(ctx context.Context, id, pkg, cursor string, limit int) (assetclient.RecordingPackageStatus, error) {
	if _, err := s.packageRecording(ctx, id, pkg); err != nil {
		return assetclient.RecordingPackageStatus{}, err
	}
	value, err := s.packages.GetRecordingPackage(ctx, pkg, cursor, limit)
	if err != nil {
		return assetclient.RecordingPackageStatus{}, err
	}
	if _, err := s.projectPackageReady(ctx, id, pkg, value); err != nil {
		return assetclient.RecordingPackageStatus{}, err
	}
	return value, nil
}

func (s *Service) SignPackage(ctx context.Context, id, pkg string, paths []string) ([]assetclient.SignedRecordingObject, error) {
	if _, err := s.packageRecording(ctx, id, pkg); err != nil {
		return nil, err
	}
	// Asset independently enforces actor ownership, inventory and session TTL.
	return s.packages.SignRecordingPackage(ctx, pkg, paths)
}

func (s *Service) CompletePackage(ctx context.Context, id, pkg string) (assetclient.RecordingPackage, error) {
	if _, err := s.packageRecording(ctx, id, pkg); err != nil {
		return assetclient.RecordingPackage{}, err
	}
	value, err := s.packages.CompleteRecordingPackage(ctx, pkg)
	if err != nil {
		return assetclient.RecordingPackage{}, err
	}
	if value.PackageID != pkg || value.SessionID != pkg || value.RecordingID != id {
		return assetclient.RecordingPackage{}, ErrConflict
	}
	return value, nil
}
