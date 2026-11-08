package onlinebulletins

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestParagraphUsesExplicitScriptureReferenceWithoutHidingUnknownFonts(t *testing.T) {
	for _, tc := range []struct {
		text, font, role string
		unknown          bool
	}{
		{"創世記一章26-27節：神說：我們要照著我們的形像。", "MingLiU", "scripture", false},
		{"詩篇廿三篇1節：耶和華是我的牧者。", "Undefined", "scripture", true},
		{"我們今天讀創世記一章26節：思想神的話。", "MingLiU", "body", false},
		{"創世記一章提到人的受造。", "MingLiU", "body", false},
	} {
		line := geometryLine{page: 3, width: templateV1Width, height: templateV1Height, parts: []GeometryText{{Text: tc.text, FontFamily: tc.font, FontSize: 12, Box: Box{X: .1, Y: .2, Width: .7, Height: .02}}}}
		block, _, issues, err := paragraphBlock("body", 1, []geometryLine{line})
		if err != nil {
			t.Fatal(err)
		}
		for _, sentence := range block.Sentences {
			for _, span := range sentence.Spans {
				if span.FontRole != tc.role {
					t.Fatalf("%s: role=%s", tc.text, span.FontRole)
				}
			}
		}
		unknown := false
		for _, issue := range issues {
			unknown = unknown || issue.Code == "unknown_font_role"
		}
		if unknown != tc.unknown || privateBlockText(block) != tc.text {
			t.Fatal("source text or uncertainty changed", issues)
		}
	}
}

func TestUnknownFontFindingLocatesAffectedSentenceNotFirstSentence(t *testing.T) {
	lines := []geometryLine{
		{page: 3, width: templateV1Width, height: templateV1Height, parts: []GeometryText{{Text: "已知字型。", FontFamily: "MingLiU", FontSize: 12, Box: Box{X: .1, Y: .2, Width: .3, Height: .02}}}},
		{page: 4, width: templateV1Width, height: templateV1Height, parts: []GeometryText{{Text: "待核對字型。", FontFamily: "Undefined", FontSize: 12, Box: Box{X: .1, Y: .2, Width: .3, Height: .02}}}},
	}
	block, _, issues, err := paragraphBlock("body", 2, lines)
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range issues {
		if issue.Code == "unknown_font_role" {
			if issue.BlockID != block.ID || issue.SentenceID != block.Sentences[1].ID || issue.SourcePage != 4 {
				t.Fatalf("wrong source locator: %#v", issue)
			}
			return
		}
	}
	t.Fatal("missing font finding")
}

func TestParagraphRecognizesCambriaBodyWithoutAcceptingOtherCambriaFaces(t *testing.T) {
	for _, tc := range []struct {
		font    string
		unknown bool
	}{
		{"Cambria", false},
		{"BCEDEE+Cambria", false},
		{"CambriaMath", true},
		{"Cambria-Unknown", true},
	} {
		t.Run(tc.font, func(t *testing.T) {
			line := geometryLine{page: 12, width: templateV1Width, height: templateV1Height, parts: []GeometryText{
				{Text: "本來以為是簽約", FontFamily: "PMingLiU", FontSize: 12, Box: Box{X: .1, Y: .2, Width: .2, Height: .02}},
				{Text: "VIP", FontFamily: tc.font, FontSize: 12, Box: Box{X: .3, Y: .2, Width: .05, Height: .02}},
				{Text: "客戶。", FontFamily: "PMingLiU", FontSize: 12, Box: Box{X: .35, Y: .2, Width: .1, Height: .02}},
			}}
			block, _, issues, err := paragraphBlock("body", 1, []geometryLine{line})
			if err != nil {
				t.Fatal(err)
			}
			if privateBlockText(block) != "本來以為是簽約VIP客戶。" {
				t.Fatal("source text changed")
			}
			unknown := false
			for _, issue := range issues {
				unknown = unknown || issue.Code == "unknown_font_role"
			}
			if unknown != tc.unknown {
				t.Fatalf("unexpected font review: %#v", issues)
			}
			for _, span := range block.Sentences[0].Spans {
				if span.FontRole != "body" {
					t.Fatalf("ordinary prose styled as %s", span.FontRole)
				}
			}
		})
	}
}

