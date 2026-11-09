package assetclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"image/png"
	"io"
	"mime"
	"net/http"
	"net/url"
	"time"
)

const maxBulletinIllustrationBytes = 2 << 20

func validBulletinIllustration(asset Asset, issueID, checksum string) bool {
	return asset.ID != "" && asset.Namespace == "cms.weekly.image" && asset.OwnerService == "hhc-web-api" && asset.OwnerType == "bulletin_issue" && asset.OwnerID == issueID && asset.Purpose == "online_bulletin_illustration" && asset.Locale == "zh-Hant" && asset.Visibility == "private" && asset.ChecksumSHA256 == checksum && asset.DetectedMIMEType == "image/png" && asset.UploadStatus == "completed" && asset.Scanned() && asset.ProcessingStatus == "not_required"
}

// EnsureBulletinIllustration resumes the same source-bound upload across retries.
// An expired, incomplete upload gets a stable successor; completed bytes are
// never uploaded again and the normal scan gate is never bypassed.
func (c *Client) EnsureBulletinIllustration(ctx context.Context, issueID, sourcePart string, data []byte) (Asset, bool, error) {
	if c.caller != "hhc-web-api" || issueID == "" || len(sourcePart) != 64 || len(data) == 0 || len(data) > maxBulletinIllustrationBytes {
		return Asset{}, false, ErrInvalid
	}
	if _, err := png.DecodeConfig(bytes.NewReader(data)); err != nil {
		return Asset{}, false, ErrInvalid
	}
	sum := sha256.Sum256(data)
	checksum := hex.EncodeToString(sum[:])
	identity := sha256.Sum256([]byte(issueID + ":" + sourcePart + ":" + checksum))
	key := "online-image:" + hex.EncodeToString(identity[:])
	for range 4 {
		var upload CreatedUpload
		body := map[string]any{"namespace": "cms.weekly.image", "ownerService": "hhc-web-api", "ownerType": "bulletin_issue", "ownerId": issueID, "purpose": "online_bulletin_illustration", "locale": "zh-Hant", "originalFileName": checksum + ".png", "expectedMimeType": "image/png", "maxSizeBytes": len(data), "visibility": "private"}
		if err := c.request(ctx, http.MethodPost, "/priv/assets/upload-sessions", body, key, &upload); err != nil {
			return Asset{}, false, err
		}
		if upload.Asset.ID == "" {
			return Asset{}, false, ErrUnavailable
		}
		if upload.Asset.UploadStatus != "completed" {
			if !upload.UploadTarget.ExpiresAt.After(time.Now()) {
				key = "online-image-resume:" + upload.Asset.ID + ":" + checksum
				continue
			}
			if err := c.Upload(ctx, upload.UploadTarget, bytes.NewReader(data), int64(len(data))); err != nil {
				return Asset{}, false, err
			}
			if _, err := c.CompleteUpload(ctx, upload.Asset.ID, CompleteUploadInput{SizeBytes: int64(len(data)), ChecksumSHA256: checksum, MIMEType: "image/png"}); err != nil {
				return Asset{}, false, err
			}
		}
		asset, err := c.Get(ctx, upload.Asset.ID)
		if err != nil {
			return Asset{}, false, err
		}
		if asset.Namespace != "cms.weekly.image" || asset.OwnerService != "hhc-web-api" || asset.OwnerType != "bulletin_issue" || asset.OwnerID != issueID || asset.Purpose != "online_bulletin_illustration" || asset.Locale != "zh-Hant" || asset.Visibility != "private" || asset.ChecksumSHA256 != checksum {
			return Asset{}, false, ErrInvalid
		}
		if asset.ScanStatus == "infected" || asset.ScanStatus == "failed" || asset.ProcessingStatus == "failed" {
			return Asset{}, false, ErrInvalid
		}
		return asset, validBulletinIllustration(asset, issueID, checksum), nil
	}
	return Asset{}, false, ErrConflict
}

// ReadBulletinIllustration rechecks owned, clean bytes through the private CMS
// illustration route; reads never create or revoke service grants.
func (c *Client) ReadBulletinIllustration(ctx context.Context, issueID, assetID, checksum string) (data []byte, err error) {
	if c.caller != "hhc-web-api" || issueID == "" || assetID == "" || len(checksum) != 64 {
		return nil, ErrInvalid
	}
	query := url.Values{"issueId": {issueID}, "checksumSha256": {checksum}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/priv/assets/"+url.PathEscape(assetID)+"/bulletin-illustration?"+query.Encode(), nil)
	if err != nil {
		return nil, ErrInvalid
	}
	request.Header.Set("X-Internal-Caller-App-Id", c.caller)
	var response *http.Response
	if c.ownerContent {
		response, err = c.OwnerContent(ctx, Asset{ID: assetID, OwnerID: issueID, OwnerType: "bulletin_issue", OwnerService: "hhc-web-api", Namespace: "cms.weekly.image", Purpose: "online_bulletin_illustration", Locale: "zh-Hant", Visibility: "private", ChecksumSHA256: checksum})
	} else {
		response, err = c.http.Do(request)
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	defer response.Body.Close()
	typeName, _, typeErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || typeErr != nil || typeName != "image/png" || response.ContentLength > maxBulletinIllustrationBytes {
		return nil, ErrUnavailable
	}
	data, err = io.ReadAll(io.LimitReader(response.Body, maxBulletinIllustrationBytes+1))
	if err != nil || len(data) > maxBulletinIllustrationBytes {
		return nil, ErrInvalid
	}
	sum := sha256.Sum256(data)
	config, imageErr := png.DecodeConfig(bytes.NewReader(data))
	if hex.EncodeToString(sum[:]) != checksum || imageErr != nil || config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > 4_000_000 {
		return nil, ErrInvalid
	}
	return data, nil
}
