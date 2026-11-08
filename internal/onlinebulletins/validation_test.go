package onlinebulletins

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func validDocument(pageCount int) Document {
	d := Document{IssueID: "b08a80e8-6388-4166-9a2a-23d336f74b7a", Series: "general", ContentLocale: "zh-Hant", SchemaVersion: "1", TemplateVersion: "v1", SourceAssetChecksum: strings.Repeat("a", 64), SourcePageCount: pageCount}
	d.LayoutManifest = LayoutManifest{TemplateVersion: "v1", RendererVersion: "v1", RendererArtifactSha256: strings.Repeat("b", 64), Assets: []Asset{{URL: "/assets/weekly/v1/" + strings.Repeat("c", 64) + ".woff2", SHA256: strings.Repeat("c", 64), Kind: "font", FontRole: "body"}}}
	block := func(id, text string, page int) Block {
		return Block{ID: "block-" + id, Style: ParagraphStyle{FontSize: 16, LineHeight: 24}, Sentences: []Sentence{{ID: "sentence-" + id, Spans: []Span{{Text: text, FontRole: "body"}}}}}
	}
	songTitle := block("song-title", "詩歌", 1)
	d.Components = []Component{
		{ID: "cover", Type: "cover", Cover: &Cover{Welcome: []Block{block("welcome", "歡迎。", 1)}, Worship: []Item{{ID: "song", Title: &songTitle, Blocks: []Block{}}}, Work: []Item{{ID: "work", Blocks: []Block{block("work", "服事。", 1)}}}, WordQuestions: []Item{{ID: "question", Blocks: []Block{block("question", "問題？", 1)}}}, WeeklyVerses: []Block{block("verse", "經文。", 1)}}},
		{ID: "body", Type: "bodySection", BodySection: &BodySection{Kind: "sermon", Title: block("body-title", "信息", 2), Contributors: []Contributor{{Role: "speaker", Name: block("speaker", "講員", 2)}}, Blocks: []Block{block("body", "正文。", 2)}}},
		{ID: "hymns", Type: "hymnLyrics", HymnLyrics: &HymnLyrics{Hymns: []Hymn{{ID: "hymn", Title: block("hymn-title", "詩歌", pageCount-1), Sections: []HymnSection{{ID: "verse", Kind: "verse", Lines: []Block{block("lyric", "歌詞。", pageCount-1)}}}}}}},
		{ID: "summary", Type: "backSummary", Items: []Item{{ID: "summary-item", Blocks: []Block{block("summary", "摘要。", pageCount)}}}},
		{ID: "announcements", Type: "announcements", Items: []Item{{ID: "announcement", Blocks: []Block{block("announcement", "公告。", pageCount)}}}},
		{ID: "prayers", Type: "victoriesAndPrayers", Items: []Item{{ID: "prayer", Blocks: []Block{block("prayer", "代求。", pageCount)}}}},
	}
	for i := 1; i <= pageCount; i++ {
		id := "page-" + string(rune('a'+i))
		d.Pages = append(d.Pages, Page{ID: id, Width: 595.32, Height: 841.92})
		d.LayoutManifest.Pages = append(d.LayoutManifest.Pages, LayoutPage{PageID: id, Slots: []Slot{}})
	}
	// Hand assigned component/page correspondence, independent of the validator.
	assign := func(page int, component, block, sentence string, n int) {
		d.LayoutManifest.Pages[page-1].Slots = append(d.LayoutManifest.Pages[page-1].Slots, Slot{ID: "slot-" + sentence, ComponentID: component, BlockID: block, Box: Box{X: .1, Y: .1, Width: .8, Height: .1}, Fragments: []Fragment{{SentenceID: sentence, Start: 0, End: n}}})
	}
	assign(1, "cover", "block-welcome", "sentence-welcome", 3)
	assign(1, "cover", "block-song-title", "sentence-song-title", 2)
	assign(1, "cover", "block-work", "sentence-work", 3)
	assign(1, "cover", "block-question", "sentence-question", 3)
	assign(1, "cover", "block-verse", "sentence-verse", 3)
	assign(2, "body", "block-body", "sentence-body", 3)
	assign(2, "body", "block-body-title", "sentence-body-title", 2)
	assign(2, "body", "block-speaker", "sentence-speaker", 2)
	assign(pageCount-1, "hymns", "block-hymn-title", "sentence-hymn-title", 2)
	assign(pageCount-1, "hymns", "block-lyric", "sentence-lyric", 3)
	assign(pageCount, "summary", "block-summary", "sentence-summary", 3)
	assign(pageCount, "announcements", "block-announcement", "sentence-announcement", 3)
	assign(pageCount, "prayers", "block-prayer", "sentence-prayer", 3)
	return d
}