func TestParagraphNormalizesOnlyVerifiedWingdingsStars(t *testing.T) {
	line := geometryLine{page: 3, width: templateV1Width, height: templateV1Height, right: .9, parts: []GeometryText{{Text: "\uf0ab\uf0ac", FontFamily: "Wingdings", FontSize: 12, Box: Box{X: .1, Y: .2, Width: .1, Height: .02}}}}
	block, _, issues, err := paragraphBlock("hymns", 1, []geometryLine{line})
	if err != nil || len(issues) != 0 || privateBlockText(block) != "★\U0001f7cb" || block.Sentences[0].Spans[0].FontRole != "symbol" || !fontRole("symbol") {
		t.Fatal("verified Wingdings stars did not receive Unicode and a legal symbol role")
	}
	line.parts[0].FontFamily = "BCECEE+Wingdings-Regular"
	block, _, issues, err = paragraphBlock("hymns", 1, []geometryLine{line})
	if err != nil || len(issues) != 0 || privateBlockText(block) != "★\U0001f7cb" {
		t.Fatal("verified original font subset identity was not recognized")
	}
	line.parts[0].FontFamily = "MingLiU"
	block, _, _, err = paragraphBlock("hymns", 1, []geometryLine{line})
	if err != nil || privateBlockText(block) != "\uf0ab\uf0ac" {
		t.Fatal("private-use characters from another font were silently remapped")
	}
	line.parts[0].FontFamily = "Wingdings 2"
	block, _, issues, err = paragraphBlock("hymns", 1, []geometryLine{line})
	if err != nil || len(issues) == 0 || privateBlockText(block) != "\uf0ab\uf0ac" {
		t.Fatal("another Wingdings family received the original font's mapping")
	}
	line.parts[0].FontFamily, line.parts[0].Text = "Wingdings", "\uf0ad"
	block, _, issues, err = paragraphBlock("hymns", 1, []geometryLine{line})
	if err != nil || len(issues) != 0 || privateBlockText(block) != "\U0001f7cf" {
		t.Fatal("verified Wingdings eight-pointed star not mapped")
	}
	line.parts[0].Text = "\uf0ae"
	block, _, issues, err = paragraphBlock("hymns", 1, []geometryLine{line})
	if err != nil || len(issues) == 0 || !issues[0].Blocking || privateBlockText(block) != "\uf0ae" {
		t.Fatal("unreviewed Wingdings mapping guessed")
	}
}

func TestBodyWingdingsNumbersUseTheExistingUnicodeAndLegalBodyFace(t *testing.T) {
	line := geometryLine{page: 3, width: templateV1Width, height: templateV1Height, right: .9, parts: []GeometryText{{Text: "\uf08c\uf08d\uf08e\uf08f\uf090\uf0ab", FontFamily: "BCECEE+Wingdings-Regular", FontSize: 12, Box: Box{X: .1, Y: .2, Width: .15, Height: .02}}}}
	block, _, issues, err := paragraphBlock("body", 1, []geometryLine{line})
	if err != nil || len(issues) != 0 || privateBlockText(block) != "➊➋➌➍➎★" || len(block.Sentences[0].Spans) != 2 || block.Sentences[0].Spans[0].FontRole != "body" || block.Sentences[0].Spans[1].FontRole != "symbol" {
		t.Fatal("verified body numbering retained private-use glyphs or selected the wrong legal face")
	}
	if line.parts[0].Text != "\uf08c\uf08d\uf08e\uf08f\uf090\uf0ab" {
		t.Fatal("normalization mutated source evidence")
	}
	line.parts[0].FontFamily = "Wingdings 2"
	block, _, issues, err = paragraphBlock("body", 1, []geometryLine{line})
	if err != nil || len(issues) == 0 || privateBlockText(block) != line.parts[0].Text {
		t.Fatal("another font's numbering silently changed")
	}
}

