package onlinebulletins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func layoutRunnerFixture(t *testing.T) (Document, CanonicalMetadata, []byte) {
	t.Helper()
	d := validDocument(4)
	metadata := CanonicalMetadata{Title: "主題", IssueNumber: 1739, Date: "2026-09-20"}
	submission, err := json.Marshal(struct {
		Document          Document          `json:"document"`
		CanonicalMetadata CanonicalMetadata `json:"canonicalMetadata"`
	}{d, metadata})
	if err != nil {
		t.Fatal(err)
	}
	return d, metadata, submission
}

func layoutRunnerOutput(t *testing.T, document Document, submission []byte, mutate func(map[string]any)) []byte {
	t.Helper()
	digest := sha256.Sum256(submission)
	pages := make([]map[string]any, len(document.Pages))
	for i, page := range document.Pages {
		slots := []map[string]any{}
		for _, slot := range document.LayoutManifest.Pages[i].Slots {
			slots = append(slots, map[string]any{"slotId": slot.ID})
		}
		pages[i] = map[string]any{"pageId": page.ID, "width": page.Width, "height": page.Height, "slots": slots}
	}
	measurement := map[string]any{"contentHash": hex.EncodeToString(digest[:]), "rendererArtifactSha256": document.LayoutManifest.RendererArtifactSha256, "chromiumVersion": "153.0.8010.12", "fontHashes": []string{strings.Repeat("c", 64)}, "pages": pages, "overflow": []any{}}
	if mutate != nil {
		mutate(measurement)
	}
	proof, err := json.Marshal(measurement)
	if err != nil {
		t.Fatal(err)
	}
	proofHash := sha256.Sum256(proof)
	proof = append(proof[:len(proof)-1], []byte(`,"layoutValidationHash":"`+hex.EncodeToString(proofHash[:])+`"}`)...)
	output, err := json.Marshal(struct {
		SubmissionJSON      string          `json:"submissionJSON"`
		ExpectedContentHash string          `json:"expectedContentHash"`
		Measurement         json.RawMessage `json:"measurement"`
	}{string(submission), hex.EncodeToString(digest[:]), proof})
	if err != nil {
		t.Fatal(err)
	}
	return output
}

func TestCompositionAcceptsExtractionOnlyProvenanceAfterJSONRoundTrip(t *testing.T) {
	d, metadata, submission := layoutRunnerFixture(t)
	for i := range d.LayoutManifest.Pages {
		for j := range d.LayoutManifest.Pages[i].Slots {
			d.LayoutManifest.Pages[i].Slots[j].sourcePage = i + 1
		}
	}
	if _, err := verifyLayoutResult(layoutRunnerOutput(t, d, submission, nil), d, metadata, true); err != nil {
		t.Fatal("serialized layout rejected extraction-only provenance", err)
	}
}

