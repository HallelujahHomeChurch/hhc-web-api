package onlinebulletins

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestBodyUsesFrameForPunctuatedTitleAndKeepsInteriorTilde(t *testing.T) {
	page := GeometryPage{Number: 2, Width: templateV1Width, Height: templateV1Height}
	add := func(text string, x, y, width float64) {
		page.Texts = append(page.Texts, GeometryText{Text: text, FontFamily: "MingLiU", FontSize: 14, Box: Box{X: x, Y: y, Width: width, Height: .02}})
	}
	add("第一段", .1, .33, .1)
	add("正文。", .14, .36, .2)
	add("榮耀的呼召～作教育山頭時代的守望者!", .1, .40, .5)
	add("～", .61, .40, .024)
	add("祥德(Ark)", .634, .40, .16)
	add("第二段正文。", .1, .43, .5) // No first-line indentation required.
	page.Frames = []GeometryFrame{{Element: "bodyHeadingFrame", Box: Box{X: .098, Y: .398, Width: .505, Height: .024}}}
	mapping, err := bodyComponents([]GeometryPage{page})
	if err != nil || len(mapping.components) != 2 {
		t.Fatalf("boxed title missed: %v; components=%d", err, len(mapping.components))
	}
	body := mapping.components[1].BodySection
	if privateBlockText(body.Title) != "榮耀的呼召～作教育山頭時代的守望者!" || len(body.Contributors) != 1 || privateBlockText(body.Contributors[0].Name) != "祥德(Ark)" {
		t.Fatalf("frame boundary lost: %+v", body)
	}
	for _, issue := range mapping.issues {
		if issue.ComponentID == mapping.components[1].ID && (issue.Code == "ambiguous_body_heading" || issue.Code == "ambiguous_inline_speaker") {
			t.Fatalf("measured frame reported as ambiguous: %+v", issue)
		}
	}
}

func TestBodyFrameResolvesMergedPunctuatedCJKButRetainsGeometryReview(t *testing.T) {
	text := "珍惜所有，不視為當然～淑菁"
	page := GeometryPage{Number: 2, Width: templateV1Width, Height: templateV1Height,
		Texts: []GeometryText{
			{Text: text, FontFamily: "DFXingShu", FontSize: 14, Box: Box{X: .1, Y: .4, Width: float64(len([]rune(text))) * 14 / templateV1Width, Height: .02}},
			{Text: "正文。", FontFamily: "MingLiU", FontSize: 14, Box: Box{X: .14, Y: .43, Width: .3, Height: .02}},
		}, Frames: []GeometryFrame{{Element: "bodyHeadingFrame", Box: Box{X: .099, Y: .399, Width: .002 + 10*14/templateV1Width, Height: .023}}}}
	mapping, err := bodyComponents([]GeometryPage{page})
	if err != nil {
		t.Fatal(err)
	}
	body := mapping.components[0].BodySection
	if privateBlockText(body.Title) != "珍惜所有，不視為當然" || len(body.Contributors) != 1 || privateBlockText(body.Contributors[0].Name) != "淑菁" {
		t.Fatalf("frame speaker not resolved: %+v", body)
	}
	for _, issue := range mapping.issues {
		if issue.Code == "inferred_inline_speaker_geometry" {
			return
		}
	}
	t.Fatal("estimated source bounds lost their review marker")
}

func TestBodyFrameSplitsMixedSpeakerUsingFrameNotLatinGrid(t *testing.T) {
	page := GeometryPage{Number: 2, Width: templateV1Width, Height: templateV1Height,
		Texts: []GeometryText{
			{Text: "活出〝撕裂心腸〞的態度～祥德(Ark)", FontFamily: "DFXingShu", FontSize: 14, Box: Box{X: .115138, Y: .4, Width: .388930, Height: 14.04 / templateV1Height}},
			{Text: "正文。", FontFamily: "MingLiU", FontSize: 14, Box: Box{X: .15, Y: .43, Width: .3, Height: .02}},
		}, Frames: []GeometryFrame{{Element: "bodyHeadingFrame", Box: Box{X: .114204, Y: .396, Width: .260174, Height: .024}}}}
	mapping, err := bodyComponents([]GeometryPage{page})
	if err != nil {
		t.Fatal(err)
	}
	body := mapping.components[0].BodySection
	if privateBlockText(body.Title) != "活出〝撕裂心腸〞的態度" || len(body.Contributors) != 1 || privateBlockText(body.Contributors[0].Name) != "祥德(Ark)" {
		t.Fatalf("mixed speaker lost: %+v", body)
	}
}