func TestTemplateV1ExcludesPrivateBackRegionsBeforeReturningPayload(t *testing.T) {
	geometry := GeometryDocument{Pages: []GeometryPage{{Number: 1, Width: 595.32, Height: 841.92}, {Number: 2, Width: 595.32, Height: 841.92}, {Number: 3, Width: 595.32, Height: 841.92}, {Number: 4, Width: 595.32, Height: 841.92}}}
	geometry.Pages[1].Frames = []GeometryFrame{{Element: "bodyHeadingFrame", Box: Box{X: .1, Y: .4, Width: .4, Height: .02}}}
	back := &geometry.Pages[3]
	for _, line := range []struct {
		text        string
		top, height float64
	}{{"KEEP_summary", 40, 16}, {"KEEP_announcements", 210, 16}, {"KEEP_prayers", 443, 16}, {"DROP_cross_boundary", 460, 16}, {"主日敬拜服事表", 470, 14}, {"DROP_roster", 490, 16}, {"DROP_attendance", 580, 16}, {"DROP_offerings", 600, 16}} {
		back.Texts = append(back.Texts, GeometryText{Text: line.text, Box: Box{X: .1, Y: line.top / back.Height, Width: .6, Height: line.height / back.Height}})
	}
	back.Images = []GeometryImage{{Box: Box{X: .6, Y: .75, Width: .2, Height: .1}, SHA256: strings.Repeat("a", 64)}}
	back.Frames = []GeometryFrame{
		{Element: "summaryFrame", Box: Box{X: .1, Y: 40 / back.Height, Width: .8, Height: 100 / back.Height}},
		{Element: "DROP_private_vector", Box: Box{X: .1, Y: 460 / back.Height, Width: .8, Height: 100 / back.Height}},
	}
	allowed, issues, err := filterTemplateV1Geometry(geometry)
	if err != nil {
		t.Fatal(err)
	}
	if len(allowed.Pages[1].Frames) != 1 {
		t.Fatal("source heading frame lost at privacy boundary")
	}
	data, err := json.Marshal(allowed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "DROP_") || strings.Contains(string(data), "服事表") || strings.Contains(string(data), strings.Repeat("a", 64)) {
		t.Fatal("excluded source escaped filter")
	}
	for _, keep := range []string{"KEEP_summary", "KEEP_announcements", "KEEP_prayers"} {
		if !strings.Contains(string(data), keep) {
			t.Fatal("allowed component lost")
		}
	}
	if len(issues) != 1 || issues[0].Code != "source_crosses_exclusion_boundary" || !issues[0].Blocking {
		t.Fatal("cross-boundary text silently lost")
	}
	if len(allowed.Pages[3].Frames) != 1 || len(back.Frames) != 2 {
		t.Fatal("vector filtering lost retained frame or mutated source")
	}
}

func TestUnknownBackLayoutCannotSendBackTextToClassification(t *testing.T) {
	geometry := GeometryDocument{Pages: make([]GeometryPage, 4)}
	for index := range geometry.Pages {
		geometry.Pages[index] = GeometryPage{Number: index + 1, Width: 595.32, Height: 841.92}
	}
	geometry.Pages[3].Texts = []GeometryText{{Text: "DROP_unknown_back", Box: Box{X: .1, Y: .1, Width: .5, Height: .05}}}
	geometry.Pages[3].Frames = []GeometryFrame{{Element: "summaryFrame", Box: Box{X: .1, Y: .1, Width: .8, Height: .2}}}
	allowed, issues, err := filterTemplateV1Geometry(geometry)
	if err != nil || len(allowed.Pages[3].Texts) != 0 || len(allowed.Pages[3].Frames) != 0 || len(issues) != 1 || issues[0].Code != "unknown_back_layout" || !issues[0].Blocking {
		t.Fatal("unknown layout leaked back text")
	}
	if len(geometry.Pages[3].Texts) != 1 {
		t.Fatal("filter mutated caller source")
	}
}

