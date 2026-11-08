package recordings

import (
	"errors"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrNotFound            = errors.New("recording not found")
	ErrConflict            = errors.New("recording conflict")
	ErrDraftLimit          = fmt.Errorf("%w: ordinary recording draft limit reached", ErrConflict)
	ErrInvalid             = errors.New("invalid recording")
	ErrPrecondition        = errors.New("recording version mismatch")
	ErrTransactionCapacity = errors.New("recording transaction capacity exhausted")
	ErrAuditUnavailable    = errors.New("recording audit unavailable")
	ErrForbidden           = errors.New("recording permission denied")
)

const EntitlementCode = "video.meeting-recordings.access"

// PublishReceipt is immutable evidence of one accepted intent, not current
// visibility. Current and Outcome must be checked before reporting success.
type PublishReceipt struct {
	OperationKey       string    `json:"operationKey"`
	ActorType          string    `json:"actorType"`
	ActorID            string    `json:"actorId"`
	RecordingID        string    `json:"recordingId"`
	AssetVersionID     string    `json:"assetVersionId"`
	PackageID          string    `json:"packageId,omitempty"`
	ExpectedVersion    int64     `json:"expectedVersion"`
	PublishedVersion   int64     `json:"publishedVersion"`
	PublishedAt        time.Time `json:"publishedAt"`
	RequestFingerprint string    `json:"requestFingerprint"`
}

type PublishResult struct {
	Receipt PublishReceipt `json:"receipt"`
	Current Recording      `json:"current"`
	Outcome string         `json:"outcome"`
}

func NewPublishResult(receipt PublishReceipt, current Recording, now time.Time) PublishResult {
	outcome := "state_changed"
	if current.Visible(now) && current.Version == receipt.PublishedVersion && current.PrimaryAssetVersionID == receipt.AssetVersionID && current.PackageID == receipt.PackageID {
		outcome = "published"
	}
	return PublishResult{Receipt: receipt, Current: current, Outcome: outcome}
}

var operationKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9._:-]{1,128}$`)

func ValidOperationKey(key string) bool { return operationKeyPattern.MatchString(key) }

type Recording struct {
	PublicID              string                                   `json:"publicId,omitempty"`
	BroadcastStartedAt    *time.Time                               `json:"-"`
	Broadcast             bool                                     `json:"-"`
	ProcessingProgress    *assetclient.RecordingProcessingProgress `json:"-"`
	SelectedCoverID       string                                   `json:"selectedCoverId,omitempty"`
	ID                    string                                   `json:"id"`
	Title                 string                                   `json:"title"`
	Description           string                                   `json:"description,omitempty"`
	UploadedAt            *time.Time                               `json:"uploadedAt"`
	ExpiresAt             *time.Time                               `json:"expiresAt"`
	DraftExpiresAt        time.Time                                `json:"-"`
	Featured              bool                                     `json:"featured"`
	Hidden                bool                                     `json:"hidden"`
	Status                string                                   `json:"status"`
	PrimaryAssetVersionID string                                   `json:"primaryAssetVersionId,omitempty"`
	DurationSeconds       float64                                  `json:"durationSeconds,omitempty"`
	Version               int64                                    `json:"version"`
	PackageID             string                                   `json:"packageId,omitempty"`
	SourceID              string                                   `json:"sourceId,omitempty"`
	ReadyAt               *time.Time                               `json:"readyAt,omitempty"`
	MediaTerminal         bool                                     `json:"-"`
}

func ValidTitle(title string) bool {
	return strings.TrimSpace(title) != "" && utf8.RuneCountInString(title) <= 180
}

func ValidDescription(value string) bool {
	return utf8.ValidString(value) && utf8.RuneCountInString(value) <= 5000 && !strings.ContainsRune(value, 0)
}

func (r Recording) Visible(now time.Time) bool {
	return !r.MediaTerminal && r.Status == "published" && !r.Hidden && (r.PackageID != "" && r.ReadyAt != nil) && r.UploadedAt != nil && r.ExpiresAt != nil && now.Before(*r.ExpiresAt)
}

func (r Recording) Expired(now time.Time) bool {
	if r.MediaTerminal {
		return true
	}
	if r.ExpiresAt != nil {
		return !now.Before(*r.ExpiresAt)
	}
	return !r.Broadcast && !now.Before(r.DraftExpiresAt)
}
