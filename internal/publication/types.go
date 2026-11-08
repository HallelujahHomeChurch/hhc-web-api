package publication

import (
	"context"
	"errors"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"time"
)

var ErrStalePublication = errors.New("publication was superseded")

type Event struct {
	ID, EventType, AggregateID string
	ActorID                    string
	AggregateVersion           int64
	Payload                    []byte
	Attempts                   int
	CreatedAt                  time.Time
	ClaimedUntil               time.Time
}
type PublishPayload struct {
	WorkflowID        string `json:"workflowId"`
	IssueID           string `json:"issueId"`
	Series            string `json:"series,omitempty"`
	Locale            string `json:"locale"`
	AssetID           string `json:"assetId"`
	AggregateVersion  int64  `json:"aggregateVersion"`
	NotifySubscribers bool   `json:"notifySubscribers,omitempty"`
	ActorID           string `json:"actorId,omitempty"`
}
type NotificationTranslation struct {
	Subject       string `json:"subject"`
	Body          string `json:"body"`
	ClickBehavior string `json:"clickBehavior"`
	ActionURL     string `json:"actionUrl"`
}
type NotificationCommand struct {
	Channel              string                             `json:"channel"`
	AudienceType         string                             `json:"audienceType"`
	Translations         map[string]NotificationTranslation `json:"translations"`
	RequiredEntitlements map[string]string                  `json:"requiredEntitlements,omitempty"`
}
type BulletinSource struct {
	Kind                   string `json:"kind"`
	IssueID                string `json:"issueId,omitempty"`
	RecordingID            string `json:"recordingId,omitempty"`
	Series                 string `json:"series,omitempty"`
	NotificationGeneration int    `json:"notificationGeneration"`
}
type NotificationSubmission struct {
	Name           string                `json:"name"`
	Source         *BulletinSource       `json:"source,omitempty"`
	Notifications  []NotificationCommand `json:"notifications"`
	ActorID        string                `json:"-"`
	RequestID      string                `json:"-"`
	IdempotencyKey string                `json:"-"`
}
type NotificationSubmissionPayload struct {
	Name           string                `json:"name"`
	Source         *BulletinSource       `json:"source,omitempty"`
	Notifications  []NotificationCommand `json:"notifications"`
	ActorID        string                `json:"actorId"`
	RequestID      string                `json:"requestId"`
	IdempotencyKey string                `json:"idempotencyKey"`
}
type BulletinNotificationPayload struct {
	IssueID      string                             `json:"issueId"`
	ActorID      string                             `json:"actorId"`
	RequestID    string                             `json:"requestId"`
	Name         string                             `json:"name"`
	Translations map[string]NotificationTranslation `json:"translations"`
}
type UnpublishPayload struct {
	ActorID          string `json:"actorId,omitempty"`
	RequestID        string `json:"requestId,omitempty"`
	WorkflowID       string `json:"workflowId"`
	IssueID          string `json:"issueId"`
	Series           string `json:"series,omitempty"`
	Locale           string `json:"locale"`
	AssetID          string `json:"assetId"`
	GrantID          string `json:"grantId"`
	AggregateVersion int64  `json:"aggregateVersion"`
}
type ContentPublishPayload struct {
	StatementNotification *content.StatementNotificationRequest `json:"statementNotification,omitempty"`
	ActorID               string                                `json:"actorId,omitempty"`
	ContentID             string                                `json:"contentId"`
	AssetID               string                                `json:"assetId,omitempty"`
	HomeAssetID           string                                `json:"homeAssetId,omitempty"`
	InlineAssetIDs        []string                              `json:"inlineAssetIds,omitempty"`
	ManifestSHA256        string                                `json:"manifestSha256,omitempty"`
	AggregateVersion      int64                                 `json:"aggregateVersion"`
}
type PublishedAsset struct {
	Usage     string `json:"usage"`
	AssetID   string `json:"assetId"`
	GrantID   string `json:"grantId"`
	PublicURL string `json:"publicUrl,omitempty"`
}
type ContentUnpublishPayload struct {
	ActorID          string           `json:"actorId,omitempty"`
	RequestID        string           `json:"requestId,omitempty"`
	ContentID        string           `json:"contentId"`
	AssetID          string           `json:"assetId,omitempty"`
	GrantID          string           `json:"grantId,omitempty"`
	Assets           []PublishedAsset `json:"assets,omitempty"`
	AggregateVersion int64            `json:"aggregateVersion"`
}

type Repository interface {
	StatementNotification(context.Context, string) (content.StatementNotificationBatch, error)
	SetStatementCampaign(context.Context, string, string) error
	CompleteStatementNotification(context.Context, Event, content.StatementNotificationBatch, time.Time) error
	Claim(context.Context, time.Time, time.Duration) (Event, bool, error)
	Retry(context.Context, string, string, time.Time, time.Time) error
	Defer(context.Context, string, string, time.Time, time.Time) error
	Fail(context.Context, Event, string, time.Time) error
	FailPublish(context.Context, Event, string, string, string, time.Time) error
	FailContentPublish(context.Context, Event, []PublishedAsset, string, time.Time) error
	EventDelivered(context.Context, string) (bool, error)
	CompletePublish(context.Context, Event, string, string, time.Time) error
	CompleteUnpublish(context.Context, Event, time.Time) error
	CompleteContentPublish(context.Context, Event, []PublishedAsset, time.Time) error
	CompleteContentUnpublish(context.Context, Event, time.Time) error
	CompleteNotification(context.Context, Event, time.Time) error
	Complete(context.Context, string, time.Time) error
}
type AssetClient interface {
	Get(context.Context, string) (Asset, error)
	CreatePublicGrant(context.Context, string, string) (Grant, error)
	RevokeGrant(context.Context, string, string) error
	Delete(context.Context, string) error
	PublicURL(string) string
}
type Asset struct {
	ID, Namespace, OwnerService, OwnerType, OwnerID, Locale, Purpose string
	UploadStatus, ScanStatus, ProcessingStatus, DetectedMIMEType     string
	ChecksumSHA256                                                   string
}
type Grant struct{ ID string }

type NotificationClient interface {
	SubmitNotifications(context.Context, NotificationSubmission) error
	CreateStatementCampaign(context.Context, content.StatementNotificationBatch) (string, error)
	SendStatementCampaign(context.Context, string, string) error
	CancelStatementCampaign(context.Context, string, string) error
	QueueBulletinNotification(context.Context, BulletinNotificationPayload) error
}

type MessageQueue interface {
	Enqueue(context.Context, []byte) error
}
