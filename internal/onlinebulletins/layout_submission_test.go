package onlinebulletins

import (
	"bytes"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func submissionFixture(version int, locale string, proof bool) (Document, CanonicalMetadata) {
	d := Document{IssueID: "fixture-1739", Series: "general", ContentLocale: locale, SchemaVersion: "v1", TemplateVersion: "weekly-v1", SourceAssetChecksum: strings.Repeat("a", 64), SourcePageCount: 1,
		Pages:          []Page{{ID: "page-1", Width: 595.28, Height: 841.89}},
		LayoutManifest: LayoutManifest{TemplateVersion: "weekly-v1", RendererVersion: fmt.Sprintf("v%d", version), RendererArtifactSha256: strings.Repeat("b", 64)},
		Components:     []Component{{ID: "announcements", Type: "announcements"}, {ID: "body", Type: "bodySection", BodySection: &BodySection{Kind: "article", Title: Block{ID: "title", Sentences: []Sentence{{ID: "sentence", Spans: []Span{{Text: "信仰与愛 😀 <b>&\"\u2028\u2029", FontRole: "body"}}}}}}}},
	}
	if version%2 == 0 {
		d.LayoutManifest.Assets = []Asset{}
		d.LayoutManifest.Pages = []LayoutPage{}
	}
	if version >= 9 {
		d.TemplateSnapshot = &TemplateSettings{Version: 7, TemplateText: TemplateText{VisionMission: "福音與爱 😀", VisionFellowship: "<>&", VisionCommitment: "同行"}}
	}
	if proof {
		d.LayoutManifest.ContentHash = strings.Repeat("c", 64)
		d.LayoutManifest.LayoutValidationHash = strings.Repeat("d", 64)
	}
	return d, CanonicalMetadata{Title: "週報与信仰 😀 <>&", Subtitle: "\"副題\"\n\u2028\u2029", IssueNumber: 1739, Date: "2026-10-13"}
}

func TestMarshalLayoutSubmissionMatchesFrozenBytes(t *testing.T) {
	for v := 1; v <= 10; v++ {
		for _, locale := range []string{"zh-Hant", "zh-Hans"} {
			for _, proof := range []bool{false, true} {
				name := fmt.Sprintf("v%d-%s-proof-%t", v, locale, proof)
				t.Run(name, func(t *testing.T) {
					d, m := submissionFixture(v, locale, proof)
					got, hash, err := MarshalLayoutSubmission(d, m)
					if err != nil {
						t.Fatal(err)
					}
					want, err := os.ReadFile(filepath.Join("testdata", "layout-submission", name+".json"))
					if err != nil {
						t.Fatal(err)
					}
					digest, err := os.ReadFile(filepath.Join("testdata", "layout-submission", name+".sha256"))
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(got, want) || hash != strings.TrimSpace(string(digest)) {
						t.Fatal("frozen submission bytes/hash changed")
					}
				})
			}
		}
	}
}

func TestMarshalLayoutSubmissionDoesNotMutateDocument(t *testing.T) {
	d, m := submissionFixture(10, "zh-Hant", true)
	before, _ := submissionFixture(10, "zh-Hant", true)
	if _, _, err := MarshalLayoutSubmission(d, m); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(d, before) {
		t.Fatal("serializer mutated input")
	}
}

func TestMarshalLayoutSubmissionInvalidNumber(t *testing.T) {
	for _, number := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		d, m := submissionFixture(1, "zh-Hant", false)
		d.Pages[0].Width = number
		raw, hash, err := MarshalLayoutSubmission(d, m)
		if err == nil || raw != nil || hash != "" {
			t.Fatalf("invalid number returned %q %q %v", raw, hash, err)
		}
	}
}

func TestVerifyComposedLayoutMatchesFrozenSubmission(t *testing.T) {
	d, m, _ := layoutRunnerFixture(t)
	raw, err := os.ReadFile("testdata/layout-submission/checked-output.json")
	if err != nil {
		t.Fatal(err)
	}
	hash, err := os.ReadFile("testdata/layout-submission/checked-output.sha256")
	if err != nil {
		t.Fatal(err)
	}
	result, err := verifyLayoutResult(layoutRunnerOutput(t, d, raw, nil), d, m, true)
	if err != nil {
		t.Fatal(err)
	}
	checked, err := VerifyComposedLayout(result, d, m)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(checked.SubmissionJSON, raw) || checked.ContentHash != strings.TrimSpace(string(hash)) {
		t.Fatal("checked output changed frozen bytes/hash")
	}
}
