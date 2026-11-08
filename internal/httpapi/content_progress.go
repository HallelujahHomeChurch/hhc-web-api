package httpapi

import (
	"context"
	"sort"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

const operationProgressRetryAfterMS = 2000

func (h *Handler) contentOperationProgress(ctx context.Context, item content.Item, now time.Time) *platform.OperationProgress {
	if item.Module != content.ModuleNews || (item.Status != content.StatusPublishing && item.Status != content.StatusPublished && item.Status != content.StatusPublishFailed) {
		return nil
	}
	progress := &platform.OperationProgress{Status: "running", Stage: "queued", UpdatedAt: now, RetryAfterMS: operationProgressRetryAfterMS}
	if item.Status == content.StatusPublished {
		progress.Status, progress.Stage, progress.Percent = "ready", "published", 100
		return progress
	}

	ids := publicationAssetIDs(item)
	assets, ok := h.publicationAssets(ctx, ids)
	if !ok {
		if item.Status == content.StatusPublishFailed {
			progress.Status, progress.Stage, progress.ErrorCode = "failed", "failed", "publication_failed"
		}
		return progress
	}

	cleanScans, readyDerivatives, requiredDerivatives := 0, 0, 0
	failed := item.Status == content.StatusPublishFailed
	for _, asset := range assets {
		if asset.ScanStatus == "clean" {
			cleanScans++
		}
		if asset.ProcessingStatus != "not_required" {
			requiredDerivatives++
			if asset.ProcessingStatus == "ready" {
				readyDerivatives++
			}
		}
		if asset.UploadStatus != "completed" || (asset.ScanStatus != "pending" && asset.ScanStatus != "clean") || (asset.ProcessingStatus != "pending" && asset.ProcessingStatus != "ready" && asset.ProcessingStatus != "not_required") {
			failed = true
		}
	}

	totalUnits := len(assets) + requiredDerivatives + 1
	progress.Percent = (cleanScans + readyDerivatives) * 100 / totalUnits
	if progress.Percent > 99 {
		progress.Percent = 99
	}
	if failed {
		progress.Status, progress.Stage, progress.ErrorCode = "failed", "failed", "publication_failed"
		return progress
	}
	if cleanScans < len(assets) {
		progress.Stage = "security_check"
		progress.Completed, progress.Total = integerPointer(cleanScans), integerPointer(len(assets))
		return progress
	}
	if readyDerivatives < requiredDerivatives {
		progress.Stage = "image_processing"
		progress.Completed, progress.Total = integerPointer(readyDerivatives), integerPointer(requiredDerivatives)
		return progress
	}
	progress.Stage = "publishing"
	return progress
}

func publicationAssetIDs(item content.Item) []string {
	seen := map[string]bool{}
	for _, id := range []string{item.CoverAssetID, item.HomeCoverAssetID} {
		if id != "" {
			seen[id] = true
		}
	}
	if item.Kind == content.KindStatement {
		if references, err := content.StatementImageRefs(item.Translations); err == nil {
			for _, reference := range references {
				if reference.AssetID != "" {
					seen[reference.AssetID] = true
				}
			}
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (h *Handler) publicationAssets(ctx context.Context, ids []string) ([]assetclient.Asset, bool) {
	if len(ids) == 0 {
		return nil, true
	}
	if h.uploads == nil {
		return nil, false
	}
	type result struct {
		asset assetclient.Asset
		err   error
	}
	semaphore := make(chan struct{}, 4)
	results := make(chan result, len(ids))
	for _, id := range ids {
		go func() {
			select {
			case semaphore <- struct{}{}:
				defer func() { <-semaphore }()
			case <-ctx.Done():
				results <- result{err: ctx.Err()}
				return
			}
			asset, err := h.uploads.Get(ctx, id)
			results <- result{asset: asset, err: err}
		}()
	}
	assets := make([]assetclient.Asset, 0, len(ids))
	for range ids {
		value := <-results
		if value.err != nil {
			return nil, false
		}
		assets = append(assets, value.asset)
	}
	return assets, true
}

func integerPointer(value int) *int { return &value }