func TestValidateDocument(t *testing.T) {
	for _, delta := range []int{-1, 1} {
		d := validDocument(12)
		d.SourcePageCount += delta
		if _, err := ValidateDocument(d); err == nil {
			t.Fatal("source and saved page counts must match")
		}
	}
	for _, pages := range []int{12, 16} {
		if issues, err := ValidateDocument(validDocument(pages)); err != nil || len(issues) != 0 {
			t.Fatalf("%d-page document: issues=%v err=%v", pages, issues, err)
		}
	}
	for _, kind := range []string{"sermon", "testimony", "teaching", "reflection", "unknown"} {
		d := validDocument(12)
		d.Components[1].BodySection.Kind = kind
		issues, err := ValidateDocument(d)
		if err != nil {
			t.Fatal(err)
		}
		if len(issues) != 0 {
			t.Fatalf("optional classification blocked reading: %v", issues)
		}
	}
}

func TestSimplifiedDocumentRequiresVersionedRenderer(t *testing.T) {
	d := validDocument(4)
	d.ContentLocale = "zh-Hans"
	if _, err := ValidateDocument(d); err == nil {
		t.Fatal("Hans must not reuse V1 rendering identity")
	}
	d.TemplateVersion = "v2"
	d.LayoutManifest.TemplateVersion = "v2"
	d.LayoutManifest.RendererVersion = "v2"
	if _, err := ValidateDocument(d); err != nil {
		t.Fatal("versioned Hans rejected", err)
	}
	d.ContentLocale = "en"
	if _, err := ValidateDocument(d); err == nil {
		t.Fatal("unsupported language accepted")
	}
}

func TestAdaptiveRendererRetainsBothTemplateContracts(t *testing.T) {
	for _, version := range []string{"v1", "v2"} {
		d := validDocument(4)
		d.TemplateVersion = version
		d.LayoutManifest.TemplateVersion = version
		d.LayoutManifest.RendererVersion = "v3"
		if version == "v2" {
			d.ContentLocale = "zh-Hans"
		}
		if _, err := ValidateDocument(d); err != nil {
			t.Fatal("adaptive renderer rejected", version, err)
		}
		d.LayoutManifest.RendererVersion = "v4"
		if _, err := ValidateDocument(d); err != nil {
			t.Fatal("dense-page renderer rejected", version, err)
		}
		d.LayoutManifest.RendererVersion = "v5"
		if _, err := ValidateDocument(d); err != nil {
			t.Fatal("complete-cover renderer rejected", version, err)
		}
		d.LayoutManifest.RendererVersion = "v6"
		if _, err := ValidateDocument(d); err != nil {
			t.Fatal("historical-cover renderer rejected", version, err)
		}
		for _, renderer := range []string{"v7", "v8"} {
			d.LayoutManifest.RendererVersion = renderer
			if _, err := ValidateDocument(d); err != nil {
				t.Fatal("measured renderer rejected", renderer, version, err)
			}
		}
		d.LayoutManifest.RendererVersion = "v999"
		if _, err := ValidateDocument(d); err == nil {
			t.Fatal("unknown renderer accepted")
		}
	}
}

