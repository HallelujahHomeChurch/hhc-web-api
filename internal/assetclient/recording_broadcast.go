package assetclient

import (
	"context"
	"net/http"
	"time"
)

type BroadcastRangeInput struct {
	RecordingID          string `json:"recordingId"`
	Epoch                int64  `json:"epoch"`
	RangeRevision        int64  `json:"rangeRevision"`
	StartSequence        *int   `json:"startSequence"`
	EndSequenceExclusive *int   `json:"endSequenceExclusive"`
	Revoked              bool   `json:"revoked"`
	MemberState          string `json:"memberState"`
}
type BroadcastProjection struct {
	Epoch                int64    `json:"epoch"`
	RangeRevision        int64    `json:"rangeRevision"`
	State                string   `json:"state"`
	StartSequence        *int     `json:"startSequence"`
	EndSequenceExclusive *int     `json:"endSequenceExclusive"`
	MediaStartSeconds    *float64 `json:"mediaStartSeconds"`
	DurationSeconds      *float64 `json:"durationSeconds"`
	Revision             int64    `json:"revision"`
}

func (c *Client) CreateRecordingBroadcastCapture(ctx context.Context, recording, key string, epoch int64, frames int, recoveryVersion string) (RecordingCaptureResult, error) {
	var out RecordingCaptureResult
	err := c.request(ctx, http.MethodPost, "/priv/recording-captures", struct {
		RecordingID     string `json:"recordingId"`
		OperationKey    string `json:"operationKey"`
		BroadcastEpoch  int64  `json:"broadcastEpoch"`
		SegmentFrames   int    `json:"segmentFrames,omitempty"`
		RecoveryVersion string `json:"recoveryVersion,omitempty"`
	}{recording, key, epoch, frames, recoveryVersion}, "", &out)
	return out, err
}
func (c *Client) SetRecordingBroadcastRange(ctx context.Context, capture string, in BroadcastRangeInput) (BroadcastRangeInput, error) {
	var out BroadcastRangeInput
	if !recordingPackageID.MatchString(capture) {
		return out, ErrInvalid
	}
	err := c.request(ctx, http.MethodPut, "/priv/recording-captures/"+capture+"/broadcast-range", in, "", &out)
	if err == nil && (out.RecordingID != in.RecordingID || out.Epoch != in.Epoch || out.RangeRevision != in.RangeRevision || out.Revoked != in.Revoked || out.MemberState != in.MemberState || !broadcastSequenceEqual(out.StartSequence, in.StartSequence) || !broadcastSequenceEqual(out.EndSequenceExclusive, in.EndSequenceExclusive)) {
		return out, ErrConflict
	}
	return out, err
}
func broadcastSequenceEqual(a, b *int) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
func (c *Client) GetRecordingBroadcastProjection(ctx context.Context, capture string) (BroadcastProjection, error) {
	var out BroadcastProjection
	if !recordingPackageID.MatchString(capture) {
		return out, ErrInvalid
	}
	err := c.request(ctx, http.MethodGet, "/priv/recording-captures/"+capture+"/broadcast-projection", nil, "", &out)
	return out, err
}

type BroadcastPreviewGrantInput struct {
	RecordingID     string    `json:"recordingId"`
	Epoch           int64     `json:"epoch"`
	UserID          string    `json:"userId"`
	PlaybackScopeID string    `json:"playbackScopeId"`
	ExpiresAt       time.Time `json:"expiresAt"`
}
type BroadcastPreviewGrant struct {
	RecordingID        string    `json:"recordingId"`
	CaptureID          string    `json:"captureId"`
	Epoch              int64     `json:"epoch"`
	Purpose            string    `json:"purpose"`
	Scope              string    `json:"scope"`
	MediaPath          string    `json:"mediaPath"`
	ExchangeCredential string    `json:"exchangeCredential"`
	IssuedAt           time.Time `json:"issuedAt"`
	ExpiresAt          time.Time `json:"expiresAt"`
}

func (c *Client) IssueRecordingBroadcastPreview(ctx context.Context, capture string, in BroadcastPreviewGrantInput) (BroadcastPreviewGrant, error) {
	var out BroadcastPreviewGrant
	err := c.request(ctx, http.MethodPost, "/priv/recording-captures/"+capture+"/preview-grant", in, "", &out)
	return out, err
}
