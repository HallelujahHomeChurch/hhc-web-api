package assetclient

import (
	"context"
	"net/http"
	"net/url"
)

func (c *Client) DeleteRecordingMedia(ctx context.Context, id string) error {
	return c.request(ctx, http.MethodDelete, "/priv/recordings/"+url.PathEscape(id), nil, "", nil)
}
