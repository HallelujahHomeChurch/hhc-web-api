package assetclient

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type RecordingSourceInput struct {
	RecordingID    string `json:"recordingId"`
	FileName       string `json:"fileName"`
	SizeBytes      int64  `json:"sizeBytes"`
	ChecksumSHA256 string `json:"checksumSHA256"`
}

type RecordingSource struct {
	SourceID       string     `json:"sourceId"`
	RecordingID    string     `json:"recordingId"`
	FileName       string     `json:"fileName"`
	SizeBytes      int64      `json:"sizeBytes"`
	ChecksumSHA256 string     `json:"checksumSHA256"`
	BlockCount     int        `json:"blockCount"`
	State          string     `json:"state"`
	CreatedAt      time.Time  `json:"createdAt"`
	ExpiresAt      time.Time  `json:"expiresAt"`
	CompletedAt    *time.Time `json:"completedAt,omitempty"`
	RetryUntil     *time.Time `json:"retryUntil,omitempty"`
	FailureCode    string     `json:"failureCode,omitempty"`
	PackageID      string     `json:"packageId,omitempty"`
}
type RecordingSourceStatus struct {
	RecordingSource
	ProcessingProgress *RecordingProcessingProgress `json:"processingProgress,omitempty"`
	ConfirmedBlocks    []int                        `json:"confirmedBlocks"`
	NextCursor         int                          `json:"nextCursor"`
}
type SignedRecordingSourceBlock struct {
	Number int `json:"number"`
	UploadTarget
}

func (c *Client) CreateRecordingSource(ctx context.Context, input RecordingSourceInput, key string) (RecordingSource, error) {
	var value RecordingSource
	err := c.request(ctx, http.MethodPost, "/priv/recording-sources", input, key, &value)
	return value, err
}

func (c *Client) GetRecordingSource(ctx context.Context, id string, cursor, limit int) (RecordingSourceStatus, error) {
	if !recordingPackageID.MatchString(id) || cursor < 0 || cursor > 2981 || limit < 1 || limit > 1000 {
		return RecordingSourceStatus{}, ErrInvalid
	}
	query := url.Values{"cursor": {strconv.Itoa(cursor)}, "limit": {strconv.Itoa(limit)}}
	var value RecordingSourceStatus
	err := c.request(ctx, http.MethodGet, "/priv/recording-sources/"+id+"?"+query.Encode(), nil, "", &value)
	if err == nil && !value.ProcessingProgress.Valid() {
		return RecordingSourceStatus{}, ErrUnavailable
	}
	return value, err
}

func (c *Client) SignRecordingSource(ctx context.Context, id string, numbers []int) ([]SignedRecordingSourceBlock, error) {
	if !recordingPackageID.MatchString(id) || len(numbers) < 1 || len(numbers) > 100 {
		return nil, ErrInvalid
	}
	seen := map[int]bool{}
	for _, n := range numbers {
		if n < 1 || n > 2981 || seen[n] {
			return nil, ErrInvalid
		}
		seen[n] = true
	}
	var value []SignedRecordingSourceBlock
	err := c.request(ctx, http.MethodPost, "/priv/recording-sources/"+id+"/sign", map[string]any{"numbers": numbers}, "", &value)
	return value, err
}

func (c *Client) CompleteRecordingSource(ctx context.Context, id string) (RecordingSource, error) {
	if !recordingPackageID.MatchString(id) {
		return RecordingSource{}, ErrInvalid
	}
	var value RecordingSource
	err := c.request(ctx, http.MethodPost, "/priv/recording-sources/"+id+"/complete", nil, "", &value)
	return value, err
}

func (c *Client) RetryRecordingSource(ctx context.Context, id string) (RecordingSource, error) {
	if !recordingPackageID.MatchString(id) {
		return RecordingSource{}, ErrInvalid
	}
	var value RecordingSource
	err := c.request(ctx, http.MethodPost, "/priv/recording-sources/"+id+"/retry-processing", nil, "", &value)
	return value, err
}

func (c *Client) GetReadyRecordingSource(ctx context.Context, id, recordingID string) (RecordingSource, error) {
	if !recordingPackageID.MatchString(id) {
		return RecordingSource{}, ErrInvalid
	}
	query := url.Values{"recordingId": {recordingID}}
	var value RecordingSource
	err := c.request(ctx, http.MethodGet, "/priv/recording-sources/"+id+"/ready?"+query.Encode(), nil, "", &value)
	return value, err
}