func TestLayoutResultRejectsStaleOrUnprovenContent(t *testing.T) {
	d, metadata, submission := layoutRunnerFixture(t)
	if _, err := verifyLayoutResult(layoutRunnerOutput(t, d, submission, func(m map[string]any) {
		page := m["pages"].([]map[string]any)[0]
		page["width"] = d.Pages[0].Width - .008
		page["height"] = d.Pages[0].Height + .008
	}), d, metadata, true); err != nil {
		t.Fatal("browser subpixel rounding rejected", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"valid", nil},
		{"content", func(m map[string]any) { m["contentHash"] = strings.Repeat("e", 64) }},
		{"renderer", func(m map[string]any) { m["rendererArtifactSha256"] = strings.Repeat("e", 64) }},
		{"browser", func(m map[string]any) { m["chromiumVersion"] = "future" }},
		{"fonts", func(m map[string]any) { m["fontHashes"] = []string{strings.Repeat("e", 64)} }},
		{"overflow", func(m map[string]any) { m["overflow"] = []any{map[string]any{"slotId": "slot"}} }},
		{"pages", func(m map[string]any) { m["pages"] = []any{} }},
		{"page width", func(m map[string]any) { m["pages"].([]map[string]any)[0]["width"] = 1 }},
		{"page height", func(m map[string]any) { m["pages"].([]map[string]any)[0]["height"] = 1 }},
		{"missing page dimension", func(m map[string]any) { delete(m["pages"].([]map[string]any)[0], "width") }},
		{"missing slots", func(m map[string]any) { m["pages"].([]map[string]any)[0]["slots"] = []any{} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, err := verifyLayoutResult(layoutRunnerOutput(t, d, submission, tc.mutate), d, metadata, true)
			if tc.mutate != nil {
				if err == nil {
					t.Fatal("accepted mismatched proof")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if result.ContentHash == "" || result.LayoutValidationHash == "" || string(result.SubmissionJSON) != string(submission) {
				t.Fatal("lost exact submission/proof")
			}
		})
	}
	changed := d
	changed.Components = append([]Component(nil), d.Components...)
	changed.Components[1].ID = "changed-anchor"
	if _, err := verifyLayoutResult(layoutRunnerOutput(t, d, submission, nil), changed, metadata, true); err == nil {
		t.Fatal("changed semantic identity accepted")
	}
	metadata.Title = "不同主題"
	if _, err := verifyLayoutResult(layoutRunnerOutput(t, d, submission, nil), d, metadata, true); err == nil {
		t.Fatal("canonical metadata changed")
	}
	broken := layoutRunnerOutput(t, d, submission, nil)
	broken = []byte(strings.Replace(string(broken), `"layoutValidationHash":"`, `"layoutValidationHash":"0`, 1))
	if _, err := verifyLayoutResult(broken, d, metadata, true); err == nil {
		t.Fatal("corrupted proof accepted")
	}
}

func TestLayoutRunnerBoundsProcessAndDoesNotLeakPrivateDiagnostics(t *testing.T) {
	d, metadata, _ := layoutRunnerFixture(t)
	for _, tc := range []struct {
		name, script string
		timeout      time.Duration
	}{
		{"failure", "echo private-member-data >&2; exit 1", time.Second},
		{"deadline", "sleep 30", 50 * time.Millisecond},
		{"oversize", "head -c 17000000 /dev/zero", time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "runner")
			if err := os.WriteFile(path, []byte("#!/bin/sh\n"+tc.script+"\n"), 0700); err != nil {
				t.Fatal(err)
			}
			runner := LayoutRunner{NodePath: path, ScriptPath: "/trusted/runner.mjs", AssetsDirectory: "/trusted/assets", RendererArtifactSHA256: d.LayoutManifest.RendererArtifactSha256}
			ctx, cancel := context.WithTimeout(context.Background(), tc.timeout)
			defer cancel()
			start := time.Now()
			_, err := runner.Compose(ctx, d, metadata)
			if !errors.Is(err, ErrLayoutRunner) || strings.Contains(err.Error(), "private") || time.Since(start) > 3*time.Second {
				t.Fatalf("unbounded or leaking failure: %v", err)
			}
		})
	}
}