func TestFirstBodyHeaderHasIndependentAnchoredLectureDateAndContributorFields(t *testing.T) {
	d := validDocument(12)
	block := func(id, text string) Block {
		return Block{ID: id, Style: ParagraphStyle{FontSize: 11, LineHeight: 14}, Sentences: []Sentence{{ID: "s-" + id, Spans: []Span{{Text: text, FontRole: "emphasis"}}}}}
	}
	header := &BodyHeader{LectureDate: block("lecture-date", "2026-09-13"), Contributors: []Contributor{{Role: "speaker", Name: block("header-speaker", "講員姓名")}, {Role: "transcriber", Name: block("header-transcriber", "謄修姓名")}, {Role: "editor", Name: block("header-editor", "完稿姓名")}}}
	d.Components[1].BodySection.Header = header
	for _, check := range []struct {
		id    string
		count int
	}{{"lecture-date", 10}, {"header-speaker", 4}, {"header-transcriber", 4}, {"header-editor", 4}} {
		d.LayoutManifest.Pages[1].Slots = append(d.LayoutManifest.Pages[1].Slots, Slot{ID: "slot-" + check.id, ComponentID: "body", BlockID: check.id, Box: Box{X: .1, Y: .25, Width: .3, Height: .02}, Fragments: []Fragment{{SentenceID: "s-" + check.id, End: check.count}}})
	}
	if _, err := ValidateDocument(d); err != nil {
		t.Fatal(err)
	}
	header.Contributors[2].Role = "speaker"
	if _, err := ValidateDocument(d); err == nil {
		t.Fatal("duplicate first-page contributor role accepted")
	}
	header.Contributors[2].Role = "editor"
	d.LayoutManifest.Pages[1].Slots = d.LayoutManifest.Pages[1].Slots[:len(d.LayoutManifest.Pages[1].Slots)-1]
	if _, err := ValidateDocument(d); err == nil {
		t.Fatal("unassigned header sentence accepted")
	}
}

func TestParagraphTypographyMustBeExplicitAndBounded(t *testing.T) {
	for _, style := range []ParagraphStyle{{}, {FontSize: 16, LineHeight: 12}, {FontSize: math.Inf(1), LineHeight: 24}, {FontSize: 16, LineHeight: 193}} {
		d := validDocument(12)
		d.Components[1].BodySection.Blocks[0].Style = style
		if _, err := ValidateDocument(d); err == nil {
			t.Fatal("unmeasurable typography accepted")
		}
	}
}

func TestWhitespaceStyleRunIsAllowedOnlyInsideMeaningfulSentence(t *testing.T) {
	d := validDocument(12)
	d.Components[1].BodySection.Blocks[0].Sentences[0].Spans = []Span{{Text: "正文", FontRole: "body"}, {Text: " ", FontRole: "emphasis"}, {Text: "。", FontRole: "body"}}
	d.LayoutManifest.Pages[1].Slots[0].Fragments[0].End = 4
	if _, err := ValidateDocument(d); err != nil {
		t.Fatal("source whitespace style run rejected")
	}
	d.Components[1].BodySection.Blocks[0].Sentences[0].Spans = []Span{{Text: " ", FontRole: "body"}}
	d.LayoutManifest.Pages[1].Slots[0].Fragments[0].End = 1
	if _, err := ValidateDocument(d); err == nil {
		t.Fatal("whitespace-only sentence accepted")
	}
}

func TestSentenceCannotCollideWithCanonicalMetadataAnchor(t *testing.T) {
	d := validDocument(12)
	d.Components[0].Cover.Welcome[0].Sentences[0].ID = "canonical-title"
	d.LayoutManifest.Pages[0].Slots[0].Fragments[0].SentenceID = "canonical-title"
	if _, err := ValidateDocument(d); err == nil {
		t.Fatal("canonical metadata anchor collision accepted")
	}
}

func TestInlineTypographyMustPreserveSourceSizeAndStayBounded(t *testing.T) {
	for _, size := range []float64{12, 6, 96, 5, 97, math.Inf(1)} {
		d := validDocument(12)
		d.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].FontSize = &size
		_, err := ValidateDocument(d)
		if (size >= 6 && size <= 96) != (err == nil) {
			t.Fatalf("size=%v err=%v", size, err)
		}
	}
}

func TestFixedTemplateTextSlotsAreTypedAndValidated(t *testing.T) {
	data, _ := json.Marshal(validDocument(12))
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	page := value["layoutManifest"].(map[string]any)["pages"].([]any)[0].(map[string]any)
	slot := map[string]any{"id": "fixed-title", "element": "title", "box": map[string]any{"x": .1, "y": .1, "width": .8, "height": .1}, "style": map[string]any{"fontSize": 18, "lineHeight": 24, "indent": 0, "firstLineIndent": 0, "spaceBefore": 0, "spaceAfter": 0}}
	page["fixedSlots"] = []any{slot}
	var encoded []byte
	for _, element := range []string{"title", "speakerSeparator"} {
		slot["element"] = element
		encoded, _ = json.Marshal(value)
		if _, err := DecodeDocument(strings.NewReader(string(encoded))); err != nil {
			t.Fatalf("code-owned metadata slot rejected: %v", err)
		}
	}
	for _, bad := range []string{"serviceRoster", "memberName", "externalScript"} {
		slot["element"] = bad
		encoded, _ = json.Marshal(value)
		if _, err := DecodeDocument(strings.NewReader(string(encoded))); err == nil {
			t.Fatal("unknown fixed element accepted")
		}
	}
}

