package publication

import (
	"context"
	"errors"
	"time"
)

var ErrStalePublication = errors.New("publication was superseded")

type Event struct {
	ID, EventType, AggregateID string
	AggregateVersion           int64
	Payload                    []byte
	Attempts                   int
	CreatedAt                  time.Time
}
type PublishPayload struct {
	WorkflowID        string `json:"workflowId"`
	IssueID           string `json:"issueId"`
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
type BulletinNotificationPayload struct {
	IssueID      string                             `json:"issueId"`
	ActorID      string                             `json:"actorId"`
	Name         string                             `json:"name"`
	Translations map[string]NotificationTranslation `json:"translations"`
}
type UnpublishPayload struct {
	WorkflowID       string `json:"workflowId"`
	IssueID          string `json:"issueId"`
	Locale           string `json:"locale"`
	AssetID          string `json:"assetId"`
	GrantID          string `json:"grantId"`
	AggregateVersion int64  `json:"aggregateVersion"`
}
type ContentPublishPayload struct {
	ContentID        string `json:"contentId"`
	AssetID          string `json:"assetId,omitempty"`
	HomeAssetID      string `json:"homeAssetId,omitempty"`
	AggregateVersion int64  `json:"aggregateVersion"`
}
type PublishedAsset struct {
	Usage     string `json:"usage"`
	AssetID   string `json:"assetId"`
	GrantID   string `json:"grantId"`
	PublicURL string `json:"publicUrl,omitempty"`
}
type ContentUnpublishPayload struct {
	ContentID        string           `json:"contentId"`
	AssetID          string           `json:"assetId,omitempty"`
	GrantID          string           `json:"grantId,omitempty"`
	Assets           []PublishedAsset `json:"assets,omitempty"`
	AggregateVersion int64            `json:"aggregateVersion"`
}

type Repository interface {
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
	UploadStatus, ScanStatus, ProcessingStatus                       string
}
type Grant struct{ ID string }

type NotificationClient interface {
	QueueBulletinNotification(context.Context, BulletinNotificationPayload) error
}
