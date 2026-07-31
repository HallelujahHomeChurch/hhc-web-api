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
}
type PublishPayload struct {
	WorkflowID       string `json:"workflowId"`
	IssueID          string `json:"issueId"`
	Locale           string `json:"locale"`
	AssetID          string `json:"assetId"`
	AggregateVersion int64  `json:"aggregateVersion"`
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
	AssetID          string `json:"assetId"`
	AggregateVersion int64  `json:"aggregateVersion"`
}
type ContentUnpublishPayload struct {
	ContentID        string `json:"contentId"`
	AssetID          string `json:"assetId"`
	GrantID          string `json:"grantId"`
	AggregateVersion int64  `json:"aggregateVersion"`
}

type Repository interface {
	Claim(context.Context, time.Time, time.Duration) (Event, bool, error)
	Retry(context.Context, string, string, time.Time, time.Time) error
	Fail(context.Context, Event, string, time.Time) error
	EventDelivered(context.Context, string) (bool, error)
	CompletePublish(context.Context, Event, string, string, time.Time) error
	CompleteUnpublish(context.Context, Event, time.Time) error
	CompleteContentPublish(context.Context, Event, string, string, time.Time) error
	CompleteContentUnpublish(context.Context, Event, time.Time) error
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
	ID, Namespace, OwnerService, OwnerType, OwnerID, Locale string
	UploadStatus, ScanStatus, ProcessingStatus              string
}
type Grant struct{ ID string }
