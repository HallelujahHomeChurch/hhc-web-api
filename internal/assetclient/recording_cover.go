package assetclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
)

var RecordingCoverID = regexp.MustCompile(`^[A-Za-z0-9-]{1,128}$`)
var ErrCoverTooLarge = errors.New("cover_too_large")
var ErrInvalidCover = errors.New("invalid_cover")

type RecordingCover struct {
	ID           string `json:"id"`
	UploadID     string `json:"uploadId"`
	Kind         string `json:"kind"`
	State        string `json:"state"`
	OperationKey string `json:"operationKey,omitempty"`
	Error        string `json:"error,omitempty"`
	ContentPath  string `json:"contentPath,omitempty"`
}
type RecordingCoverUpload struct {
	UploadID string `json:"uploadId"`
	State    string `json:"state"`
}

func coverPath(pkg, id, suffix string) (string, error) {
	if !recordingPackageID.MatchString(pkg) || id == "" {
		return "", ErrInvalid
	}
	return "/priv/recording-packages/" + pkg + suffix + "?" + url.Values{"recordingId": {id}}.Encode(), nil
}

// Cover management never follows an Asset redirect into another ownership boundary.
func (c *Client) coverRequest(ctx context.Context, method, path string, body, destination any) error {
	client := *c
	httpClient := *c.http
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.http = &httpClient
	return client.request(ctx, method, path, body, "", destination)
}
func (c *Client) ListRecordingCovers(ctx context.Context, pkg, id string) ([]RecordingCover, error) {
	path, err := coverPath(pkg, id, "/covers")
	if err != nil {
		return nil, err
	}
	var v struct {
		Items []RecordingCover `json:"items"`
	}
	if err := c.coverRequest(ctx, "GET", path, nil, &v); err != nil {
		return nil, err
	}
	if len(v.Items) > 1000 {
		return nil, ErrUnavailable
	}
	for _, item := range v.Items {
		if !RecordingCoverID.MatchString(item.ID) || !RecordingCoverID.MatchString(item.UploadID) || (item.Kind != "auto" && item.Kind != "custom") {
			return nil, ErrUnavailable
		}
	}
	return v.Items, nil
}
func (c *Client) UploadRecordingCover(ctx context.Context, pkg, id, mimeType, key string, data []byte) (RecordingCoverUpload, error) {
	if len(data) == 0 || len(data) > 5<<20 || (mimeType != "image/png" && mimeType != "image/jpeg") || key == "" {
		return RecordingCoverUpload{}, ErrInvalidCover
	}
	path, err := coverPath(pkg, id, "/cover-uploads")
	if err != nil {
		return RecordingCoverUpload{}, err
	}
	return c.uploadCoverBytes(ctx, path, mimeType, key, data)
}

func (c *Client) uploadCoverBytes(ctx context.Context, path, mimeType, key string, data []byte) (RecordingCoverUpload, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return RecordingCoverUpload{}, ErrInvalid
	}
	req.Header.Set("X-Internal-Caller-App-Id", c.caller)
	req.Header.Set("Content-Type", mimeType)
	req.Header.Set("Idempotency-Key", key)
	p, ok := auditclient.ProvenanceFromContext(ctx)
	if !ok {
		return RecordingCoverUpload{}, ErrForbidden
	}
	req.Header.Set("X-HHC-Actor-ID", p.ActorID)
	req.Header.Set("X-HHC-Request-ID", p.RequestID)
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return RecordingCoverUpload{}, ErrUnavailable
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case 202:
	case 413:
		return RecordingCoverUpload{}, ErrCoverTooLarge
	case 422:
		return RecordingCoverUpload{}, ErrInvalidCover
	case 403:
		return RecordingCoverUpload{}, ErrForbidden
	case 404:
		return RecordingCoverUpload{}, ErrNotFound
	case 409:
		return RecordingCoverUpload{}, ErrConflict
	default:
		return RecordingCoverUpload{}, ErrUnavailable
	}
	var v RecordingCoverUpload
	if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&v) != nil || !RecordingCoverID.MatchString(v.UploadID) {
		return v, ErrUnavailable
	}
	return v, nil
}
func (c *Client) RetainRecordingCover(ctx context.Context, pkg, id, cover, reference string) error {
	if !RecordingCoverID.MatchString(cover) || !RecordingCoverID.MatchString(reference) {
		return ErrInvalid
	}
	path, err := coverPath(pkg, id, "/covers/"+cover+"/retain")
	if err != nil {
		return err
	}
	return c.coverRequest(ctx, "POST", path, map[string]string{"referenceId": reference}, nil)
}
func (c *Client) ReleaseRecordingCover(ctx context.Context, pkg, id, cover, reference string) error {
	if !RecordingCoverID.MatchString(cover) || !RecordingCoverID.MatchString(reference) {
		return ErrInvalid
	}
	path, err := coverPath(pkg, id, "/covers/"+cover+"/references/"+reference)
	if err != nil {
		return err
	}
	err = c.coverRequest(ctx, "DELETE", path, nil, nil)
	if err == ErrNotFound {
		return nil
	}
	return err
}
func (c *Client) ReadRecordingCover(ctx context.Context, pkg, id, cover string) ([]byte, error) {
	if !RecordingCoverID.MatchString(cover) {
		return nil, ErrNotFound
	}
	path, err := coverPath(pkg, id, "/covers/"+cover+"/content")
	if err != nil {
		return nil, err
	}
	return c.readCoverBytes(ctx, path)
}

func (c *Client) readCoverBytes(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+path, nil)
	if err != nil {
		return nil, ErrInvalid
	}
	req.Header.Set("X-Internal-Caller-App-Id", c.caller)
	client := *c.http
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 404 || response.StatusCode == 403 {
		return nil, ErrNotFound
	}
	kind, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != 200 || parseErr != nil || kind != "image/jpeg" {
		return nil, ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return nil, ErrUnavailable
	}
	return data, nil
}
