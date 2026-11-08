package onlinebulletins

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestReaderPolicyOmitsProductionBandWithoutRemovingArticleSpeaker(t *testing.T) {
	for _, version := range []string{"v7", "v8"} {
		t.Run(version, func(t *testing.T) {
			for _, native := range []bool{false, true} {
				source := candidateGeometry()
				source.Pages[1].Texts[0].Text = "主題～講員"
				source.Pages[1].Texts[0].FontFamily = "Undefined"
				source.Pages[1].Texts[0].Box.Width = 5 * 12 / templateV1Width
				if native {
					for _, row := range []struct {
						text string
						x, y float64
					}{{"‧Sep.13.2026 (1739共2頁)", .115, 234.17696}, {"‧講員：甲牧師", .115, 254.21696}, {"謄修：甲乙", .642, 234.17696}, {"完稿：丙丁", .642, 254.21696}} {
						source.Pages[1].Texts = append(source.Pages[1].Texts, GeometryText{Text: row.text, FontFamily: "DFXingShu", FontSize: 11, Box: Box{X: row.x, Y: row.y / templateV1Height, Width: .3, Height: 11.04 / templateV1Height}})
					}
				}
				before, _ := json.Marshal(source)
				manifest := validDocument(4).LayoutManifest
				manifest.RendererVersion = version
				candidate, issues, err := ApplyTemplateV1(source, candidateIssue(), manifest)
				if err != nil || candidate.Document == nil {
					t.Fatalf("candidate unavailable: %v", err)
				}
				body := candidate.Document.Components[1].BodySection
				if body.Header != nil || privateBlockText(body.Title) != "主題" || len(body.Contributors) != 1 || privateBlockText(body.Contributors[0].Name) != "講員" {
					t.Fatal("production metadata removal changed article title or speaker")
				}
				for _, issue := range issues {
					if issue.Code == "unmapped_body_header" || issue.Code == "inferred_body_header_geometry" {
						t.Fatal("excluded production band still creates editor work")
					}
				}
				encoded, _ := json.Marshal(candidate.Document)
				if !strings.Contains(string(encoded), "KEEP_body") || strings.Contains(string(encoded), "甲牧師") || strings.Contains(string(encoded), "bodyIssueSummary") {
					t.Fatal("article content missing or excluded metadata retained")
				}
				after, _ := json.Marshal(source)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("source mutated")
				}
			}

		})
	}
}

func TestV7ReflowsOnlyFrameVerifiedSpeakerSplits(t *testing.T) {
	for _, version := range []string{"v6", "v7", "v8"} {
		for _, framed := range []bool{false, true} {
			source := candidateGeometry()
			title := &source.Pages[1].Texts[0]
			title.Text, title.FontFamily = "主題～講員", "Undefined"
			title.Box.Width, title.Box.Height = 5*12/templateV1Width, 12/templateV1Height
			if framed {
				source.Pages[1].Frames = []GeometryFrame{{Element: "bodyHeadingFrame", Box: Box{X: title.Box.X, Y: title.Box.Y, Width: 2 * 12 / templateV1Width, Height: title.Box.Height}}}
			}
			manifest := validDocument(4).LayoutManifest
			manifest.RendererVersion = version
			candidate, issues, err := ApplyTemplateV1(source, candidateIssue(), manifest)
			if err != nil || candidate.Document == nil {
				t.Fatalf("candidate unavailable: %v", err)
			}
			body := candidate.Document.Components[1].BodySection
			if privateBlockText(body.Title) != "主題" || len(body.Contributors) != 1 || privateBlockText(body.Contributors[0].Name) != "講員" {
				t.Fatal("framed source text changed")
			}
			found := false
			for _, issue := range issues {
				found = found || issue.Code == "inferred_inline_speaker_geometry"
			}
			if found != (version == "v6" || !framed) {
				t.Fatalf("version=%s framed=%v: review=%v", version, framed, found)
			}
		}
	}
}

