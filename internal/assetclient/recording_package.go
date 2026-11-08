package assetclient

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

var recordingPackageID = regexp.MustCompile(`^[a-f0-9]{32}$`)

// These wire fields follow Asset's canonical package contract. The Asset owner
// validates inventory integrity, media and readiness; CMS must not infer ready.
type RecordingPackageInventory struct {
	SchemaVersion   int                      `json:"schemaVersion"`
	PresetVersion   string                   `json:"presetVersion"`
	Objects         []RecordingPackageObject `json:"objects"`
	Renditions      []RecordingRendition     `json:"renditions"`
	InventoryDigest string                   `json:"inventoryDigest,omitempty"`
}
type RecordingPackageObject struct {
	Path      string `json:"path"`
	SizeBytes int64  `json:"sizeBytes"`
	SHA256    string `json:"sha256"`
}
type RecordingRendition struct {
	Name            string  `json:"name"`
	Width           int     `json:"width"`
	Height          int     `json:"height"`
	FrameRate       float64 `json:"frameRate"`
	VideoBitrate    int64   `json:"videoBitrate"`
	AudioBitrate    int64   `json:"audioBitrate"`
	DurationSeconds float64 `json:"durationSeconds"`
	SegmentCount    int     `json:"segmentCount"`
}
type RecordingPackage struct {
	PackageID      string                    `json:"packageId"`
	SessionID      string                    `json:"sessionId"`
	RecordingID    string                    `json:"recordingId"`
	State          string                    `json:"state"`
	SizeBytes      int64                     `json:"sizeBytes"`
	CreatedAt      time.Time                 `json:"createdAt"`
	ExpiresAt      time.Time                 `json:"expiresAt"`
	Inventory      RecordingPackageInventory `json:"inventory"`
	ReadyAt        *time.Time                `json:"readyAt,omitempty"`
	MediaExpiresAt *time.Time                `json:"mediaExpiresAt,omitempty"`
}
type RecordingPackageAudio struct {
	Version      string `json:"version"`
	State        string `json:"state"`
	PlaylistPath string `json:"playlistPath,omitempty"`
}

type RecordingPackageStatus struct {
	Audio              *RecordingPackageAudio       `json:"audio,omitempty"`
	ProcessingProgress *RecordingProcessingProgress `json:"processingProgress,omitempty"`
	PackageID          string                       `json:"packageId"`
	SessionID          string                       `json:"sessionId"`
	RecordingID        string                       `json:"recordingId"`
	State              string                       `json:"state"`
	SizeBytes          int64                        `json:"sizeBytes"`
	ExpiresAt          time.Time                    `json:"expiresAt"`
	ConfirmedObjects   []string                     `json:"confirmedObjects"`
	NextCursor         string                       `json:"nextCursor"`
	ReadyAt            *time.Time                   `json:"readyAt,omitempty"`
	MediaExpiresAt     *time.Time                   `json:"mediaExpiresAt,omitempty"`
	UploadedAt         *time.Time                   `json:"uploadedAt,omitempty"`
	RetentionRevision  int64                        `json:"retentionRevision"`
	Renditions         []RecordingRendition         `json:"renditions,omitempty"`
}
type SignedRecordingObject struct {
	Path    string              `json:"path"`
	URL     string              `json:"url"`
	Method  string              `json:"method"`
	Headers map[string][]string `json:"headers"`
}

func (c *Client) CreateRecordingPackage(ctx context.Context, recordingID string, inventory RecordingPackageInventory, key string) (RecordingPackage, error) {
	var value RecordingPackage
	err := c.request(ctx, http.MethodPost, "/priv/recording-packages", struct {
		RecordingID string                    `json:"recordingId"`
		Inventory   RecordingPackageInventory `json:"inventory"`
	}{recordingID, inventory}, key, &value)
	return value, err
}
func (c *Client) GetRecordingPackage(ctx context.Context, id, cursor string, limit int) (RecordingPackageStatus, error) {
	if !recordingPackageID.MatchString(id) || limit < 1 || limit > 1000 || len(cursor) > 128 {
		return RecordingPackageStatus{}, ErrInvalid
	}
	query := url.Values{"cursor": {cursor}, "limit": {strconv.Itoa(limit)}}
	var value RecordingPackageStatus
	err := c.request(ctx, http.MethodGet, "/priv/recording-packages/"+url.PathEscape(id)+"?"+query.Encode(), nil, "", &value)
	if err == nil && !value.ProcessingProgress.Valid() {
		return RecordingPackageStatus{}, ErrUnavailable
	}
	return value, err
}
func (c *Client) SignRecordingPackage(ctx context.Context, id string, paths []string) ([]SignedRecordingObject, error) {
	if !recordingPackageID.MatchString(id) || len(paths) < 1 || len(paths) > 100 {
		return nil, ErrInvalid
	}
	var value []SignedRecordingObject
	err := c.request(ctx, http.MethodPost, "/priv/recording-packages/"+url.PathEscape(id)+"/sign", map[string]any{"paths": paths}, "", &value)
	return value, err
}
func (c *Client) CompleteRecordingPackage(ctx context.Context, id string) (RecordingPackage, error) {
	if !recordingPackageID.MatchString(id) {
		return RecordingPackage{}, ErrInvalid
	}
	var value RecordingPackage
	err := c.request(ctx, http.MethodPost, "/priv/recording-packages/"+url.PathEscape(id)+"/complete", nil, "", &value)
	return value, err
}
func (c *Client) IssueRecordingPackageGrant(ctx context.Context, id, recordingID, scopeID string, expiresAt time.Time) (RecordingGrant, error) {
	if !recordingPackageID.MatchString(id) {
		return RecordingGrant{}, ErrInvalid
	}
	var value RecordingGrant
	err := c.request(ctx, http.MethodPost, "/priv/recording-packages/"+url.PathEscape(id)+"/grant", map[string]any{"recordingId": recordingID, "scopeId": scopeID, "recordingExpiresAt": expiresAt}, "", &value)
	return value, err
}

func (c *Client) GetReadyRecordingPackage(ctx context.Context, id, recordingID string) (RecordingPackageStatus, error) {
	if !recordingPackageID.MatchString(id) {
		return RecordingPackageStatus{}, ErrInvalid
	}
	var value RecordingPackageStatus
	query := url.Values{"recordingId": {recordingID}}
	err := c.request(ctx, http.MethodGet, "/priv/recording-packages/"+id+"/ready?"+query.Encode(), nil, "", &value)
	return value, err
}
