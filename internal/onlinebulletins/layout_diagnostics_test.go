package onlinebulletins

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestLayoutFailureLocationsAreBoundToOriginalInput(t *testing.T) {
	d, _, _ := layoutRunnerFixture(t)
	page := d.LayoutManifest.Pages[1]
	slot := page.Slots[0]
	hash := strings.Repeat("a", 64)
	for _, tc := range []struct {
		name, content, renderer, pageID, slotID string
		valid                                   bool
	}{
		{"located", hash, d.LayoutManifest.RendererArtifactSha256, page.PageID, slot.ID, true},
		{"stale", strings.Repeat("b", 64), d.LayoutManifest.RendererArtifactSha256, page.PageID, slot.ID, false},
		{"renderer", hash, strings.Repeat("e", 64), page.PageID, slot.ID, false},
		{"wrong page", hash, d.LayoutManifest.RendererArtifactSha256, d.LayoutManifest.Pages[0].PageID, slot.ID, false},
		{"unknown slot", hash, d.LayoutManifest.RendererArtifactSha256, page.PageID, "missing", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"failure": map[string]any{"contentHash": tc.content, "rendererArtifactSha256": tc.renderer, "locations": []any{map[string]string{"pageId": tc.pageID, "slotId": tc.slotID}}}})
			err := layoutFailure(raw, d, hash)
			var located *LayoutReviewError
			if errors.As(err, &located) != tc.valid || !errors.Is(err, ErrLayoutRunner) {
				t.Fatalf("unexpected error: %v", err)
			}
			if tc.valid {
				want := []ReviewIssue{{Code: "layout_validation_failed", ComponentID: slot.ComponentID, BlockID: slot.BlockID, SentenceID: slot.Fragments[0].SentenceID, SourcePage: 2, Blocking: true}}
				if !reflect.DeepEqual(located.Issues, want) {
					t.Fatalf("got %+v", located.Issues)
				}
			}
		})
	}
	for _, raw := range []string{`{"failure":{"locations":[]}}`, `{"failure":{"privateText":"secret"}}`, `not json`} {
		if err := layoutFailure([]byte(raw), d, hash); err != ErrLayoutRunner {
			t.Fatalf("unexpected diagnostic: %v", err)
		}
	}
	raw, _ := json.Marshal(map[string]any{"failure": map[string]any{"contentHash": hash, "rendererArtifactSha256": d.LayoutManifest.RendererArtifactSha256, "locations": []any{map[string]string{"pageId": page.PageID}}}})
	var located *LayoutReviewError
	if !errors.As(layoutFailure(raw, d, hash), &located) || len(located.Issues) != 1 || located.Issues[0].SourcePage != 2 || located.Issues[0].BlockID != "" {
		t.Fatal("page-only evidence lost or invented a block")
	}
}

func TestLayoutFailurePreservesFixedElement(t *testing.T) {
	d, _, _ := layoutRunnerFixture(t)
	page := &d.LayoutManifest.Pages[0]
	page.FixedSlots = append(page.FixedSlots, FixedSlot{ID: "fixed-cover-visionMission", Element: "visionMission"})
	hash := strings.Repeat("a", 64)
	raw, _ := json.Marshal(map[string]any{"failure": map[string]any{"contentHash": hash, "rendererArtifactSha256": d.LayoutManifest.RendererArtifactSha256, "locations": []any{map[string]string{"pageId": page.PageID, "slotId": "fixed-cover-visionMission"}}}})
	var located *LayoutReviewError
	if !errors.As(layoutFailure(raw, d, hash), &located) || len(located.Issues) != 1 || located.Issues[0].FixedElement != "visionMission" || located.Issues[0].SourcePage != 1 {
		t.Fatal("fixed location lost")
	}
	if ValidateReview(located.Issues, located.Issues) == nil {
		t.Fatal("fixed layout failure waived")
	}
}