func TestReaderPolicyRetainsExcludedBandIdentityMismatch(t *testing.T) {
	for _, version := range []string{"v7", "v8"} {
		t.Run(version, func(t *testing.T) {
			for _, identity := range []string{"1738共2頁", "1739共3頁"} {
				source := candidateGeometry()
				for _, row := range []struct {
					text string
					x, y float64
				}{{"‧Sep.13.2026 (" + identity + ")", .115, 234.17696}, {"‧講員：甲牧師", .115, 254.21696}, {"謄修：甲乙", .642, 234.17696}, {"完稿：丙丁", .642, 254.21696}} {
					source.Pages[1].Texts = append(source.Pages[1].Texts, GeometryText{Text: row.text, FontFamily: "DFXingShu", FontSize: 11, Box: Box{X: row.x, Y: row.y / templateV1Height, Width: .3, Height: 11.04 / templateV1Height}})
				}
				manifest := validDocument(4).LayoutManifest
				manifest.RendererVersion = version
				candidate, issues, err := ApplyTemplateV1(source, candidateIssue(), manifest)
				if err != nil || candidate.Document == nil {
					t.Fatalf("candidate unavailable: %v", err)
				}
				found := false
				for _, issue := range issues {
					found = found || issue.Code == "canonical_source_mismatch" && issue.Blocking && issue.SourcePage == 2
				}
				if !found || candidate.Document.Components[1].BodySection.Header != nil {
					t.Fatal("excluded band's source mismatch lost or rendering restored", identity)
				}
			}

		})
	}
}

func TestReaderPolicyKeepsSeparatorInsideFramedTitleForReview(t *testing.T) {
	for _, version := range []string{"v7", "v8"} {
		t.Run(version, func(t *testing.T) {
			source := candidateGeometry()
			title := &source.Pages[1].Texts[0]
			title.Text, title.FontFamily = "主題～副題～講員", "Undefined"
			title.Box.Width, title.Box.Height = 8*12/templateV1Width, 12/templateV1Height
			source.Pages[1].Frames = []GeometryFrame{{Element: "bodyHeadingFrame", Box: Box{X: title.Box.X, Y: title.Box.Y, Width: 5 * 12 / templateV1Width, Height: title.Box.Height}}}
			manifest := validDocument(4).LayoutManifest
			manifest.RendererVersion = version
			_, issues, err := ApplyTemplateV1(source, candidateIssue(), manifest)
			if err != nil {
				t.Fatal(err)
			}
			for _, issue := range issues {
				if issue.Code == "inferred_inline_speaker_geometry" && issue.Blocking {
					return
				}
			}
			t.Fatal("ambiguous title lost its review requirement")

		})
	}
}

func TestReaderPolicyKeepsAmbiguousFramedSpeakerForReview(t *testing.T) {
	for _, version := range []string{"v7", "v8"} {
		t.Run(version, func(t *testing.T) {
			source := candidateGeometry()
			title := &source.Pages[1].Texts[0]
			title.Text, title.FontFamily = "主題～講員～其他", "Undefined"
			title.Box.Width, title.Box.Height = 8*12/templateV1Width, 12/templateV1Height
			source.Pages[1].Frames = []GeometryFrame{{Element: "bodyHeadingFrame", Box: Box{X: title.Box.X, Y: title.Box.Y, Width: 2 * 12 / templateV1Width, Height: title.Box.Height}}}
			manifest := validDocument(4).LayoutManifest
			manifest.RendererVersion = version
			candidate, issues, err := ApplyTemplateV1(source, candidateIssue(), manifest)
			if err != nil || candidate.Document == nil {
				t.Fatalf("candidate unavailable: %v", err)
			}
			for _, issue := range issues {
				if issue.Code == "inferred_inline_speaker_geometry" && issue.Blocking {
					return
				}
			}
			t.Fatal("ambiguous suffix lost its review requirement")

		})
	}
}