func TestBodyPageNumbersUseMeasuredSourcePlacementAndValidatePrintedSequence(t *testing.T) {
	pages := []GeometryPage{}
	for number := 2; number <= 3; number++ {
		page := GeometryPage{Number: number, Width: templateV1Width, Height: templateV1Height, Texts: []GeometryText{
			{Text: strconv.Itoa(number - 1), FontFamily: "Times", FontSize: 10, Box: Box{X: .48, Y: .035, Width: .02, Height: .015}},
			{Text: "主題", FontFamily: "DFXingShu", FontSize: 14, Box: Box{X: .1, Y: .33, Width: .12, Height: .02}},
			{Text: "正文。", FontFamily: "MingLiU", FontSize: 14, Box: Box{X: .14, Y: .36, Width: .6, Height: .02}},
		}}
		pages = append(pages, page)
	}
	mapping, err := bodyComponents(pages)
	if err != nil || len(mapping.fixed) != 2 {
		t.Fatalf("measured page numbers missing: %v", err)
	}
	for index, slot := range mapping.fixed {
		if slot.Element != "pageNumber" || slot.sourcePage != index+2 || slot.Box.X != .48 || slot.Style.FontSize != 10 {
			t.Fatal("page number source placement changed")
		}
	}
	pages[0].Texts[0].Text = "7"
	if _, err := bodyComponents(pages); err == nil {
		t.Fatal("wrong printed page number accepted as code-owned sequence")
	}
}

func TestBodyPreservesTextInsidePageNumberBand(t *testing.T) {
	for _, content := range []string{"接續內容。", "2", "1743"} {
		for _, height := range []float64{792, templateV1Height} {
			width := templateV1Width
			if height == 792 {
				width = 612
			}
			pages := []GeometryPage{
				{Number: 2, Width: width, Height: height, Texts: []GeometryText{
					{Text: "1", FontFamily: "Times", FontSize: 10, Box: Box{X: .85, Y: 40 / height, Width: .02, Height: .015}},
					{Text: "主題", FontFamily: "DFXingShu", FontSize: 14, Box: Box{X: .1, Y: .4, Width: .1, Height: .02}},
					{Text: "跨頁的正文", FontFamily: "MingLiU", FontSize: 14, Box: Box{X: .14, Y: .44, Width: .5, Height: .02}},
				}},
				{Number: 3, Width: width, Height: height, Texts: []GeometryText{
					{Text: "2", FontFamily: "Times", FontSize: 10, Box: Box{X: .85, Y: 40 / height, Width: .02, Height: .015}},
					{Text: content, FontFamily: "MingLiU", FontSize: 14, Box: Box{X: .1, Y: 64 / height, Width: .5, Height: .02}},
				}},
			}
			mapping, err := bodyComponents(pages)
			if err != nil {
				t.Fatal(err)
			}
			text := ""
			for _, component := range mapping.components {
				for _, block := range component.BodySection.Blocks {
					text += privateBlockText(block)
				}
			}
			if !strings.HasSuffix(text, content) || len(mapping.fixed) != 2 {
				t.Fatal("top-of-page content or page numbers lost")
			}
		}
	}

}