func TestBackExclusionFollowsAnEarlierServiceTableAnchor(t *testing.T) {
	geometry := GeometryDocument{Pages: make([]GeometryPage, 4)}
	for index := range geometry.Pages {
		geometry.Pages[index] = GeometryPage{Number: index + 1, Width: templateV1Width, Height: templateV1Height}
	}
	back := &geometry.Pages[3]
	for _, line := range []struct {
		text        string
		top, height float64
	}{{"KEEP_summary", 40, 16}, {"KEEP_prayers", 440, 13}, {"DROP_cross_boundary", 454, 8}, {"主日敬拜服事表", 457, 12}, {"DROP_roster", 480, 16}} {
		back.Texts = append(back.Texts, GeometryText{Text: line.text, Box: Box{X: .1, Y: line.top / back.Height, Width: .6, Height: line.height / back.Height}})
	}
	back.Texts = append(back.Texts, GeometryText{Text: "\uf0ab", FontFamily: "Wingdings-Regular", Box: Box{X: .23, Y: 455 / back.Height, Width: .02, Height: 14 / back.Height}})
	allowed, issues, err := filterTemplateV1Geometry(geometry)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(allowed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "DROP_") || strings.Contains(string(data), "服事表") || strings.Contains(string(data), "\uf0ab") || !strings.Contains(string(data), "KEEP_prayers") {
		t.Fatal("earlier service table leaked private content or discarded the allowed back")
	}
	if len(issues) != 1 || issues[0].Code != "source_crosses_exclusion_boundary" || !issues[0].Blocking {
		t.Fatal("cross-boundary text must require review")
	}
}

func TestServiceTableAnchorMayHaveMultipleFontRuns(t *testing.T) {
	geometry := GeometryDocument{Pages: make([]GeometryPage, 4)}
	for index := range geometry.Pages {
		geometry.Pages[index] = GeometryPage{Number: index + 1, Width: 595.32, Height: 841.92}
	}
	geometry.Pages[3].Texts = []GeometryText{{Text: "KEEP_summary", Box: Box{X: .2, Y: .1, Width: .5, Height: .03}}, {Text: "主日", Box: Box{X: .23, Y: 470 / templateV1Height, Width: .05, Height: .02}}, {Text: "敬拜服事表", Box: Box{X: .28, Y: 470 / templateV1Height, Width: .14, Height: .02}}}
	allowed, issues, err := filterTemplateV1Geometry(geometry)
	if err != nil || len(issues) != 0 || len(allowed.Pages[3].Texts) != 1 {
		t.Fatalf("split anchor lost: %v %v", issues, err)
	}
}

func TestLetterBackExclusionPreservesPrayersAndBoundary(t *testing.T) {
	geometry := GeometryDocument{Pages: make([]GeometryPage, 4)}
	for i := range geometry.Pages {
		geometry.Pages[i] = GeometryPage{Number: i + 1, Width: 612, Height: 792}
	}
	back := &geometry.Pages[3]
	for _, line := range []struct {
		text        string
		top, height float64
	}{
		{"KEEP_summary", 40, 16}, {"KEEP_announcements", 180, 16}, {"KEEP_prayers", 410, 13},
		{"DROP_crossing", 425, 8}, {"主日敬拜服事表", 429.112811, 12}, {"DROP_roster", 445, 16},
	} {
		back.Texts = append(back.Texts, GeometryText{Text: line.text, Box: Box{X: .1, Y: line.top / back.Height, Width: .6, Height: line.height / back.Height}})
	}
	allowed, issues, err := filterTemplateV1Geometry(geometry)
	if err != nil {
		t.Fatal(err)
	}
	if len(allowed.Pages[3].Texts) != 3 || allowed.Pages[3].Texts[2].Text != "KEEP_prayers" {
		t.Fatal("Letter back lost retained content or leaked roster")
	}
	if len(issues) != 1 || issues[0].Code != "source_crosses_exclusion_boundary" || !issues[0].Blocking {
		t.Fatalf("crossing content not flagged: %+v", issues)
	}
	if math.Abs(backContentBottom(allowed.Pages[3])-429.112811) > 1e-6 {
		t.Fatal("back composer reverted to a later boundary")
	}
}

