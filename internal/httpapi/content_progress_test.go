package httpapi

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/platform"
)

func TestContentOperationProgress(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		item   content.Item
		assets map[string]assetclient.Asset
		want   *platform.OperationProgress
	}{
		{
			name:   "duplicate references count once",
			item:   content.Item{Module: content.ModuleNews, Kind: content.KindStatement, Status: content.StatusPublishing, CoverAssetID: "asset-1", HomeCoverAssetID: "asset-1", Translations: []content.Translation{{Locale: "zh-Hant", BodyJSON: statementImageBody("asset-1")}}},
			assets: map[string]assetclient.Asset{"asset-1": progressAsset("asset-1", "clean", "ready")},
			want:   &platform.OperationProgress{Status: "running", Stage: "publishing", Percent: 66, UpdatedAt: now, RetryAfterMS: 2000},
		},
		{
			name: "pending scans report security count",
			item: content.Item{Module: content.ModuleNews, Status: content.StatusPublishing, CoverAssetID: "asset-1", HomeCoverAssetID: "asset-2", Kind: content.KindStatement, Translations: []content.Translation{{Locale: "zh-Hant", BodyJSON: statementImageBody("asset-3")}}},
			assets: map[string]assetclient.Asset{
				"asset-1": progressAsset("asset-1", "clean", "ready"),
				"asset-2": progressAsset("asset-2", "clean", "pending"),
				"asset-3": progressAsset("asset-3", "pending", "pending"),
			},
			want: progressWithCount("running", "security_check", 42, 2, 3, now),
		},
		{
			name: "clean scans pending derivatives report image count",
			item: content.Item{Module: content.ModuleNews, Status: content.StatusPublishing, CoverAssetID: "asset-1", HomeCoverAssetID: "asset-2", Kind: content.KindStatement, Translations: []content.Translation{{Locale: "zh-Hant", BodyJSON: statementImageBody("asset-3")}}},
			assets: map[string]assetclient.Asset{
				"asset-1": progressAsset("asset-1", "clean", "ready"),
				"asset-2": progressAsset("asset-2", "clean", "pending"),
				"asset-3": progressAsset("asset-3", "clean", "pending"),
			},
			want: progressWithCount("running", "image_processing", 57, 1, 3, now),
		},
		{
			name:   "all asset work remains below one hundred before commit",
			item:   content.Item{Module: content.ModuleNews, Status: content.StatusPublishing, CoverAssetID: "asset-1"},
			assets: map[string]assetclient.Asset{"asset-1": progressAsset("asset-1", "clean", "ready")},
			want:   &platform.OperationProgress{Status: "running", Stage: "publishing", Percent: 66, UpdatedAt: now, RetryAfterMS: 2000},
		},
		{
			name:   "not required processing adds no derivative unit",
			item:   content.Item{Module: content.ModuleNews, Status: content.StatusPublishing, CoverAssetID: "asset-1"},
			assets: map[string]assetclient.Asset{"asset-1": progressAsset("asset-1", "clean", "not_required")},
			want:   &platform.OperationProgress{Status: "running", Stage: "publishing", Percent: 50, UpdatedAt: now, RetryAfterMS: 2000},
		},
		{
			name:   "published is exactly one hundred",
			item:   content.Item{Module: content.ModuleNews, Status: content.StatusPublished, CoverAssetID: "asset-1"},
			assets: map[string]assetclient.Asset{"asset-1": progressAsset("asset-1", "pending", "pending")},
			want:   &platform.OperationProgress{Status: "ready", Stage: "published", Percent: 100, UpdatedAt: now, RetryAfterMS: 2000},
		},
		{
			name:   "terminal asset failure is safe and stable",
			item:   content.Item{Module: content.ModuleNews, Status: content.StatusPublishing, CoverAssetID: "asset-1"},
			assets: map[string]assetclient.Asset{"asset-1": progressAsset("asset-1", "infected", "pending")},
			want:   &platform.OperationProgress{Status: "failed", Stage: "failed", Percent: 0, ErrorCode: "publication_failed", UpdatedAt: now, RetryAfterMS: 2000},
		},
		{
			name:   "publication failure uses a non sensitive code",
			item:   content.Item{Module: content.ModuleNews, Status: content.StatusPublishFailed, CoverAssetID: "asset-1"},
			assets: map[string]assetclient.Asset{"asset-1": progressAsset("asset-1", "clean", "ready")},
			want:   &platform.OperationProgress{Status: "failed", Stage: "failed", Percent: 66, ErrorCode: "publication_failed", UpdatedAt: now, RetryAfterMS: 2000},
		},
		{
			name: "zero referenced assets use final publication unit",
			item: content.Item{Module: content.ModuleNews, Status: content.StatusPublishing},
			want: &platform.OperationProgress{Status: "running", Stage: "publishing", Percent: 0, UpdatedAt: now, RetryAfterMS: 2000},
		},
		{
			name: "draft news omits progress",
			item: content.Item{Module: content.ModuleNews, Status: content.StatusDraft},
			want: nil,
		},
		{
			name: "other modules omit progress",
			item: content.Item{Module: content.ModuleVideos, Status: content.StatusPublished},
			want: nil,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			uploads := &contentProgressUploads{assets: test.assets}
			handler := &Handler{uploads: uploads}
			got := handler.contentOperationProgress(context.Background(), test.item, now)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("progress=%#v want=%#v", got, test.want)
			}
			if test.item.Status == content.StatusPublished && uploads.calls != 0 {
				t.Fatalf("published asset calls=%d", uploads.calls)
			}
		})
	}
}