func TestBodyKeepsParagraphContinuationsAndAmbiguousSectionReview(t *testing.T) {
	pages := []GeometryPage{{Number: 2, Width: templateV1Width, Height: templateV1Height}, {Number: 3, Width: templateV1Width, Height: templateV1Height}}
	add := func(page int, text, font string, x, y float64) {
		pages[page].Texts = append(pages[page].Texts, GeometryText{Text: text, FontFamily: font, FontSize: 14, Box: Box{X: x, Y: y, Width: .60, Height: .02}})
	}
	add(0, "講員：姓名", "MingLiU", .1, .30)
	add(0, "主題～講員", "DFXingShu", .075, .33)
	add(0, "第一段尚未", "MingLiU", .115, .36)
	add(0, "結束，跨頁", "MingLiU", .075, .39)
	add(1, "接續同一句。", "MingLiU", .075, .09)
	add(1, "第二段。", "MingLiU", .115, .12)
	add(1, "另一個主題", "Undefined", .075, .15)
	add(1, "另一段正文。", "MingLiU", .115, .18)
	mapping, err := bodyComponents(pages)
	components, slots, header, issues := mapping.components, mapping.slots, mapping.header, mapping.issues
	if err != nil || len(components) != 2 || len(header) != 1 || len(issues) < 2 {
		t.Fatalf("body structure: %v %v", err, issues)
	}
	if components[0].BodySection.Kind != "unknown" || len(components[0].BodySection.Blocks) != 2 || len(components[1].BodySection.Blocks) != 1 {
		t.Fatal("ambiguous kind guessed or paragraph boundary lost")
	}
	text, _ := json.Marshal(components)
	if !strings.Contains(string(text), "第一段尚未結束，跨頁接續同一句。") || strings.Contains(string(text), "講員：姓名") {
		t.Fatal("cross-page sentence or separate header lost")
	}
	continued := false
	for _, slot := range slots {
		if slot.ContinuationOf != "" {
			continued = true
		}
	}
	if !continued {
		t.Fatal("paragraph continuation geometry lost")
	}
}

func TestBodyRejectsMissingTitleOrOutOfOrderPages(t *testing.T) {
	if _, err := bodyComponents(nil); err == nil {
		t.Fatal("missing body accepted")
	}
	pages := []GeometryPage{{Number: 3, Width: templateV1Width, Height: templateV1Height}, {Number: 2, Width: templateV1Width, Height: templateV1Height}}
	if _, err := bodyComponents(pages); err == nil {
		t.Fatal("body pages reordered")
	}
}

func TestBodySeparatesLargeCanonicalWordArtBeforeParagraphValidation(t *testing.T) {
	page := GeometryPage{Number: 2, Width: templateV1Width, Height: templateV1Height, Texts: []GeometryText{
		{Text: "大型主標", FontFamily: "DFXingShu", FontSize: 100, Box: Box{X: .1, Y: .06, Width: .7, Height: .12}},
		{Text: "段落標題", FontFamily: "DFXingShu", FontSize: 14, Box: Box{X: .1, Y: .33, Width: .2, Height: .02}},
		{Text: "正文。", FontFamily: "MingLiU", FontSize: 14, Box: Box{X: .14, Y: .36, Width: .6, Height: .02}},
	}}
	mapping, err := bodyComponents([]GeometryPage{page})
	if err != nil || len(mapping.header) != 1 || len(mapping.components) != 1 {
		t.Fatalf("canonical art blocked body: %v", err)
	}
	page.Texts[2].FontSize = 100
	if _, err := bodyComponents([]GeometryPage{page}); err == nil {
		t.Fatal("oversized body paragraph accepted")
	}
}

func TestBodyDoesNotCutValidLastLineAtAnAssumedFooter(t *testing.T) {
	page := GeometryPage{Number: 2, Width: templateV1Width, Height: templateV1Height, Texts: []GeometryText{
		{Text: "主題", FontFamily: "DFXingShu", FontSize: 14, Box: Box{X: .115, Y: .33, Width: .3, Height: .02}},
		{Text: "保留最後一句。", FontFamily: "MingLiU", FontSize: 14, Box: Box{X: .155, Y: 752.29 / templateV1Height, Width: .65, Height: 14.04 / templateV1Height}},
	}}
	mapping, err := bodyComponents([]GeometryPage{page})
	if err != nil || len(mapping.components[0].BodySection.Blocks) != 1 {
		t.Fatalf("valid source bottom line was cut: %v", err)
	}
}