func TestBackExclusionRetainsPrayersAboveLaterVerifiedTable(t *testing.T) {
	geometry := GeometryDocument{Pages: make([]GeometryPage, 4)}
	for i := range geometry.Pages {
		geometry.Pages[i] = GeometryPage{Number: i + 1, Width: templateV1Width, Height: templateV1Height}
	}
	back := &geometry.Pages[3]
	for _, line := range []struct {
		text        string
		top, height float64
	}{
		{"KEEP_prayers", 458, 14}, {"DROP_crossing", 480, 10}, {"主日敬拜服事表", 484.47, 12}, {"DROP_roster", 499, 14},
	} {
		back.Texts = append(back.Texts, GeometryText{Text: line.text, Box: Box{X: .1, Y: line.top / back.Height, Width: .6, Height: line.height / back.Height}})
	}
	allowed, issues, err := filterTemplateV1Geometry(geometry)
	if err != nil || len(allowed.Pages[3].Texts) != 1 || allowed.Pages[3].Texts[0].Text != "KEEP_prayers" || len(issues) != 1 || issues[0].Code != "source_crosses_exclusion_boundary" {
		t.Fatalf("later anchor lost allowed prayer or retained excluded source: %+v %v %v", allowed.Pages[3], issues, err)
	}
}

func TestParagraphCapturesFullEmSourceTrackingWithoutGuessingLatinAdvances(t *testing.T) {
	line := geometryLine{page: 1, width: 595.32, height: 841.92, parts: []GeometryText{{Text: "原稿字距保留", FontFamily: "PMingLiU", FontSize: 13, Box: Box{X: .1, Y: .2, Width: 72 / 595.32, Height: 14 / 841.92}}}}
	block, _, _, err := paragraphBlock("body", 1, []geometryLine{line})
	if err != nil || math.Abs(block.Style.LetterSpacing-(-6.0/(5*13))) > 1e-9 || block.Style.FontSize != 13 {
		t.Fatalf("source tracking lost: %v %+v", err, block.Style)
	}
	line.parts[0].Text = "LatinOnly"
	block, _, _, err = paragraphBlock("body", 1, []geometryLine{line})
	if err != nil || block.Style.LetterSpacing != 0 {
		t.Fatal("proportional Latin advances were guessed")
	}
	line.parts[0].Text = "原稿字距保留"
	second := line
	second.parts = append([]GeometryText(nil), line.parts...)
	second.parts[0].Box.Y += .03
	second.parts[0].Box.Width = 60 / 595.32
	_, _, issues, err := paragraphBlock("body", 1, []geometryLine{line, second})
	if err != nil || len(issues) != 1 || issues[0].Code != "non_uniform_source_tracking" || !issues[0].Blocking {
		t.Fatalf("varying tracking silently accepted: %v %v", err, issues)
	}
}

