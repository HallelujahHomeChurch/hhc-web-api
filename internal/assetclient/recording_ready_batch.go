package assetclient

import (
	"context"
	"net/http"
	"strings"
)

type RecordingReadyResult struct {
	RecordingLifecycleBinding
	Status  string                  `json:"status"`
	Package *RecordingPackageStatus `json:"package,omitempty"`
}

func (c *Client) GetReadyRecordingPackages(ctx context.Context, items []RecordingLifecycleBinding) ([]RecordingReadyResult, error) {
	if len(items) < 1 || len(items) > 100 {
		return nil, ErrInvalid
	}
	seen := make(map[RecordingLifecycleBinding]bool, len(items))
	for _, item := range items {
		if item.RecordingID == "" || len(item.RecordingID) > 128 || strings.TrimSpace(item.RecordingID) != item.RecordingID || !recordingPackageID.MatchString(item.PackageID) || seen[item] {
			return nil, ErrInvalid
		}
		seen[item] = true
	}
	var response struct {
		Items []RecordingReadyResult `json:"items"`
	}
	err := c.request(ctx, http.MethodPost, "/priv/recording-packages/ready-batch", struct {
		Items []RecordingLifecycleBinding `json:"items"`
	}{items}, "", &response)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	// Whole-route errors must never become the old per-item skippable denials.
	if err != nil || len(response.Items) != len(items) {
		return nil, ErrUnavailable
	}
	for i, item := range response.Items {
		if item.RecordingLifecycleBinding != items[i] {
			return nil, ErrUnavailable
		}
		switch item.Status {
		case "ready":
			p := item.Package
			if p == nil || p.PackageID != item.PackageID || p.SessionID != item.PackageID || p.RecordingID != item.RecordingID || p.State != "ready" || p.ReadyAt == nil || p.MediaExpiresAt == nil {
				return nil, ErrUnavailable
			}
		case "not_found", "forbidden":
			if item.Package != nil {
				return nil, ErrUnavailable
			}
		default:
			return nil, ErrUnavailable
		}
	}
	return response.Items, nil
}
