package onlinebulletins

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func recognitionDocumentFixture(t *testing.T) (GeometryDocument, []RecognitionPage, []RecognitionResult) {
	t.Helper()
	source := candidateGeometry()
	for i := range source.Pages[0].Texts {
		if source.Pages[0].Texts[i].Text == "本週金句" {
			source.Pages[0].Texts[i].Text = "其他標籤"
		}
	}
	pages, err := PrepareRecognitionPages(source, recognitionRasters(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	var results []RecognitionResult
	for _, page := range pages {
		result := RecognitionResult{Page: page.Page, Blocks: []RecognitionBlock{}, UnassignedIDs: []string{}}
		for _, part := range page.Native {
			role := "fixed_label"
			switch {
			case strings.Contains(part.Text, "KEEP_welcome"):
				role = "cover_welcome"
			case strings.Contains(part.Text, "KEEP_song"):
				role = "cover_worship"
			case strings.Contains(part.Text, "KEEP_work"):
				role = "cover_work"
			case strings.Contains(part.Text, "KEEP_question"):
				role = "cover_word"
			case strings.Contains(part.Text, "KEEP_verse"):
				role = "cover_verse"
			case page.Page == 2 && part.Text == "主題":
				role = "body_title"
			case strings.Contains(part.Text, "KEEP_body"):
				role = "body_paragraph"
			case strings.Contains(part.Text, "KEEP_hymn"):
				role = "hymn_title"
			case strings.Contains(part.Text, "KEEP_lyric"):
				role = "hymn_verse"
			case strings.Contains(part.Text, "KEEP_summary"):
				role = "back_summary"
			case strings.Contains(part.Text, "KEEP_announcement"):
				role = "announcement"
			case strings.Contains(part.Text, "KEEP_prayer"):
				role = "prayer"
			}
			kind := ""
			if role == "body_title" {
				kind = "sermon"
			}
			group := part.ID
			if strings.HasPrefix(role, "body_") {
				group = "article"
			}
			if strings.HasPrefix(role, "hymn_") {
				group = "song"
			}
			result.Blocks = append(result.Blocks, RecognitionBlock{SectionKind: kind, Mode: "native", Role: role, Group: group, Text: part.Text, SourceIDs: []string{part.ID}, Box: part.Box})
		}
		results = append(results, result)
	}
	return source, pages, results
}

func TestAssembleRecognitionDoesNotUseLegacyCoverParser(t *testing.T) {
	source, pages, results := recognitionDocumentFixture(t)
	// The LLM result is authoritative for semantics even without a legacy verse label.

	candidate, issues, err := AssembleRecognition(source, pages, results, candidateIssue(), ConversionTemplateSettings(), validDocument(4).LayoutManifest.Assets)
	if err != nil || candidate.Document == nil {
		t.Fatal("full recognition did not form a document", err, issues)
	}
	document := *candidate.Document
	for page, elements := range map[int][]string{0: {"title", "subtitle"}, 1: {"title", "subtitle", "topRule"}} {
		for _, element := range elements {
			found := false
			for _, slot := range document.LayoutManifest.Pages[page].FixedSlots {
				found = found || slot.Element == element
			}
			if !found {
				t.Fatal("canonical header omitted", page, element)
			}
		}
	}

	if _, err := ValidateDocument(document); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(document)
	if !strings.Contains(string(data), "KEEP_body") || !strings.Contains(string(data), "KEEP_verse") || strings.Contains(string(data), "DROP_roster") {
		t.Fatal("source content lost or private content retained")
	}
	if len(document.Components) != 6 || document.LayoutManifest.RendererVersion != "v11" {
		t.Fatal("existing document contract not preserved")
	}
}

func TestAssembleRecognitionRejectsMissingOrTamperedSource(t *testing.T) {
	for _, name := range []string{"missing_page", "duplicate_id", "rewritten_native", "wrong_checksum", "unassigned"} {
		t.Run(name, func(t *testing.T) {
			source, pages, results := recognitionDocumentFixture(t)
			switch name {
			case "missing_page":
				results = results[:3]
			case "duplicate_id":
				results[0].Blocks = append(results[0].Blocks, results[0].Blocks[0])
			case "rewritten_native":
				results[0].Blocks[0].Text = "改字"
			case "wrong_checksum":
				pages[0].SourceSHA256 = strings.Repeat("b", 64)
			case "unassigned":
				results[0].UnassignedIDs = []string{results[0].Blocks[0].SourceIDs[0]}
				results[0].Blocks = results[0].Blocks[1:]
			}
			if _, _, err := AssembleRecognition(source, pages, results, candidateIssue(), ConversionTemplateSettings(), validDocument(4).LayoutManifest.Assets); err == nil {
				t.Fatal("incomplete source accepted")
			}
		})
	}
}

func TestRecognitionUsesActualGlyphBoundsInsteadOfLegacyColumnCutoff(t *testing.T) {
	source, pages, results := recognitionDocumentFixture(t)
	for i := range source.Pages[1].Texts {
		if strings.Contains(source.Pages[1].Texts[i].Text, "KEEP_body") {
			source.Pages[1].Texts[i].Box.Width = .82
		}
	}
	for i := range pages[1].Native {
		if strings.Contains(pages[1].Native[i].Text, "KEEP_body") {
			pages[1].Native[i].Box.Width = .82
		}
	}
	for i := range results[1].Blocks {
		if results[1].Blocks[i].Role == "body_paragraph" {
			results[1].Blocks[i].Box.Width = .82
		}
	}
	if _, _, err := AssembleRecognition(source, pages, results, candidateIssue(), ConversionTemplateSettings(), validDocument(4).LayoutManifest.Assets); err != nil {
		t.Fatal("legacy column cutoff blocked recognised content", err)
	}
}

func TestRecognitionKeepsLateSubheadingsInReadingOrder(t *testing.T) {
	source, pages, results := recognitionDocumentFixture(t)
	for i, text := range []string{"後段小標一", "後段小標二"} {
		box := Box{X: .155, Y: .4 + float64(i)*.04, Width: .5, Height: .015}
		source.Pages[1].Texts = append(source.Pages[1].Texts, GeometryText{Text: text, Box: box, FontFamily: "MingLiU", FontSize: 12})
		id := fmt.Sprintf("p2-t%d", len(pages[1].Native)+1)
		pages[1].Native = append(pages[1].Native, RecognitionText{ID: id, Text: text, Box: box})
		results[1].Blocks = append(results[1].Blocks, RecognitionBlock{Mode: "native", Role: "body_subtitle", Group: "article", Text: text, SourceIDs: []string{id}, Box: box})
	}
	candidate, _, err := AssembleRecognition(source, pages, results, candidateIssue(), ConversionTemplateSettings(), validDocument(4).LayoutManifest.Assets)
	if err != nil {
		t.Fatal(err)
	}
	body := candidate.Document.Components[1].BodySection
	if body.Subtitle != nil || len(body.Blocks) != 3 {
		t.Fatal("late subheading moved ahead of article text")
	}
}

func TestRecognitionIllustrationRequiresOwnedBytesAndTrustedBounds(t *testing.T) {
	source, pages, results := recognitionDocumentFixture(t)
	candidate, _, err := AssembleRecognition(source, pages, results, candidateIssue(), ConversionTemplateSettings(), validDocument(4).LayoutManifest.Assets)
	if err != nil {
		t.Fatal(err)
	}
	box := Box{X: .2, Y: .5, Width: .3, Height: .1}
	candidate.Evidence.Pages[1].Images = []GeometryImage{{SHA256: strings.Repeat("f", 64), Box: box}}
	parts, err := CatalogParts(candidate.Evidence)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, part := range parts {
		if part.Kind == "image" {
			id = part.ID
		}
	}
	recognized := []RecognitionResult{{Page: 2, Blocks: []RecognitionBlock{{Role: "illustration", ImageIDs: []string{id}, Box: Box{X: .9, Y: .9, Width: .1, Height: .1}}}}}
	if err := attachRecognitionImages(&candidate, recognized, nil); err == nil {
		t.Fatal("missing owned image silently dropped")
	}
	image := SourceImage{AssetID: "b08a80e8-6388-4166-9a2a-23d336f74b7b", SHA256: strings.Repeat("e", 64), SizeBytes: 128}
	if err := attachRecognitionImages(&candidate, recognized, map[string]SourceImage{id: image}); err != nil {
		t.Fatal(err)
	}
	pictures := DocumentIllustrations(*candidate.Document)
	if len(pictures) != 1 || pictures[0].Box != box || pictures[0].AssetID != image.AssetID {
		t.Fatal("model replaced source image authority")
	}
}

func TestRecognitionRemovesOnlyCodeOwnedCoverLabel(t *testing.T) {
	input := []GeometryText{{Text: "一、Wel"}, {Text: "come：原稿內容"}}
	trimmed := recognitionContentParts(input, "cover_welcome")
	if len(trimmed) != 1 || trimmed[0].Text != "原稿內容" || input[1].Text != "come：原稿內容" {
		t.Fatal("fixed label or immutable source changed")
	}
	body := recognitionContentParts(input, "body_paragraph")
	if len(body) != 2 || body[0].Text != input[0].Text {
		t.Fatal("body prose was stripped")
	}
}

func TestRecognitionHymnNumberMayPrecedeItsTitle(t *testing.T) {
	source, pages, results := recognitionDocumentFixture(t)
	for i := range results[2].Blocks {
		b := &results[2].Blocks[i]
		if b.Role == "fixed_label" {
			b.Role, b.Group = "hymn_number", "song"
			break
		}
	}
	candidate, _, err := AssembleRecognition(source, pages, results, candidateIssue(), ConversionTemplateSettings(), validDocument(4).LayoutManifest.Assets)
	if err != nil || candidate.Document == nil {
		t.Fatal("number above title rejected", err)
	}
	for _, component := range candidate.Document.Components {
		if component.HymnLyrics != nil {
			if len(component.HymnLyrics.Hymns) != 1 || component.HymnLyrics.Hymns[0].Number == nil {
				t.Fatal("number assigned to a different hymn")
			}
		}
	}
}