func TestParagraphSentencesPreserveInlineRolesQuotesAndPageContinuations(t *testing.T) {
	lines := []geometryLine{
		{page: 1, width: 595.32, height: 841.92, parts: []GeometryText{{Text: "第一句。", FontFamily: "MingLiU", FontSize: 16, Box: Box{X: .1, Y: .85, Width: .3, Height: .025}}, {Text: "「第二", FontFamily: "DFKaiShu", FontSize: 12, Box: Box{X: .4, Y: .85, Width: .2, Height: .025}}}},
		{page: 2, width: 595.32, height: 841.92, parts: []GeometryText{{Text: "句！」", FontFamily: "DFKaiShu", FontSize: 12, Box: Box{X: .1, Y: .1, Width: .2, Height: .025}}, {Text: "第三句？", FontFamily: "DFXingShu", FontSize: 16, Box: Box{X: .3, Y: .1, Width: .3, Height: .025}}}},
	}
	block, slots, issues, err := paragraphBlock("body", 1, lines)
	if err != nil || len(issues) != 0 || len(slots) != 2 || len(block.Sentences) != 3 {
		t.Fatalf("block build: %v %v", issues, err)
	}
	texts := make([]string, len(block.Sentences))
	for index, sentence := range block.Sentences {
		for _, span := range sentence.Spans {
			texts[index] += span.Text
		}
	}
	if strings.Join(texts, "|") != "第一句。|「第二句！」|第三句？" {
		t.Fatalf("sentences=%v", texts)
	}
	if slots[1].ContinuationOf != slots[0].ID || slots[0].Fragments[1].SentenceID != slots[1].Fragments[0].SentenceID || slots[0].Fragments[1].End != slots[1].Fragments[0].Start {
		t.Fatal("cross-page sentence identity lost")
	}
	span := block.Sentences[1].Spans[0]
	if span.FontRole != "scripture" || span.FontSize == nil || *span.FontSize != 12 {
		t.Fatal("inline font evidence lost")
	}
	other, otherSlots, _, err := paragraphBlock("body", 1, lines)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal([]any{block, slots})
	b, _ := json.Marshal([]any{other, otherSlots})
	if string(a) != string(b) {
		t.Fatal("identical geometry is not deterministic")
	}
}

func TestUnknownSourceFontCreatesBlockingEvidenceWithoutRetainingFontBytes(t *testing.T) {
	_, _, issues, err := paragraphBlock("body", 1, []geometryLine{{page: 1, width: 595.32, height: 841.92, parts: []GeometryText{{Text: "不能推測字型", FontFamily: "Undefined", FontSize: 16, Box: Box{X: .1, Y: .2, Width: .5, Height: .03}}}}})
	if err != nil || len(issues) != 1 || issues[0].Code != "unknown_font_role" || !issues[0].Blocking {
		t.Fatalf("font ambiguity accepted: %v %v", issues, err)
	}
}

func TestParagraphRejectsReorderedPages(t *testing.T) {
	line := geometryLine{width: 595.32, height: 841.92, parts: []GeometryText{{Text: "句子。", FontFamily: "MingLiU", FontSize: 16, Box: Box{X: .1, Y: .2, Width: .3, Height: .03}}}}
	for _, pages := range [][]int{{1, 3, 2}, {1, 2, 1}} {
		var lines []geometryLine
		for _, page := range pages {
			copy := line
			copy.page = page
			lines = append(lines, copy)
		}
		if _, _, _, err := paragraphBlock("body", 1, lines); err == nil {
			t.Fatal("out-of-order continuation accepted")
		}
	}
}

func TestParagraphPreservesColumnAllocationAndIndentation(t *testing.T) {
	lines := []geometryLine{
		{page: 3, width: 595.32, height: 841.92, right: .49, parts: []GeometryText{{Text: "第一行尚未結束", FontFamily: "MingLiU", FontSize: 16, Box: Box{X: .15, Y: .2, Width: .3, Height: .03}}}},
		{page: 3, width: 595.32, height: 841.92, right: .49, parts: []GeometryText{{Text: "下一行。", FontFamily: "MingLiU", FontSize: 16, Box: Box{X: .1, Y: .23, Width: .2, Height: .03}}}},
	}
	block, slots, _, err := paragraphBlock("lyrics", 1, lines)
	if err != nil || len(slots) != 1 {
		t.Fatal(err)
	}
	if got := slots[0].Box.X + slots[0].Box.Width; got < .489999 || got > .490001 {
		t.Fatalf("left column extended into right column: %v", got)
	}
	if got := block.Style.FirstLineIndent; got < 1.8603 || got > 1.8605 {
		t.Fatalf("source first-line indentation lost: %v", got)
	}
}

