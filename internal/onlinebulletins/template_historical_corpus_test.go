package onlinebulletins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// Private originals and reports stay outside Git; failure output contains no passages.
func TestHistoricalCorpusPrivateOriginal(t *testing.T) {
	path, snapshot := os.Getenv("HHC_PRIVATE_PDF_SOURCE"), os.Getenv("HHC_PRIVATE_CANONICAL_METADATA")
	root, digest := os.Getenv("HHC_TEST_LAYOUT_RUNNER_ROOT"), os.Getenv("HHC_TEST_LAYOUT_RENDERER_DIGEST")
	version := os.Getenv("HHC_TEST_LAYOUT_RENDERER_VERSION")
	if version == "" {
		version = "v6"
	}
	if version != "v6" && version != "v7" && version != "v8" && version != "v9" && version != "v10" {
		t.Fatal("unsupported private corpus runtime")
	}
	if path == "" || snapshot == "" || root == "" || digest == "" {
		t.Skip("requires private original, metadata and isolated runtime")
	}
	var metadata CanonicalMetadata
	if json.Unmarshal([]byte(snapshot), &metadata) != nil {
		t.Fatal("invalid private metadata")
	}
	class := map[int]string{1731: "outlined", 1732: "native", 1733: "outlined", 1734: "native", 1735: "native", 1736: "native", 1737: "outlined", 1738: "native", 1739: "current", 1740: "current", 1741: "current", 1742: "current-outlined"}[metadata.IssueNumber]
	if class == "" {
		t.Fatal("missing independently reviewed source expectation")
	}
	geometry, err := ParsePDF(path)
	if err != nil {
		t.Fatal(err)
	}
	assets := func(locale string) []Asset {
		t.Helper()
		manifest := filepath.Join(root, "packages/ui/src/bulletin-reader/template-assets.json")
		pin := "209d7746450558c26ea08d47e0c1f35c55472c572c1b15139ce2b3b1f326153b"
		if version == "v7" || version == "v8" || (version == "v9" || version == "v10") {
			manifest = filepath.Join(root, "packages/ui/src/bulletin-reader/v7/template-assets.json")
			pin = "983f2545fd63ad9afe04c9cde5c3456c76934d2aa08a81050595e8f909f6120f"
		}
		traditional, traditionalPin := manifest, pin
		if locale == "zh-Hans" {
			manifest = filepath.Join(root, "packages/ui/src/bulletin-reader/v2/template-assets.json")
			pin = "27382983b5b4e1025030048f15b81f5f22514b0d4ada267d5be5145530f095e5"
		}
		value, err := LoadBundledTemplateAssets(manifest, pin)
		if err != nil {
			t.Fatal(err)
		}
		if (version == "v7" || version == "v8" || (version == "v9" || version == "v10")) && locale == "zh-Hans" {
			fallback, err := LoadBundledTemplateAssets(traditional, traditionalPin)
			if err != nil {
				t.Fatal(err)
			}
			for _, asset := range fallback {
				if asset.FontRole == "body" || asset.FontRole == "emphasis" {
					value = append(value, asset)
				}
			}
		}
		return value
	}
	canonical := candidateIssue()
	canonical.Metadata = metadata
	manifest := LayoutManifest{TemplateVersion: "v1", RendererVersion: version, RendererArtifactSha256: digest, Assets: assets("zh-Hant")}
	var candidate DocumentCandidate
	var issues []ReviewIssue
	if version == "v9" || version == "v10" {
		var settings TemplateSettings
		if raw := os.Getenv("HHC_PRIVATE_TEMPLATE_SNAPSHOT"); raw == "" || json.Unmarshal([]byte(raw), &settings) != nil {
			t.Fatal("explicit private template snapshot required")
		}
		if version == "v10" {
			candidate, issues, err = ApplyTemplateV10(geometry, canonical, manifest, settings)
		} else {
			candidate, issues, err = ApplyTemplateV9(geometry, canonical, manifest, settings)
		}
	} else {
		candidate, issues, err = ApplyTemplateV1(geometry, canonical, manifest)
	}
	if err != nil || candidate.Document == nil {
		t.Fatal("candidate unavailable", err)
	}
	source := *candidate.Document
	counts := map[string]int{}
	for _, issue := range issues {
		counts[issue.Code]++
	}
	encoded, _ := json.Marshal(counts)
	t.Logf("issue=%d class=%s source=%s pages=%d components=%d unresolved=%s", metadata.IssueNumber, class, source.SourceAssetChecksum, len(source.Pages), len(source.Components), encoded)
	if counts["unmapped_body_header"] > 0 {
		t.Error("source first-body header is incomplete")
	}
	expected := []string{"logo", "masthead", "title", "titleLabel", "date", "issueNumber", "pastor", "welcomeLabel", "worshipLabel", "workLabel", "wordLabel", "verseLabel"}
	if metadata.Subtitle != "" {
		expected = append(expected, "subtitle")
	}
	if class == "current" || class == "current-outlined" {
		expected = append(expected, "visionMission", "visionFellowship", "visionCommitment")
	} else {
		goal := "historicalGoals"
		if metadata.IssueNumber == 1738 {
			goal = "historicalGospelGoals"
		}
		expected = append(expected, "historicalVision", goal, "historicalActions", "historicalCommitment")
	}
	fixed := map[string]int{}
	for _, slot := range source.LayoutManifest.Pages[0].FixedSlots {
		fixed[slot.Element]++
	}
	for _, element := range expected {
		if fixed[element] != 1 {
			t.Errorf("cover element %s count=%d", element, fixed[element])
		}
	}
	bodyHeader := map[string]int{}
	for _, slot := range source.LayoutManifest.Pages[1].FixedSlots {
		bodyHeader[slot.Element]++
	}
	if bodyHeader["title"] != 1 || metadata.Subtitle != "" && bodyHeader["subtitle"] != 1 {
		t.Error("first article page lost its canonical title or subtitle")
	}
	current := (version == "v8" || (version == "v9" || version == "v10")) && metadata.IssueNumber >= 1739 && metadata.IssueNumber <= 1741
	if current {
		for _, code := range []string{"inferred_body_art_geometry", "unknown_source_image", "unmapped_back_decoration", "unmapped_fixed_template"} {
			if counts[code] != 0 {
				t.Errorf("verified current template still has %s count=%d", code, counts[code])
			}
		}
		if len(source.Pages) != map[int]int{1739: 12, 1740: 16, 1741: 24}[metadata.IssueNumber] || bodyHeader["topRule"] != 1 {
			t.Error("current source page identity/divider changed")
		}
		body, err := bodyComponents(candidate.Evidence.Pages[1:2])
		if err != nil || source.LayoutManifest.Pages[1].Slots[0].Box.Y != body.slots[0].Box.Y {
			t.Error("first-body blank band changed", err)
		}
		columns := [2]int{}
		for _, component := range source.Components {
			if component.BodySection != nil && component.BodySection.Header != nil {
				t.Error("excluded production metadata returned")
			}
			if component.HymnLyrics == nil {
				continue
			}
			if n := len(component.HymnLyrics.Hymns); n < 3 || n > 5 {
				t.Error("current hymn count outside approved profile")
			}
			for _, hymn := range component.HymnLyrics.Hymns {
				for _, slot := range source.LayoutManifest.Pages[len(source.Pages)-2].Slots {
					if slot.BlockID != hymn.Title.ID {
						continue
					}
					column := 0
					if slot.Box.X >= .5 {
						column = 1
					}
					columns[column]++
				}
			}
		}
		if columns[0] < 1 || columns[0] > 3 || columns[1] < 1 || columns[1] > 3 {
			t.Errorf("hymn title columns changed: %v", columns)
		}
		back := map[string]Box{}
		for _, slot := range source.LayoutManifest.Pages[len(source.Pages)-1].FixedSlots {
			back[slot.Element] = slot.Box
		}
		if back["summaryFrame"].X <= back["summarySidebarTitle"].X+back["summarySidebarTitle"].Width || back["summaryFrame"].Y+back["summaryFrame"].Height >= back["announcementsFrame"].Y || back["announcementsFrame"].Y+back["announcementsFrame"].Height >= back["prayersFrame"].Y {
			t.Error("back panels overlap or summary includes the vertical tagline")
		}
	}
	for _, component := range source.Components {
		if component.Cover == nil {
			continue
		}
		if len(component.Cover.WeeklyVerses) == 0 {
			t.Error("weekly scripture missing")
		}
		for _, block := range component.Cover.WeeklyVerses {
			for _, sentence := range block.Sentences {
				for _, span := range sentence.Spans {
					if span.FontRole != "scripture" {
						t.Error("weekly verse lost scripture typography")
					}
				}
			}
		}
	}
	runner := LayoutRunner{NodePath: "/usr/local/bin/node", ScriptPath: filepath.Join(root, "scripts/measure-bulletin-layout-"+version+".mjs"), AssetsDirectory: filepath.Join(root, "assets"), RendererArtifactSHA256: digest}
	converter := SimplifiedRunner{NodePath: "/usr/local/bin/node", ScriptPath: "/opt/bulletin-conversion/convert.mjs"}
	before, _ := json.Marshal(source)
	for _, locale := range []string{"zh-Hant", "zh-Hans"} {
		t.Run(locale, func(t *testing.T) {
			document, canonical := source, metadata
			if locale == "zh-Hans" {
				document, err = converter.ConvertSimplified(context.Background(), source)
				if err != nil {
					t.Fatal("OpenCC source conversion failed", err)
				}
				canonical, err = converter.ConvertMetadata(context.Background(), metadata)
				if err != nil {
					t.Fatal("OpenCC metadata conversion failed", err)
				}
				document.TemplateVersion, document.LayoutManifest.TemplateVersion = "v2", "v2"
				document.LayoutManifest.Assets = assets(locale)
			}
			if directory := os.Getenv("HHC_PRIVATE_LAYOUT_REPORT_DIR"); directory != "" {
				submission, err := json.Marshal(layoutSubmission{document, canonical})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(directory, fmt.Sprintf("%d-%s.json", metadata.IssueNumber, locale)), submission, 0600); err != nil {
					t.Fatal(err)
				}
			}
			result, err := runner.Compose(context.Background(), document, canonical)
			if err != nil {
				t.Fatal("layout or semantic conservation failed", err)
			}
			if _, err = VerifyComposedLayout(result, document, canonical); err != nil {
				t.Fatal("persistence proof failed", err)
			}
			if !reflect.DeepEqual(result.Document.Pages, source.Pages) {
				t.Fatal("source-page identity changed")
			}
			if current && math.Abs(result.Document.LayoutManifest.Pages[1].Slots[0].Box.Y-source.LayoutManifest.Pages[1].Slots[0].Box.Y) > 1e-9 {
				t.Error("composition pulled first-body content into the omitted production band")
			}
			semantic := sha256.Sum256(comparisonJSON(result.Document.Components))
			t.Logf("issue=%d locale=%s semantic=%s content=%s layout=%s overflow=0", metadata.IssueNumber, locale, hex.EncodeToString(semantic[:]), result.ContentHash, result.LayoutValidationHash)
		})
	}
	after, _ := json.Marshal(source)
	if string(before) != string(after) {
		t.Fatal("conversion or composition mutated the source")
	}
}