func TestBodySeparatesInlineSpeakerOnlyAtWholeSourceRunBoundary(t *testing.T) {
	page := GeometryPage{Number: 2, Width: templateV1Width, Height: templateV1Height, Texts: []GeometryText{
		{Text: "章節標題", FontFamily: "DFXingShu", FontSize: 14, Box: Box{X: .115, Y: .33, Width: .12, Height: .02}},
		{Text: "～", FontFamily: "DFKaiShu", FontSize: 14, Box: Box{X: .235, Y: .33, Width: .024, Height: .02}},
		{Text: "講員姓名", FontFamily: "DFXingShu", FontSize: 14, Box: Box{X: .259, Y: .33, Width: .10, Height: .02}},
		{Text: "正文。", FontFamily: "MingLiU", FontSize: 14, Box: Box{X: .155, Y: .36, Width: .60, Height: .02}},
	}}
	mapping, err := bodyComponents([]GeometryPage{page})
	if err != nil || len(mapping.fixed) != 1 || mapping.fixed[0].Element != "speakerSeparator" {
		t.Fatalf("inline speaker mapping: %v", err)
	}
	body := mapping.components[0].BodySection
	if privateBlockText(body.Title) != "章節標題" || len(body.Contributors) != 1 || privateBlockText(body.Contributors[0].Name) != "講員姓名" || len(mapping.slots) != 3 {
		t.Fatal("title, speaker and body were not separate anchors")
	}
	if mapping.slots[0].Box.Width > .121 || mapping.slots[1].Box.X != .259 {
		t.Fatal("inline source slots expanded across the whole body column")
	}
}

func TestSingleRunCJKSpeakerGeometryIsProposedAndAlwaysRequiresReview(t *testing.T) {
	page := GeometryPage{Number: 2, Width: templateV1Width, Height: templateV1Height, Texts: []GeometryText{
		{Text: "主題～講員", FontFamily: "Undefined", FontSize: 14, Box: Box{X: .115, Y: .33, Width: 5 * 14.04 / templateV1Width, Height: 14.04 / templateV1Height}},
		{Text: "正文。", FontFamily: "MingLiU", FontSize: 14, Box: Box{X: .155, Y: .36, Width: .6, Height: .02}},
	}}
	mapping, err := bodyComponents([]GeometryPage{page})
	if err != nil || len(mapping.components[0].BodySection.Contributors) != 1 || len(mapping.fixed) != 1 {
		t.Fatalf("reviewable CJK split missing: %v", err)
	}
	found := false
	for _, issue := range mapping.issues {
		if issue.Code == "inferred_inline_speaker_geometry" && issue.Blocking {
			found = true
		}
	}
	if !found {
		t.Fatal("estimated glyph boundaries were accepted as measured geometry")
	}
	page.Texts[0].Text = "主題～Blue"
	mapping, err = bodyComponents([]GeometryPage{page})
	if err != nil || len(mapping.components[0].BodySection.Contributors) != 0 {
		t.Fatal("proportional Latin run used the CJK grid assumption")
	}
}

func TestBodyPreservesAmbiguousMultipleSpeakerSeparators(t *testing.T) {
	page := GeometryPage{Number: 2, Width: templateV1Width, Height: templateV1Height}
	for index, text := range []string{"主題", "～", "講員", "～", "補充"} {
		page.Texts = append(page.Texts, GeometryText{Text: text, FontFamily: "DFXingShu", FontSize: 14, Box: Box{X: .115 + float64(index)*.05, Y: .33, Width: .05, Height: .02}})
	}
	page.Texts = append(page.Texts, GeometryText{Text: "正文。", FontFamily: "MingLiU", FontSize: 14, Box: Box{X: .155, Y: .36, Width: .6, Height: .02}})
	mapping, err := bodyComponents([]GeometryPage{page})
	if err != nil {
		t.Fatal(err)
	}
	body := mapping.components[0].BodySection
	if privateBlockText(body.Title) != "主題～講員～補充" || len(body.Contributors) != 0 || len(mapping.fixed) != 0 {
		t.Fatal("ambiguous title was partially removed or assigned to a speaker")
	}
	for _, issue := range mapping.issues {
		if issue.Code == "ambiguous_inline_speaker" && issue.Blocking {
			return
		}
	}
	t.Fatal("ambiguous speaker did not require review")
}