func TestDecodePreservesBoundedPDFTracking(t *testing.T) {
	data, _ := json.Marshal(validDocument(12))
	var value map[string]any
	_ = json.Unmarshal(data, &value)
	style := value["components"].([]any)[1].(map[string]any)["bodySection"].(map[string]any)["blocks"].([]any)[0].(map[string]any)["style"].(map[string]any)
	style["letterSpacing"] = -.2
	encoded, _ := json.Marshal(value)
	if _, err := DecodeDocument(strings.NewReader(string(encoded))); err != nil {
		t.Fatalf("explicit tracking rejected: %v", err)
	}
	for _, invalid := range []float64{-.51, 1.01} {
		style["letterSpacing"] = invalid
		encoded, _ = json.Marshal(value)
		if _, err := DecodeDocument(strings.NewReader(string(encoded))); err == nil {
			t.Fatal("unbounded tracking accepted")
		}
	}
}

func TestDecodeDoesNotDefaultMissingOrNullLayoutNumbersToZero(t *testing.T) {
	for _, field := range []string{"x", "y", "width", "height", "fontSize", "lineHeight", "indent", "firstLineIndent", "spaceBefore", "spaceAfter"} {
		for _, null := range []bool{false, true} {
			data, _ := json.Marshal(validDocument(12))
			var value map[string]any
			_ = json.Unmarshal(data, &value)
			var target map[string]any
			if field == "x" || field == "y" || field == "width" || field == "height" {
				target = value["layoutManifest"].(map[string]any)["pages"].([]any)[0].(map[string]any)["slots"].([]any)[0].(map[string]any)["box"].(map[string]any)
			} else {
				target = value["components"].([]any)[1].(map[string]any)["bodySection"].(map[string]any)["blocks"].([]any)[0].(map[string]any)["style"].(map[string]any)
			}
			delete(target, field)
			if null {
				target[field] = nil
			}
			data, _ = json.Marshal(value)
			if _, err := DecodeDocument(strings.NewReader(string(data))); err == nil {
				t.Fatalf("missing/null %s accepted (null=%v)", field, null)
			}
		}
	}
}