func TestParagraphKeepsTerminalWhitespaceWithoutEmptySentences(t *testing.T) {
	block, _, _, err := paragraphBlock("body", 1, []geometryLine{{page: 1, width: 595.32, height: 841.92, parts: []GeometryText{{Text: "第一句。 \n第二句！ ", FontFamily: "MingLiU", FontSize: 16, Box: Box{X: .1, Y: .2, Width: .5, Height: .03}}}}})
	if err != nil || len(block.Sentences) != 2 {
		t.Fatalf("whitespace created an empty sentence: %v", err)
	}
	if block.Sentences[0].Spans[0].Text != "第一句。 \n" || block.Sentences[1].Spans[0].Text != "第二句！ " {
		t.Fatal("source whitespace lost")
	}
}

func TestSourceLinesGroupMixedFontRunsInColumnReadingOrder(t *testing.T) {
	page := GeometryPage{Number: 3, Width: 595.32, Height: 841.92, Texts: []GeometryText{
		{Text: "右欄", FontFamily: "MingLiU", FontSize: 16, Box: Box{X: .55, Y: .1, Width: .3, Height: .02}},
		{Text: "經文", FontFamily: "DFKaiShu", FontSize: 12, Box: Box{X: .25, Y: .104, Width: .1, Height: .015}},
		{Text: "左欄", FontFamily: "MingLiU", FontSize: 16, Box: Box{X: .1, Y: .1, Width: .1, Height: .02}},
		{Text: "下一行", FontFamily: "MingLiU", FontSize: 16, Box: Box{X: .1, Y: .14, Width: .2, Height: .02}},
	}}
	lines, err := sourceLines(page, true)
	if err != nil || len(lines) != 3 || len(lines[0].parts) != 2 || lines[0].parts[0].Text != "左欄" || lines[0].parts[1].Text != "經文" || lines[2].parts[0].Text != "右欄" || lines[0].right != .49 || lines[2].right != .95 {
		t.Fatalf("source line grouping lost runs or columns: %v", err)
	}
	if page.Texts[0].Text != "右欄" {
		t.Fatal("grouping mutated source")
	}
}

func TestSourceLinesAnchorMixedSizesToLargestRunNotEarlierSmallLabel(t *testing.T) {
	page := GeometryPage{Number: 1, Width: templateV1Width, Height: templateV1Height, Texts: []GeometryText{
		{Text: "固定標籤", FontFamily: "DFXingShu", FontSize: 14, Box: Box{X: .07, Y: 205.974 / templateV1Height, Width: .14, Height: 14.04 / templateV1Height}},
		{Text: "主題", FontFamily: "DFXingShu", FontSize: 18, Box: Box{X: .37, Y: 209.882 / templateV1Height, Width: .18, Height: 18 / templateV1Height}},
		{Text: "!", FontFamily: "TimesNewRoman", FontSize: 14, Box: Box{X: .8, Y: 211.790 / templateV1Height, Width: .01, Height: 14 / templateV1Height}},
		{Text: "副標", FontFamily: "DFXingShu", FontSize: 14, Box: Box{X: .58, Y: 213.054 / templateV1Height, Width: .2, Height: 14.04 / templateV1Height}},
	}}
	lines, err := sourceLines(page, false)
	if err != nil || len(lines) != 1 || lineText(lines[0]) != "固定標籤主題副標!" {
		t.Fatalf("inline punctuation separated by earlier smaller run: lines=%d err=%v", len(lines), err)
	}
}

