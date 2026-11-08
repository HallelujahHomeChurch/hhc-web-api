package onlinebulletins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestSimplifiedLayoutRunnerNativeImage(t *testing.T) {
	t.Run("v9", func(t *testing.T) { testSimplifiedLayout(t, "v9", "HHC_TEST_LAYOUT_V9_RENDERER_DIGEST") })
	t.Run("v10", func(t *testing.T) { testSimplifiedLayout(t, "v10", "HHC_TEST_LAYOUT_V10_RENDERER_DIGEST") })
	t.Run("v11", func(t *testing.T) { testSimplifiedLayout(t, "v11", "HHC_TEST_LAYOUT_V11_RENDERER_DIGEST") })
	t.Run("v8", func(t *testing.T) { testSimplifiedLayout(t, "v8", "HHC_TEST_LAYOUT_V8_RENDERER_DIGEST") })
	t.Run("v7", func(t *testing.T) { testSimplifiedLayout(t, "v7", "HHC_TEST_LAYOUT_V7_RENDERER_DIGEST") })
	t.Run("v6", func(t *testing.T) { testSimplifiedLayout(t, "v6", "HHC_TEST_LAYOUT_V6_RENDERER_DIGEST") })
	t.Run("v5", func(t *testing.T) { testSimplifiedLayout(t, "v5", "HHC_TEST_LAYOUT_V5_RENDERER_DIGEST") })
	t.Run("v2", func(t *testing.T) { testSimplifiedLayout(t, "v2", "HHC_TEST_LAYOUT_V2_RENDERER_DIGEST") })
	t.Run("v3", func(t *testing.T) { testSimplifiedLayout(t, "v3", "HHC_TEST_LAYOUT_V3_RENDERER_DIGEST") })
	t.Run("v4", func(t *testing.T) { testSimplifiedLayout(t, "v4", "HHC_TEST_LAYOUT_V4_RENDERER_DIGEST") })
}

func testSimplifiedLayout(t *testing.T, version, digestEnv string) {
	root := os.Getenv("HHC_TEST_LAYOUT_RUNNER_ROOT")
	digest := os.Getenv(digestEnv)
	if root == "" || digest == "" {
		t.Skip("requires isolated renderer image")
	}
	source, metadata, _ := layoutRunnerFixture(t)
	if version == "v9" || version == "v10" || version == "v11" {
		_, _, _, snapshot := v9CandidateSource()
		source.TemplateSnapshot = &snapshot
		source.LayoutManifest.RendererVersion, source.LayoutManifest.RendererArtifactSha256 = version, digest
		for i, element := range []string{"visionMission", "visionFellowship", "visionCommitment"} {
			source.LayoutManifest.Pages[0].FixedSlots = append(source.LayoutManifest.Pages[0].FixedSlots, FixedSlot{ID: "snapshot-row-" + element, Element: element, Box: Box{X: .44, Y: .156 + float64(i)*.019, Width: .5, Height: .014}, Style: ParagraphStyle{FontSize: 9, LineHeight: 10}})
		}
	}
	converter := SimplifiedRunner{NodePath: "/usr/local/bin/node", ScriptPath: "/opt/bulletin-conversion/convert.mjs"}
	document, err := converter.ConvertSimplified(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	metadata, err = converter.ConvertMetadata(context.Background(), metadata)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "packages/ui/src/bulletin-reader/v2/template-assets.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	manifestHash := sha256.Sum256(raw)
	assets, err := LoadBundledTemplateAssets(path, hex.EncodeToString(manifestHash[:]))
	if err != nil {
		t.Fatal(err)
	}
	if version == "v7" || version == "v8" || (version == "v9" || version == "v10" || version == "v11") {
		fallback, err := LoadBundledTemplateAssets(filepath.Join(root, "packages/ui/src/bulletin-reader/v7/template-assets.json"), "983f2545fd63ad9afe04c9cde5c3456c76934d2aa08a81050595e8f909f6120f")
		if err != nil {
			t.Fatal(err)
		}
		for _, asset := range fallback {
			if asset.Kind == "font" && (asset.FontRole == "body" || asset.FontRole == "emphasis") {
				assets = append(assets, asset)
			}
		}
	}
	document.TemplateVersion = "v2"
	document.LayoutManifest.TemplateVersion = "v2"
	document.LayoutManifest.RendererVersion = version
	document.LayoutManifest.RendererArtifactSha256 = digest
	document.LayoutManifest.Assets = assets
	for i := range document.LayoutManifest.Pages {
		for j := range document.LayoutManifest.Pages[i].Slots {
			document.LayoutManifest.Pages[i].Slots[j].Box.Y = 0.1 + float64(j)*0.12
		}
	}
	runner := LayoutRunner{NodePath: "/usr/local/bin/node", ScriptPath: filepath.Join(root, "scripts/measure-bulletin-layout-"+version+".mjs"), AssetsDirectory: filepath.Join(root, "assets"), RendererArtifactSHA256: digest}
	result, err := runner.Compose(context.Background(), document, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if result.Document.ContentLocale != "zh-Hans" || len(result.Document.Pages) != len(source.Pages) || result.LayoutValidationHash == "" {
		t.Fatal("missing locale or stable page map")
	}
	if (version == "v9" || version == "v10" || version == "v11") && (result.Document.TemplateSnapshot == nil || result.Document.TemplateSnapshot.Version != source.TemplateSnapshot.Version || result.Document.TemplateSnapshot.VisionMission != "遍地华人兴起、福音传到地极") {
		t.Fatal("simplified snapshot lost version or conversion")
	}
	if _, err = VerifyComposedLayout(result, document, metadata); err != nil {
		t.Fatal("invalid stored proof", err)
	}
}