func TestValidateDocumentRejectsMalformedContent(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Document)
	}{
		{"children cannot use general template", func(d *Document) { d.Series = "children" }},
		{"unknown series", func(d *Document) { d.Series = "other" }},
		{"unsupported locale", func(d *Document) { d.ContentLocale = "en" }},
		{"unsupported template", func(d *Document) { d.TemplateVersion = "latest" }},
		{"manifest template mismatch", func(d *Document) { d.LayoutManifest.TemplateVersion = "v2" }},
		{"invalid issue", func(d *Document) { d.IssueID = "bad" }},
		{"bad source hash", func(d *Document) { d.SourceAssetChecksum = "bad" }},
		{"empty welcome", func(d *Document) { d.Components[0].Cover.Welcome = nil }},
		{"missing required component", func(d *Document) { d.Components = d.Components[:5] }},
		{"forbidden roster", func(d *Document) { d.Components[5].Type = "serviceRoster" }},
		{"forbidden attendance", func(d *Document) { d.Components[5].Type = "attendance" }},
		{"forbidden offerings", func(d *Document) { d.Components[5].Type = "offerings" }},
		{"mixed union payload", func(d *Document) { d.Components[1].Cover = d.Components[0].Cover }},
		{"duplicate sentence", func(d *Document) { d.Components[1].BodySection.Blocks[0].Sentences[0].ID = "sentence-welcome" }},
		{"invalid font role", func(d *Document) { d.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].FontRole = "embedded" }},
		{"empty sentence", func(d *Document) { d.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = " " }},
		{"oversized text", func(d *Document) {
			d.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = strings.Repeat("x", 4097)
		}},
		{"invalid page ref", func(d *Document) { d.LayoutManifest.Pages[0].PageID = "missing" }},
		{"null page slots", func(d *Document) { d.LayoutManifest.Pages[2].Slots = nil }},
		{"null item blocks", func(d *Document) { d.Components[0].Cover.Worship[0].Blocks = nil }},
		{"invalid component ref", func(d *Document) { d.LayoutManifest.Pages[0].Slots[0].ComponentID = "missing" }},
		{"wrong block owner", func(d *Document) { d.LayoutManifest.Pages[0].Slots[0].BlockID = "block-body" }},
		{"missing fragment", func(d *Document) { d.LayoutManifest.Pages[0].Slots[0].Fragments = nil }},
		{"incomplete sentence", func(d *Document) { d.LayoutManifest.Pages[0].Slots[0].Fragments[0].End = 2 }},
		{"invalid continuation", func(d *Document) { d.LayoutManifest.Pages[0].Slots[0].ContinuationOf = "missing" }},
		{"box overflow", func(d *Document) { d.LayoutManifest.Pages[0].Slots[0].Box.Width = 1 }},
		{"nonfinite box", func(d *Document) { d.LayoutManifest.Pages[0].Slots[0].Box.X = math.NaN() }},
		{"zero paper width", func(d *Document) { d.Pages[0].Width = 0 }},
		{"mutable asset path", func(d *Document) { d.LayoutManifest.Assets[0].URL = "/assets/weekly/v1/latest.woff2" }},
		{"external asset", func(d *Document) { d.LayoutManifest.Assets[0].URL = "https://example.com/font.woff2" }},
		{"too many pages", func(d *Document) { d.Pages = make([]Page, 81) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := validDocument(12)
			tc.mutate(&d)
			if _, err := ValidateDocument(d); err == nil {
				t.Fatal("malformed document accepted")
			}
		})
	}
}

func TestDecodeDocumentRejectsUnknownFieldsAndTrailingInput(t *testing.T) {
	d, err := json.Marshal(validDocument(12))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeDocument(strings.NewReader(string(d))); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{`{"sourceAssetId":"private"}`, strings.TrimSuffix(string(d), "}") + `,"title":"second canonical source"}`, string(d) + ` {}`, strings.Repeat(" ", MaxDocumentBytes+1)} {
		if _, err := DecodeDocument(strings.NewReader(input)); err == nil {
			t.Fatal("unknown, oversized or trailing input accepted")
		}
	}
}

func TestSentenceContinuationUsesCodePointsAndPreservesIdentity(t *testing.T) {
	d := validDocument(12)
	d.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = "𠮷正文。"
	first := &d.LayoutManifest.Pages[1].Slots[0]
	first.Fragments[0].End = 2
	d.LayoutManifest.Pages[2].Slots = []Slot{{ID: "slot-continuation", ComponentID: "body", BlockID: "block-body", ContinuationOf: first.ID, Box: first.Box, Fragments: []Fragment{{SentenceID: "sentence-body", Start: 2, End: 4}}}}
	if _, err := ValidateDocument(d); err != nil {
		t.Fatal(err)
	}
	d.LayoutManifest.Pages[2].Slots[0].Fragments[0].Start = 1
	if _, err := ValidateDocument(d); err == nil {
		t.Fatal("overlapping continuation accepted")
	}
}

func TestRequiredArraysAreNeverNullOnWire(t *testing.T) {
	d := validDocument(12)
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"slots":null`) || strings.Contains(string(data), `"blocks":null`) {
		t.Fatal("required collection marshals as null")
	}
}

func TestDisplayTitlesHaveSentenceAnchors(t *testing.T) {
	data, err := json.Marshal(validDocument(12))
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err = json.Unmarshal(data, &wire); err != nil {
		t.Fatal(err)
	}
	components := wire["components"].([]any)
	bodyTitle := components[1].(map[string]any)["bodySection"].(map[string]any)["title"]
	worshipTitle := components[0].(map[string]any)["cover"].(map[string]any)["worship"].([]any)[0].(map[string]any)["title"]
	for _, title := range []any{bodyTitle, worshipTitle} {
		block, ok := title.(map[string]any)
		if !ok || block["id"] == nil || len(block["sentences"].([]any)) == 0 {
			t.Fatal("display title lacks stable block/sentence anchors and cannot be positioned or annotated")
		}
	}
}