func TestParagraphPreservesHangingIndentation(t *testing.T) {
	lines := []geometryLine{
		{page: 1, width: 595.32, height: 841.92, parts: []GeometryText{{Text: "1.第一行", FontFamily: "MingLiU", FontSize: 16, Box: Box{X: .1, Y: .2, Width: .3, Height: .03}}}},
		{page: 1, width: 595.32, height: 841.92, parts: []GeometryText{{Text: "續行。", FontFamily: "MingLiU", FontSize: 16, Box: Box{X: .15, Y: .23, Width: .3, Height: .03}}}},
	}
	block, _, _, err := paragraphBlock("cover", 1, lines)
	if err != nil || block.Style.Indent < 1.86 || block.Style.Indent > 1.861 || block.Style.FirstLineIndent != -block.Style.Indent {
		t.Fatalf("hanging indentation lost: %+v %v", block.Style, err)
	}
}

func TestUnknownFontRunIsNotMergedWithKnownBodyRun(t *testing.T) {
	line := geometryLine{page: 1, width: 595.32, height: 841.92}
	for index, family := range []string{"MingLiU", "Undefined", "MingLiU"} {
		line.parts = append(line.parts, GeometryText{Text: "文字", FontFamily: family, FontSize: 16, Box: Box{X: .1 + float64(index)*.1, Y: .2, Width: .1, Height: .03}})
	}
	block, _, issues, err := paragraphBlock("body", 1, []geometryLine{line})
	if err != nil || len(issues) != 1 || len(block.Sentences[0].Spans) != 3 {
		t.Fatal("unknown font run merged away before review")
	}
}

func TestParagraphLeadingComesFromSourceBaselinesNotDefaultMultiplier(t *testing.T) {
	var lines []geometryLine
	for index := 0; index < 3; index++ {
		lines = append(lines, geometryLine{page: 1, width: 595.32, height: 841.92, parts: []GeometryText{{Text: "經文", FontFamily: "DFKaiShu", FontSize: 11, Box: Box{X: .2, Y: .79 + float64(index)*11/templateV1Height, Width: .6, Height: 11 / templateV1Height}}}})
	}
	block, _, _, err := paragraphBlock("cover", 1, lines)
	if err != nil || block.Style.LineHeight < 10.9999 || block.Style.LineHeight > 11.0001 {
		t.Fatalf("source leading lost: %+v %v", block.Style, err)
	}
}

func TestParagraphContinuationAcrossHymnColumnsUsesSeparateSlots(t *testing.T) {
	lines := []geometryLine{
		{page: 3, width: 595.32, height: 841.92, right: .49, parts: []GeometryText{{Text: "前半句", FontFamily: "MingLiU", FontSize: 16, Box: Box{X: .1, Y: .8, Width: .3, Height: .03}}}},
		{page: 3, column: 1, width: 595.32, height: 841.92, right: .95, parts: []GeometryText{{Text: "後半句。", FontFamily: "MingLiU", FontSize: 16, Box: Box{X: .55, Y: .1, Width: .3, Height: .03}}}},
	}
	block, slots, _, err := paragraphBlock("lyrics", 1, lines)
	if err != nil || len(block.Sentences) != 1 || len(slots) != 2 || slots[0].ID == slots[1].ID || slots[1].ContinuationOf != slots[0].ID || slots[0].Fragments[0].SentenceID != slots[1].Fragments[0].SentenceID || slots[0].Fragments[0].End != slots[1].Fragments[0].Start || slots[0].Box.Width > .4 || slots[1].Box.X < .5 {
		t.Fatalf("cross-column continuation flattened: %v", err)
	}
}

func TestSentenceEndingKeepsRepeatedPunctuationAndUnicodeClosingBrackets(t *testing.T) {
	block, _, _, err := paragraphBlock("body", 1, []geometryLine{{page: 1, width: 595.32, height: 841.92, parts: []GeometryText{{Text: "《第一句？！》第二句！！", FontFamily: "MingLiU", FontSize: 16, Box: Box{X: .1, Y: .2, Width: .7, Height: .03}}}}})
	if err != nil || len(block.Sentences) != 2 || block.Sentences[0].Spans[0].Text != "《第一句？！》" || block.Sentences[1].Spans[0].Text != "第二句！！" {
		t.Fatal("punctuation became a standalone selectable sentence")
	}
}
