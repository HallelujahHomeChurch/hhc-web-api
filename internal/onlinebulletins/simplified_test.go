package onlinebulletins

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func simplifiedRunner(t *testing.T) SimplifiedRunner {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs("../../tools/bulletin-conversion/convert.mjs")
	if err != nil {
		t.Fatal(err)
	}
	return SimplifiedRunner{NodePath: node, ScriptPath: script}
}

func TestConvertSimplifiedPreservesStructure(t *testing.T) {
	source := validDocument(4)
	source.LayoutManifest.ContentHash = source.SourceAssetChecksum
	source.LayoutManifest.LayoutValidationHash = source.SourceAssetChecksum
	before, _ := json.Marshal(source)
	got, err := simplifiedRunner(t).ConvertSimplified(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContentLocale != "zh-Hans" || got.Components[0].Cover.Welcome[0].Sentences[0].Spans[0].Text != "欢迎。" || got.Components[1].BodySection.Contributors[0].Name.Sentences[0].Spans[0].Text != "讲员" {
		t.Fatalf("conversion missing: %+v", got)
	}
	if got.LayoutManifest.ContentHash != "" || got.LayoutManifest.LayoutValidationHash != "" {
		t.Fatal("stale layout proof retained")
	}
	if !reflect.DeepEqual(source.Pages, got.Pages) || got.Components[0].Cover.Welcome[0].ID != "block-welcome" || got.Components[0].Cover.Welcome[0].Sentences[0].ID != "sentence-welcome" {
		t.Fatal("structure changed")
	}
	after, _ := json.Marshal(source)
	if string(before) != string(after) {
		t.Fatal("source mutated")
	}
}

func TestConvertSimplifiedSpanOffsets(t *testing.T) {
	source := validDocument(4)
	source.Components[1].BodySection.Blocks[0].Sentences[0].Spans = []Span{{Text: "乾杯", FontRole: "emphasis"}, {Text: "😀經文。", FontRole: "scripture"}}
	source.LayoutManifest.Pages[1].Slots[0].Fragments[0].End = 6
	got, err := simplifiedRunner(t).ConvertSimplified(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	spans := got.Components[1].BodySection.Blocks[0].Sentences[0].Spans
	if spans[0].Text != "干杯" || spans[1].Text != "😀经文。" || spans[1].FontRole != "scripture" || got.LayoutManifest.Pages[1].Slots[0].Fragments[0].End != 6 {
		t.Fatalf("incorrect offsets or styling: %+v", spans)
	}
}

func TestConvertSimplifiedPreservesVersionedHistoricalLabels(t *testing.T) {
	for _, version := range []string{"v6", "v7", "v8"} {
		t.Run(version, func(t *testing.T) { testConvertSimplifiedPreservesHistoricalLabels(t, version) })
	}
}

func testConvertSimplifiedPreservesHistoricalLabels(t *testing.T, version string) {
	source := validDocument(4)
	source.LayoutManifest.RendererVersion = version
	source.LayoutManifest.RendererArtifactSha256 = "142cdc601bfb33e2715b46efea7823bac8ee89b1b9620f5186599e34a4363d27"
	if version == "v8" {
		source.LayoutManifest.RendererArtifactSha256 = "646c849b47e87cc3607abccd3e92ad60a432bc55d8b1835f97c5b34ca2793c66"
	}
	if version == "v7" {
		source.LayoutManifest.RendererArtifactSha256 = "cc55a5bd97412c64f90205f1cf8d9c27530d9fb2fedf2cd5b2e23a2435152c85"
	}
	for i, element := range []string{"historicalVision", "historicalGoals", "historicalGospelGoals", "historicalActions", "historicalCommitment"} {
		source.LayoutManifest.Pages[0].FixedSlots = append(source.LayoutManifest.Pages[0].FixedSlots, FixedSlot{
			ID: element, Element: element, Box: Box{X: .4, Y: .1 + float64(i)*.025, Width: .5, Height: .02},
			Style: ParagraphStyle{FontSize: 10, LineHeight: 14},
		})
	}
	before, _ := json.Marshal(source)
	got, err := simplifiedRunner(t).ConvertSimplified(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(source.LayoutManifest.Pages[0].FixedSlots, got.LayoutManifest.Pages[0].FixedSlots) || got.LayoutManifest.RendererVersion != version || got.LayoutManifest.RendererArtifactSha256 != source.LayoutManifest.RendererArtifactSha256 || got.Components[0].Cover.Welcome[0].Sentences[0].Spans[0].Text != "欢迎。" {
		t.Fatal("conversion changed fixed identities or failed to translate dynamic text")
	}
	after, _ := json.Marshal(source)
	if string(before) != string(after) {
		t.Fatal("saved source was mutated")
	}
}

func TestConvertSimplifiedFailsClosed(t *testing.T) {
	runner := simplifiedRunner(t)
	source := validDocument(4)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := runner.ConvertSimplified(ctx, source); err == nil {
		t.Fatal("canceled conversion succeeded")
	}
	source.ContentLocale = "en"
	if _, err := runner.ConvertSimplified(context.Background(), source); err == nil {
		t.Fatal("unsupported source accepted")
	}
	source = validDocument(4)
	source.Components[1].BodySection.Blocks[0].Sentences[0].Spans = []Span{{Text: "乾", FontRole: "body"}, {Text: "隆。", FontRole: "emphasis"}}
	if _, err := runner.ConvertSimplified(context.Background(), source); err == nil {
		t.Fatal("ambiguous style boundary accepted")
	}
}

func TestConvertSimplifiedMetadata(t *testing.T) {
	source := CanonicalMetadata{Title: "永恆的命定和呼召", Subtitle: "", IssueNumber: 1740, Date: "2026-09-27"}
	got, err := simplifiedRunner(t).ConvertMetadata(context.Background(), source)
	if err != nil || got.Title != "永恒的命定和呼召" || got.Subtitle != "" || got.IssueNumber != 1740 || got.Date != source.Date {
		t.Fatalf("metadata: %+v %v", got, err)
	}
}