func TestCompositionPreservesSourcePagesAndBoundedPresentation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		valid  bool
		change func(*Document)
	}{
		{"cover fit", true, func(d *Document) { d.Components[0].Cover.Welcome[0].Style.FontSize = 10 }},
		{"body fit", true, func(d *Document) { d.Components[1].BodySection.Blocks[0].Style.FontSize = 14 }},
		{"unreadable body", false, func(d *Document) { d.Components[1].BodySection.Blocks[0].Style.FontSize = 8 }},
		{"text", false, func(d *Document) { d.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = "修改。" }},
		{"font role", false, func(d *Document) { d.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].FontRole = "scripture" }},
		{"page size", false, func(d *Document) { d.Pages[1].Height += 1 }},
		{"extra page", false, func(d *Document) {
			d.Pages = append(d.Pages, Page{ID: "extra", Width: 595.32, Height: 841.92})
			d.LayoutManifest.Pages = append(d.LayoutManifest.Pages, LayoutPage{PageID: "extra", Slots: []Slot{}})
		}},
		{"move source fragment", false, func(d *Document) {
			slot := d.LayoutManifest.Pages[1].Slots[0]
			d.LayoutManifest.Pages[1].Slots = d.LayoutManifest.Pages[1].Slots[1:]
			d.LayoutManifest.Pages[2].Slots = append(d.LayoutManifest.Pages[2].Slots, slot)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			original, metadata, raw := layoutRunnerFixture(t)
			var changed layoutSubmission
			if err := json.Unmarshal(raw, &changed); err != nil {
				t.Fatal(err)
			}
			tc.change(&changed.Document)
			encoded, _ := json.Marshal(changed)
			_, err := verifyLayoutResult(layoutRunnerOutput(t, changed.Document, encoded, nil), original, metadata, true)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
}

func TestLayoutRunnerNativeImage(t *testing.T) {
	t.Run("v1", func(t *testing.T) { testLayoutRunnerNativeImage(t, "v1", "HHC_TEST_LAYOUT_RENDERER_DIGEST", false) })
	t.Run("v7", func(t *testing.T) { testLayoutRunnerNativeImage(t, "v7", "HHC_TEST_LAYOUT_V7_RENDERER_DIGEST", false) })
	t.Run("v8", func(t *testing.T) { testLayoutRunnerNativeImage(t, "v8", "HHC_TEST_LAYOUT_V8_RENDERER_DIGEST", false) })
	t.Run("v9", func(t *testing.T) { testLayoutRunnerNativeImage(t, "v9", "HHC_TEST_LAYOUT_V9_RENDERER_DIGEST", false) })
	t.Run("v10", func(t *testing.T) {
		testLayoutRunnerNativeImage(t, "v10", "HHC_TEST_LAYOUT_V10_RENDERER_DIGEST", false)
	})
	t.Run("v11", func(t *testing.T) {
		testLayoutRunnerNativeImage(t, "v11", "HHC_TEST_LAYOUT_V11_RENDERER_DIGEST", false)
	})
}

func TestLayoutRunnerRuntimeSmoke(t *testing.T) {
	t.Run("v1", func(t *testing.T) { testLayoutRunnerNativeImage(t, "v1", "HHC_TEST_LAYOUT_RENDERER_DIGEST", true) })
}

func testLayoutRunnerNativeImage(t *testing.T, version, digestEnv string, smoke bool) {
	root := os.Getenv("HHC_TEST_LAYOUT_RUNNER_ROOT")
	if root == "" || os.Getenv(digestEnv) == "" {
		t.Skip("requires isolated renderer image")
	}
	d, metadata, _ := layoutRunnerFixture(t)
	manifestPath := filepath.Join(root, "packages/ui/src/bulletin-reader/template-assets.json")
	scriptPath := filepath.Join(root, "scripts/measure-bulletin-layout.mjs")
	if version == "v7" || version == "v8" || (version == "v9" || version == "v10" || version == "v11") {
		manifestPath = filepath.Join(root, "packages/ui/src/bulletin-reader/v7/template-assets.json")
		scriptPath = filepath.Join(root, "scripts/measure-bulletin-layout-"+version+".mjs")
	}
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var assets []struct {
		URL, SHA256, Kind string
		Roles             []string
	}
	if err = json.Unmarshal(data, &assets); err != nil {
		t.Fatal(err)
	}
	d.LayoutManifest.Assets = nil
	for _, asset := range assets {
		value := Asset{URL: asset.URL, SHA256: asset.SHA256, Kind: asset.Kind}
		if len(asset.Roles) > 0 {
			value.FontRole = asset.Roles[0]
		}
		d.LayoutManifest.Assets = append(d.LayoutManifest.Assets, value)
	}
	d.LayoutManifest.RendererVersion = version
	d.LayoutManifest.RendererArtifactSha256 = os.Getenv(digestEnv)
	if version == "v9" || version == "v10" || version == "v11" {
		_, _, _, snapshot := v9CandidateSource()
		d.TemplateSnapshot = &snapshot
		for i, element := range []string{"visionMission", "visionFellowship", "visionCommitment"} {
			d.LayoutManifest.Pages[0].FixedSlots = append(d.LayoutManifest.Pages[0].FixedSlots, FixedSlot{ID: "snapshot-row-" + element, Element: element, Box: Box{X: .44, Y: .156 + float64(i)*.019, Width: .5, Height: .014}, Style: ParagraphStyle{FontSize: 9, LineHeight: 10}})
		}
	}
	for i := range d.LayoutManifest.Pages {
		for n := range d.LayoutManifest.Pages[i].Slots {
			d.LayoutManifest.Pages[i].Slots[n].Box.Y = 0.1 + float64(n)*0.12
		}
	}
	runner := LayoutRunner{NodePath: "/usr/local/bin/node", ScriptPath: scriptPath, AssetsDirectory: filepath.Join(root, "assets"), RendererArtifactSHA256: d.LayoutManifest.RendererArtifactSha256}
	result, err := runner.Compose(context.Background(), d, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Document.Pages) != 4 || result.ContentHash == "" || result.LayoutValidationHash == "" {
		t.Fatal("missing composed result")
	}
	canonical, _ := json.Marshal(layoutSubmission{result.Document, metadata})
	canonicalHash := sha256.Sum256(canonical)
	if result.ContentHash != hex.EncodeToString(canonicalHash[:]) {
		t.Fatal("proof does not bind server-persisted serialization")
	}
	if smoke {
		return
	}
	validated, err := runner.Validate(context.Background(), result.Document, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if validated.LayoutValidationHash == "" {
		t.Fatal("missing validation proof")
	}
	// The server serialization can differ from JS property order, but every proof
	// must bind the exact bytes supplied for that invocation.
	if _, err := ValidateDocument(result.Document); err != nil {
		t.Fatal(err)
	}
	var components []Component
	if err = json.Unmarshal(comparisonJSON(result.Document.Components), &components); err != nil {
		t.Fatal(err)
	}
	body := components[1].BodySection
	body.Blocks = append(body.Blocks, Block{ID: "added-body", Style: body.Blocks[0].Style, Sentences: []Sentence{{ID: "added-long-sentence", Spans: []Span{{Text: strings.Repeat("文字。", 600), FontRole: "body"}}}}})
	draft, err := EditComponents(result.Document, components)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Compose(context.Background(), draft, metadata); !errors.Is(err, ErrLayoutRunner) {
		t.Fatal("oversized edit must require review, not create a continuation", err)
	} else {
		var located *LayoutReviewError
		if !errors.As(err, &located) || len(located.Issues) == 0 || located.Issues[0].SourcePage != 2 {
			t.Fatalf("oversized body lost source location: %v", err)
		}
	}
	if version == "v8" || (version == "v9" || version == "v10" || version == "v11") {
		var edits []Component
		if err := json.Unmarshal(comparisonJSON(result.Document.Components), &edits); err != nil {
			t.Fatal(err)
		}
		edits[1].BodySection.Title.Sentences[0].Spans[0].Text = "第一行\n第二行"
		draft, err := EditComponents(result.Document, edits)
		if err != nil {
			t.Fatal(err)
		}
		_, err = runner.Compose(context.Background(), draft, metadata)
		var located *LayoutReviewError
		if !errors.As(err, &located) || len(located.Issues) != 1 || located.Issues[0].BlockID != edits[1].BodySection.Title.ID || located.Issues[0].SourcePage != 2 {
			t.Fatalf("missing native title diagnostic: %v", err)
		}
	}
}

func TestLayoutRunnerPrivateOriginals(t *testing.T) {
	root, inputs := os.Getenv("HHC_TEST_LAYOUT_RUNNER_ROOT"), os.Getenv("HHC_TEST_LAYOUT_SOURCE_PATHS")
	if root == "" || inputs == "" {
		t.Skip("requires explicitly supplied private originals")
	}
	digest := os.Getenv("HHC_TEST_LAYOUT_RENDERER_DIGEST")
	runner := LayoutRunner{NodePath: "/usr/local/bin/node", ScriptPath: filepath.Join(root, "scripts/measure-bulletin-layout.mjs"), AssetsDirectory: filepath.Join(root, "assets"), RendererArtifactSHA256: digest}
	for _, path := range strings.Split(inputs, ",") {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("private fixture unavailable")
		}
		var submission layoutSubmission
		if decodeLayoutJSON(data, &submission) != nil {
			t.Fatal("invalid private fixture")
		}
		submission.Document.LayoutManifest.RendererArtifactSha256 = digest
		result, err := runner.Compose(context.Background(), submission.Document, submission.CanonicalMetadata)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("issue %d: %d saved pages, verified semantic identity and zero overflow", submission.CanonicalMetadata.IssueNumber, len(result.Document.Pages))
	}
}

type layoutDeadlineObserver struct {
	context.Context
	deadlines *int
}

func (c layoutDeadlineObserver) Deadline() (time.Time, bool) {
	*c.deadlines++
	return time.Now().Add(2 * time.Minute), true
}

func TestComposeAndSavedDocumentMeasurementKeepCallerDeadline(t *testing.T) {
	document, metadata, _ := layoutRunnerFixture(t)
	submission, _, err := MarshalLayoutSubmission(document, metadata)
	if err != nil {
		t.Fatal(err)
	}
	composed := layoutRunnerOutput(t, document, submission, nil)
	var envelope struct {
		Measurement json.RawMessage `json:"measurement"`
	}
	if json.Unmarshal(composed, &envelope) != nil {
		t.Fatal("fixture")
	}
	root := t.TempDir()
	for name, body := range map[string][]byte{"composed.json": composed, "measurement.json": envelope.Measurement} {
		if err = os.WriteFile(filepath.Join(root, name), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
case "$1" in
 /opt/bulletin-layout/compose.mjs) cat "$(dirname "$0")/composed.json";;
 *) cat "$(dirname "$0")/measurement.json";;
esac
`
	path := filepath.Join(root, "node")
	if err = os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	runner := LayoutRunner{NodePath: path, ScriptPath: "/trusted/measure.mjs", AssetsDirectory: "/trusted/assets", RendererArtifactSHA256: document.LayoutManifest.RendererArtifactSha256}
	deadlines := 0
	if _, err = runner.Compose(layoutDeadlineObserver{Context: context.Background(), deadlines: &deadlines}, document, metadata); err != nil {
		t.Fatal("two phase result", err)
	}
	if deadlines != 2 {
		t.Fatalf("postserialization measurement inherited the composition-only deadline: caller deadline consulted %d times", deadlines)
	}
}
