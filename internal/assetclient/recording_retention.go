package assetclient

import (
	"context"
	"errors"
	"net/http"
	"time"
)

type RecordingRetentionPolicy struct {
	RetentionDays int        `json:"retentionDays"`
	Revision      int64      `json:"revision"`
	ActivatedAt   *time.Time `json:"activatedAt"`
}
type RecordingRetentionPreview struct {
	PreviewID     string    `json:"previewId"`
	RetentionDays int       `json:"retentionDays"`
	Revision      int64     `json:"revision"`
	EvaluatedAt   time.Time `json:"evaluatedAt"`
	AffectedCount int64     `json:"affectedCount"`
	AffectedBytes int64     `json:"affectedBytes"`
}
type UpdateRecordingRetentionInput struct {
	RetentionDays    int    `json:"retentionDays"`
	ExpectedRevision int64  `json:"expectedRevision"`
	PreviewID        string `json:"previewId"`
}
type RecordingLifecycleBinding struct {
	RecordingID string `json:"recordingId"`
	PackageID   string `json:"packageId"`
}
type RecordingLifecycleSnapshot struct {
	Policy      RecordingRetentionPolicy         `json:"policy"`
	Items       []RecordingPackageStatus         `json:"items"`
	SourceItems []RecordingSourceLifecycleStatus `json:"sourceItems,omitempty"`
}

type RecordingSourceLifecycleBinding struct {
	RecordingID string `json:"recordingId"`
	SourceID    string `json:"sourceId"`
}
type RecordingSourceLifecycleStatus struct {
	RecordingID        string                       `json:"recordingId"`
	SourceID           string                       `json:"sourceId"`
	State              string                       `json:"state"`
	ProcessingProgress *RecordingProcessingProgress `json:"processingProgress,omitempty"`
}

func (c *Client) GetRecordingRetentionPolicy(ctx context.Context) (RecordingRetentionPolicy, error) {
	var v RecordingRetentionPolicy
	err := c.request(ctx, http.MethodGet, "/priv/recordings/retention-policy", nil, "", &v)
	return v, err
}
func (c *Client) PreviewRecordingRetentionPolicy(ctx context.Context, days int) (RecordingRetentionPreview, error) {
	var v RecordingRetentionPreview
	if days < 1 || days > 365 {
		return v, ErrInvalid
	}
	err := c.request(ctx, http.MethodPost, "/priv/recordings/retention-policy/preview", map[string]int{"retentionDays": days}, "", &v)
	return v, err
}
func (c *Client) UpdateRecordingRetentionPolicy(ctx context.Context, in UpdateRecordingRetentionInput, key string) (RecordingRetentionPolicy, error) {
	var v RecordingRetentionPolicy
	if in.RetentionDays < 1 || in.RetentionDays > 365 || in.ExpectedRevision < 1 || in.PreviewID == "" || key == "" || len(key) > 128 {
		return v, ErrInvalid
	}
	err := c.request(ctx, http.MethodPut, "/priv/recordings/retention-policy", in, key, &v)
	return v, err
}
func (c *Client) RecordingLifecycle(ctx context.Context, items []RecordingLifecycleBinding) (RecordingLifecycleSnapshot, error) {
	return c.RecordingLifecycleWithSources(ctx, items, nil)
}

func (c *Client) RecordingLifecycleWithSources(ctx context.Context, items []RecordingLifecycleBinding, sources []RecordingSourceLifecycleBinding) (RecordingLifecycleSnapshot, error) {
	var v RecordingLifecycleSnapshot
	if len(items)+len(sources) > 1000 {
		return v, ErrInvalid
	}
	err := c.request(ctx, http.MethodPost, "/priv/recordings/lifecycle", struct {
		Items       []RecordingLifecycleBinding       `json:"items"`
		SourceItems []RecordingSourceLifecycleBinding `json:"sourceItems,omitempty"`
	}{items, sources}, "", &v)
	// A strict legacy decoder rejects the additive field before binding checks.
	// Retry only its exact decoder error, never authorization/association failures;
	// the legacy response contains no source data and therefore no source grants.
	if len(sources) > 0 && errors.Is(err, errLifecycleBodyRejected) {
		return c.RecordingLifecycle(ctx, items)
	}
	return v, err
}