func TestContentOperationProgressAssetLookupFailureIsConservative(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	handler := &Handler{uploads: &contentProgressUploads{err: errors.New("asset unavailable")}}
	got := handler.contentOperationProgress(context.Background(), content.Item{Module: content.ModuleNews, Status: content.StatusPublishing, CoverAssetID: "asset-1"}, now)
	want := &platform.OperationProgress{Status: "running", Stage: "queued", Percent: 0, UpdatedAt: now, RetryAfterMS: 2000}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("progress=%#v want=%#v", got, want)
	}
}

func TestContentOperationProgressIsMonotonicAcrossAssetStates(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	uploads := &contentProgressUploads{assets: map[string]assetclient.Asset{}}
	handler := &Handler{uploads: uploads}
	item := content.Item{Module: content.ModuleNews, Status: content.StatusPublishing, CoverAssetID: "asset-1"}
	states := []assetclient.Asset{
		progressAsset("asset-1", "pending", "pending"),
		progressAsset("asset-1", "clean", "pending"),
		progressAsset("asset-1", "clean", "ready"),
	}
	last := -1
	for _, asset := range states {
		uploads.assets[asset.ID] = asset
		got := handler.contentOperationProgress(context.Background(), item, now)
		if got.Percent < last || got.Percent == 100 {
			t.Fatalf("progress=%d after=%d", got.Percent, last)
		}
		last = got.Percent
	}
	item.Status = content.StatusPublished
	if got := handler.contentOperationProgress(context.Background(), item, now); got.Percent != 100 || got.Status != "ready" {
		t.Fatalf("published progress=%#v", got)
	}
}

type contentProgressUploads struct {
	apiUploads
	mu     sync.Mutex
	assets map[string]assetclient.Asset
	err    error
	calls  int
}

func (u *contentProgressUploads) Get(_ context.Context, id string) (assetclient.Asset, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls++
	if u.err != nil {
		return assetclient.Asset{}, u.err
	}
	asset, ok := u.assets[id]
	if !ok {
		return assetclient.Asset{}, fmt.Errorf("missing asset %s", id)
	}
	return asset, nil
}

func progressAsset(id, scan, processing string) assetclient.Asset {
	return assetclient.Asset{ID: id, UploadStatus: "completed", ScanStatus: scan, ProcessingStatus: processing}
}

func progressWithCount(status, stage string, percent, completed, total int, now time.Time) *platform.OperationProgress {
	return &platform.OperationProgress{Status: status, Stage: stage, Percent: percent, Completed: &completed, Total: &total, UpdatedAt: now, RetryAfterMS: 2000}
}

func statementImageBody(assetID string) []byte {
	return []byte(fmt.Sprintf(`{"schemaVersion":1,"blocks":[{"id":"image-1","type":"image","assetId":%q,"alt":{"mode":"decorative"}}]}`, assetID))
}