func TestV7NormalizesVaryingTrackingWithoutChangingSourceOrText(t *testing.T) {
	source := candidateGeometry()
	line := &source.Pages[1].Texts[1]
	line.Text, line.FontFamily = "原稿字距保留", "PMingLiU"
	line.Box.Width = 68 / templateV1Width
	next := *line
	next.Text, next.Box.Y, next.Box.Width = "第二行字距異", .39, 54/templateV1Width
	next.Box.X = .115
	source.Pages[1].Texts = append(source.Pages[1].Texts, next)
	before, _ := json.Marshal(source)
	var original Block
	for _, version := range []string{"v6", "v7", "v8"} {
		manifest := validDocument(4).LayoutManifest
		manifest.RendererVersion = version
		candidate, issues, err := ApplyTemplateV1(source, candidateIssue(), manifest)
		if err != nil || candidate.Document == nil {
			t.Fatalf("candidate unavailable: %v", err)
		}
		block := candidate.Document.Components[1].BodySection.Blocks[0]
		found := false
		for _, issue := range issues {
			found = found || issue.Code == "non_uniform_source_tracking" && issue.BlockID == block.ID
		}
		if version == "v6" {
			original = block
			if !found || block.Style.LetterSpacing == 0 {
				t.Fatal("fixture must contain varying source tracking")
			}
		} else {
			if found || block.Style.LetterSpacing != 0 {
				t.Fatal("reader did not normalize tracking")
			}
			original.Style.LetterSpacing = 0
			if !reflect.DeepEqual(original, block) {
				t.Fatal("tracking normalization changed text, anchors, roles or other styling")
			}
		}
	}
	after, _ := json.Marshal(source)
	if string(before) != string(after) {
		t.Fatal("source evidence mutated")
	}
}

func TestReaderPolicyPreservesCurrentProductionBandAndLegacySpacing(t *testing.T) {
	for _, version := range []string{"v7", "v8"} {
		t.Run(version, func(t *testing.T) {
			source := candidateGeometry()
			canonical := candidateIssue()
			canonical.Metadata.Title, canonical.Metadata.Subtitle = "永恆的命定和呼召", "～我們起來建造吧!"
			source.Pages[1].Images = []GeometryImage{
				{Box: Box{X: .1026, Y: .09022, Width: .60502, Height: .0625}, Width: 900, Height: 150, SHA256: strings.Repeat("b", 64)},
				{Box: Box{X: .51401, Y: .20909, Width: .38107, Height: .03627}, Width: 900, Height: 100, SHA256: strings.Repeat("c", 64)},
			}
			before, _ := json.Marshal(source)
			manifest := validDocument(4).LayoutManifest
			manifest.RendererVersion = version
			candidate, _, err := ApplyTemplateV1(source, canonical, manifest)
			if err != nil || candidate.Document == nil {
				t.Fatalf("candidate unavailable: %v", err)
			}
			page := candidate.Document.LayoutManifest.Pages[1]
			artBottom, hasRule := 0.0, false
			for _, slot := range page.FixedSlots {
				if slot.Element == "topRule" {
					hasRule = true
				}
				if slot.Element == "title" || slot.Element == "subtitle" {
					if bottom := slot.Box.Y + slot.Box.Height; bottom > artBottom {
						artBottom = bottom
					}
				}
			}
			first := page.Slots[0]
			if artBottom == 0 {
				t.Fatal("fixture requires canonical title artwork")
			}
			if version == "v7" {
				if hasRule || math.Abs(first.Box.Y-artBottom-16/templateV1Height) > 1e-9 {
					t.Fatal("legacy spacing changed")
				}
			} else {
				body, err := bodyComponents(source.Pages[1:2])
				if err != nil || !hasRule || first.Box.Y != body.slots[0].Box.Y {
					t.Fatal("current reader removed approved blank band/divider", err)
				}
			}
			manifest.RendererVersion = "v6"
			legacy, _, err := ApplyTemplateV1(source, canonical, manifest)
			if err != nil || legacy.Document == nil || !reflect.DeepEqual(candidate.Document.Components[1].BodySection.Title, legacy.Document.Components[1].BodySection.Title) || !reflect.DeepEqual(candidate.Document.LayoutManifest.Pages[2].Slots, legacy.Document.LayoutManifest.Pages[2].Slots) {
				t.Fatal("spacing changed source anchors or another page")
			}
			if len(candidate.Document.Pages) != len(source.Pages) || candidate.Document.Components[1].BodySection.Title.Sentences[0].Source.Page != 2 {
				t.Fatal("reader spacing changed source page identity")
			}
			after, _ := json.Marshal(source)
			if string(before) != string(after) {
				t.Fatal("source evidence mutated")
			}

		})
	}
}
