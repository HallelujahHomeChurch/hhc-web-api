package assetclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image/jpeg"
	"io"
	"mime"
	"net/http"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

// ValidateHomeBanner binds the product dimensions to the immutable scanned bytes.
func (c *Client) ValidateHomeBanner(ctx context.Context, id, checksum string) (err error) {
	digest, err := hex.DecodeString(checksum)
	if c.caller != "hhc-web-api" || id == "" || err != nil || len(digest) != sha256.Size {
		return ErrInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	expires := time.Now().UTC().Add(time.Minute)
	grant, err := c.createServiceGrant(ctx, id, "home-validate:"+platform.NewID(), &expires)
	if err != nil {
		return err
	}
	if grant.ID == "" {
		return ErrUnavailable
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer stop()
		if revokeErr := c.RevokeGrant(cleanup, id, grant.ID); revokeErr != nil && !errors.Is(revokeErr, ErrNotFound) {
			err = errors.Join(err, revokeErr)
		}
	}()
	if grant.AssetID != id {
		return ErrUnavailable
	}
	response, err := c.AuthorizedDownload(ctx, http.MethodGet, id, "", "")
	if err != nil {
		return err
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		return ErrForbidden
	case http.StatusNotFound:
		return ErrNotFound
	default:
		return ErrUnavailable
	}
	contentType, _, parseErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if parseErr != nil || contentType != "image/jpeg" || response.ContentLength > 10<<20 {
		return ErrInvalid
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (10<<20)+1))
	if err != nil {
		return ErrUnavailable
	}
	actual := sha256.Sum256(data)
	if len(data) > 10<<20 || !bytes.Equal(actual[:], digest) {
		return ErrInvalid
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width != 1900 || config.Height != 700 {
		return ErrInvalid
	}
	return ctx.Err()
}
