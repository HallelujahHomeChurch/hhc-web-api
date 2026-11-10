package assetclient

import (
	"context"
	"net/http"
	"net/url"
)

// Owner reads retain the CMS issue/locale/purpose gate; they never replace user grants.
func (c *Client) OwnerContent(ctx context.Context, source Asset) (*http.Response, error) {
	if !c.ownerContent || c.serviceSubject != "hhc-web-api" || source.ID == "" || source.OwnerID == "" || source.OwnerType != "bulletin_issue" || source.OwnerService != "hhc-web-api" || source.Visibility != "private" || len(source.ChecksumSHA256) != 64 {
		return nil, ErrInvalid
	}
	if source.Locale != "zh-Hant" && source.Locale != "zh-Hans" && source.Locale != "en" {
		return nil, ErrInvalid
	}
	if !((source.Namespace == "cms.weekly.pdf" && source.Purpose == "weekly_bulletin") || (source.Namespace == "cms.weekly.image" && source.Purpose == "online_bulletin_illustration")) {
		return nil, ErrInvalid
	}
	actual, err := c.Get(ctx, source.ID)
	if err != nil {
		return nil, err
	}
	if actual.ID != source.ID || actual.OwnerID != source.OwnerID || actual.OwnerType != source.OwnerType || actual.OwnerService != source.OwnerService || actual.Namespace != source.Namespace || actual.Purpose != source.Purpose || actual.Locale != source.Locale || actual.Visibility != "private" || actual.ChecksumSHA256 != source.ChecksumSHA256 || actual.UploadStatus != "completed" || !actual.Scanned() || (actual.ProcessingStatus != "ready" && actual.ProcessingStatus != "not_required") {
		return nil, ErrInvalid
	}
	query := url.Values{"sha256": {source.ChecksumSHA256}, "ownerType": {source.OwnerType}, "ownerId": {source.OwnerID}}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/priv/assets/"+url.PathEscape(source.ID)+"/owner-content?"+query.Encode(), nil)
	if err != nil {
		return nil, ErrInvalid
	}
	response, err := c.http.Do(request)
	if err != nil {
		return nil, ErrUnavailable
	}
	return response, nil
}
