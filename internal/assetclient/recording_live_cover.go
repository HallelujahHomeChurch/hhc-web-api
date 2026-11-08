package assetclient

import (
	"context"
	"github.com/google/uuid"
	"net/url"
	"strings"
)

func liveCoverPath(scope, id, suffix string) (string, error) {
	broadcastScope := strings.HasPrefix(scope, "broadcast-") && strings.TrimPrefix(scope, "broadcast-") == id
	if broadcastScope {
		_, e := uuid.Parse(id)
		if e != nil {
			return "", ErrInvalid
		}
	}
	if scope != "defaults" && ((!recordingPackageID.MatchString(scope) && !broadcastScope) || id == "") || scope == "defaults" && id != "" {
		return "", ErrInvalid
	}
	return "/priv/recording-live-covers/" + scope + suffix + "?" + url.Values{"recordingId": {id}}.Encode(), nil
}
func (c *Client) UploadLiveCover(ctx context.Context, scope, id, mime, key string, data []byte) (RecordingCoverUpload, error) {
	if len(data) == 0 || len(data) > 5<<20 || mime != "image/jpeg" && mime != "image/png" || key == "" {
		return RecordingCoverUpload{}, ErrInvalidCover
	}
	path, err := liveCoverPath(scope, id, "/uploads")
	if err != nil {
		return RecordingCoverUpload{}, err
	}
	return c.uploadCoverBytes(ctx, path, mime, key, data)
}
func (c *Client) GetLiveCoverUpload(ctx context.Context, scope, id, upload string) (RecordingCoverUpload, error) {
	if !RecordingCoverID.MatchString(upload) {
		return RecordingCoverUpload{}, ErrNotFound
	}
	path, err := liveCoverPath(scope, id, "/uploads/"+upload)
	if err != nil {
		return RecordingCoverUpload{}, err
	}
	var v RecordingCoverUpload
	if err = c.coverRequest(ctx, "GET", path, nil, &v); err != nil {
		return v, err
	}
	if !RecordingCoverID.MatchString(v.UploadID) || v.UploadID != upload {
		return v, ErrUnavailable
	}
	return v, nil
}
func (c *Client) CreateLiveAutoCover(ctx context.Context, scope, id string) (RecordingCoverUpload, error) {
	return c.liveAutoCover(ctx, scope, id, "/auto")
}

func (c *Client) RefreshLiveAutoCover(ctx context.Context, scope, id string) (RecordingCoverUpload, error) {
	return c.liveAutoCover(ctx, scope, id, "/refresh-auto")
}

func (c *Client) liveAutoCover(ctx context.Context, scope, id, endpoint string) (RecordingCoverUpload, error) {
	if scope == "defaults" {
		return RecordingCoverUpload{}, ErrInvalid
	}
	path, err := liveCoverPath(scope, id, endpoint)
	if err != nil {
		return RecordingCoverUpload{}, err
	}
	var v RecordingCoverUpload
	if err = c.coverRequest(ctx, "POST", path, nil, &v); err != nil {
		return v, err
	}
	if !RecordingCoverID.MatchString(v.UploadID) {
		return v, ErrUnavailable
	}
	return v, nil
}
func (c *Client) RetainLiveCover(ctx context.Context, scope, id, upload, reference string) error {
	if !RecordingCoverID.MatchString(upload) || !RecordingCoverID.MatchString(reference) {
		return ErrInvalid
	}
	path, err := liveCoverPath(scope, id, "/uploads/"+upload+"/retain")
	if err != nil {
		return err
	}
	return c.coverRequest(ctx, "POST", path, map[string]string{"referenceId": reference}, nil)
}
func (c *Client) ReleaseLiveCover(ctx context.Context, scope, id, upload, reference string) error {
	if !RecordingCoverID.MatchString(upload) || !RecordingCoverID.MatchString(reference) {
		return ErrInvalid
	}
	path, err := liveCoverPath(scope, id, "/uploads/"+upload+"/references/"+reference)
	if err != nil {
		return err
	}
	err = c.coverRequest(ctx, "DELETE", path, nil, nil)
	if err == ErrNotFound {
		return nil
	}
	return err
}
func (c *Client) ReadLiveCover(ctx context.Context, scope, id, upload string) ([]byte, error) {
	if !RecordingCoverID.MatchString(upload) {
		return nil, ErrNotFound
	}
	path, err := liveCoverPath(scope, id, "/uploads/"+upload+"/content")
	if err != nil {
		return nil, err
	}
	return c.readCoverBytes(ctx, path)
}

type LiveCoverPromotion struct {
	CoverID string `json:"coverId"`
	State   string `json:"state"`
}

func (c *Client) PromoteLiveCover(ctx context.Context, scope, id, upload string) (LiveCoverPromotion, error) {
	if scope == "defaults" || !RecordingCoverID.MatchString(upload) {
		return LiveCoverPromotion{}, ErrInvalid
	}
	path, err := liveCoverPath(scope, id, "/uploads/"+upload+"/promote")
	if err != nil {
		return LiveCoverPromotion{}, err
	}
	var v LiveCoverPromotion
	if err = c.coverRequest(ctx, "POST", path, nil, &v); err != nil {
		return v, err
	}
	if !RecordingCoverID.MatchString(v.CoverID) {
		return v, ErrUnavailable
	}
	return v, nil
}
func (c *Client) PromoteBroadcastLiveCover(ctx context.Context, scope, owner, upload, capture, recording string) (LiveCoverPromotion, error) {
	var out LiveCoverPromotion
	path, e := liveCoverPath(scope, owner, "/uploads/"+upload+"/promote")
	if e != nil {
		return out, e
	}
	e = c.coverRequest(ctx, "POST", path, map[string]string{"targetCaptureId": capture, "recordingId": recording}, &out)
	if e == nil && !RecordingCoverID.MatchString(out.CoverID) {
		return out, ErrUnavailable
	}
	return out, e
}
