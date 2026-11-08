package assetclient

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

var ErrCaptureExpired = errors.New("capture_expired")
var ErrCaptureMissingObjects = errors.New("capture_missing_objects")

type RecordingCaptureObject struct {
	RecordingPackageObject
	State string `json:"state"`
}
type RecordingCaptureReceipt struct {
	OperationKey string    `json:"operationKey"`
	Operation    string    `json:"operation"`
	AcceptedAt   time.Time `json:"acceptedAt"`
	CaptureID    string    `json:"captureId"`
}
type RecordingLiveProgress struct {
	Revision        int64      `json:"revision"`
	FirstSequence   int        `json:"firstSequence"`
	LastSequence    int        `json:"lastSequence"`
	MediaEndSeconds float64    `json:"mediaEndSeconds"`
	LastAdvancedAt  *time.Time `json:"lastAdvancedAt"`
	EndedAt         *time.Time `json:"endedAt"`
	Ended           bool       `json:"ended"`
}
type RecordingCaptureStatus struct {
	CaptureID       string                   `json:"captureId"`
	RecordingID     string                   `json:"recordingId"`
	State           string                   `json:"state"`
	CreatedAt       time.Time                `json:"createdAt"`
	ExpiresAt       time.Time                `json:"expiresAt"`
	DeclaredBytes   int64                    `json:"declaredBytes"`
	DeclaredObjects int                      `json:"declaredObjects"`
	Objects         []RecordingCaptureObject `json:"objects"`
	NextCursor      string                   `json:"nextCursor"`
	PackageID       *string                  `json:"packageId"`
	TerminalAt      *time.Time               `json:"terminalAt"`
	TerminalReason  *string                  `json:"terminalReason"`
	Progress        RecordingLiveProgress    `json:"progress"`
}
type RecordingCaptureResult struct {
	Capture RecordingCaptureStatus  `json:"capture"`
	Receipt RecordingCaptureReceipt `json:"receipt"`
}
type RecordingCaptureDeclareInput struct {
	OperationKey string                   `json:"operationKey"`
	Objects      []RecordingPackageObject `json:"objects"`
}
type RecordingCaptureConfirmInput struct {
	OperationKey string   `json:"operationKey"`
	Paths        []string `json:"paths"`
}
type RecordingCaptureSealInput struct {
	OperationKey string                    `json:"operationKey"`
	NormalEnd    bool                      `json:"normalEnd"`
	Inventory    RecordingPackageInventory `json:"inventory"`
}
type RecordingCaptureAbortInput struct {
	OperationKey string `json:"operationKey"`
	ReasonCode   string `json:"reasonCode"`
}

func (c *Client) CreateRecordingCapture(ctx context.Context, recording, key string) (RecordingCaptureResult, error) {
	var result RecordingCaptureResult
	err := c.request(ctx, http.MethodPost, "/priv/recording-captures", struct {
		RecordingID  string `json:"recordingId"`
		OperationKey string `json:"operationKey"`
	}{recording, key}, "", &result)
	return result, err
}
func (c *Client) GetRecordingCapture(ctx context.Context, id, cursor string, limit int) (RecordingCaptureStatus, error) {
	if !recordingPackageID.MatchString(id) || len(cursor) > 128 || limit < 1 || limit > 1000 {
		return RecordingCaptureStatus{}, ErrInvalid
	}
	var result RecordingCaptureStatus
	query := url.Values{"cursor": {cursor}, "limit": {strconv.Itoa(limit)}}
	err := c.request(ctx, http.MethodGet, "/priv/recording-captures/"+id+"?"+query.Encode(), nil, "", &result)
	return result, err
}
func (c *Client) mutateRecordingCapture(ctx context.Context, id, operation string, input any) (RecordingCaptureResult, error) {
	if !recordingPackageID.MatchString(id) {
		return RecordingCaptureResult{}, ErrInvalid
	}
	var result RecordingCaptureResult
	err := c.request(ctx, http.MethodPost, "/priv/recording-captures/"+id+"/"+operation, input, "", &result)
	return result, err
}
func (c *Client) DeclareRecordingCapture(ctx context.Context, id string, input RecordingCaptureDeclareInput) (RecordingCaptureResult, error) {
	return c.mutateRecordingCapture(ctx, id, "objects", input)
}
func (c *Client) ConfirmRecordingCapture(ctx context.Context, id string, input RecordingCaptureConfirmInput) (RecordingCaptureResult, error) {
	return c.mutateRecordingCapture(ctx, id, "confirm", input)
}
func (c *Client) SealRecordingCapture(ctx context.Context, id string, input RecordingCaptureSealInput) (RecordingCaptureResult, error) {
	return c.mutateRecordingCapture(ctx, id, "seal", input)
}
func (c *Client) AbortRecordingCapture(ctx context.Context, id string, input RecordingCaptureAbortInput) (RecordingCaptureResult, error) {
	return c.mutateRecordingCapture(ctx, id, "abort", input)
}
func (c *Client) SignRecordingCapture(ctx context.Context, id string, paths []string) ([]SignedRecordingObject, error) {
	if !recordingPackageID.MatchString(id) || len(paths) < 1 || len(paths) > 100 {
		return nil, ErrInvalid
	}
	var result []SignedRecordingObject
	err := c.request(ctx, http.MethodPost, "/priv/recording-captures/"+id+"/sign", struct {
		Paths []string `json:"paths"`
	}{paths}, "", &result)
	return result, err
}

func (c *Client) GetRecordingLiveProgress(ctx context.Context, id string) (RecordingLiveProgress, error) {
	if !recordingPackageID.MatchString(id) {
		return RecordingLiveProgress{}, ErrInvalid
	}
	var result RecordingLiveProgress
	err := c.request(ctx, http.MethodGet, "/priv/recording-captures/"+id+"/progress", nil, "", &result)
	return result, err
}
func (c *Client) IssueRecordingLiveGrant(ctx context.Context, capture, recording, user, scope string, expires time.Time) (RecordingGrant, error) {
	if !recordingPackageID.MatchString(capture) {
		return RecordingGrant{}, ErrInvalid
	}
	var result RecordingGrant
	err := c.request(ctx, http.MethodPost, "/priv/recording-captures/"+capture+"/grant", struct {
		RecordingID     string    `json:"recordingId"`
		UserID          string    `json:"userId"`
		PlaybackScopeID string    `json:"playbackScopeId"`
		ExpiresAt       time.Time `json:"expiresAt"`
	}{recording, user, scope, expires}, "", &result)
	return result, err
}
