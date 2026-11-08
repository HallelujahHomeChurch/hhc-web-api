package onlinebulletins

import (
	"math"
	"testing"
)

func TestCoverCanonicalMetadataMapsExactSourceRunsNotGuessedGlyphWidths(t *testing.T) {
	page := GeometryPage{Number: 1, Width: templateV1Width, Height: templateV1Height}
	add := func(text string, x, y, width, size float64) {
		page.Texts = append(page.Texts, GeometryText{Text: text, FontFamily: "DFXingShu", FontSize: size, Box: Box{X: x, Y: y, Width: width, Height: size / page.Height}})
	}
	add("Sep.20.2026 ", .10, .18, .12, 11)
	add("第", .22, .18, .02, 11)
	add("1739", .25, .18, .04, 11)
	add("期", .30, .18, .02, 11)
	add("主題", .30, .25, .20, 18)
	add("～", .50, .255, .02, 12)
	add("副標", .52, .255, .20, 14)
	metadata := CanonicalMetadata{Title: "主題", Subtitle: "～副標", IssueNumber: 1739, Date: "2026-09-20"}
	slots, issues, err := coverMetadataSlots(page, metadata)
	if err != nil || len(issues) != 0 || len(slots) != 4 {
		t.Fatalf("canonical slots: %v %v", err, issues)
	}
	byElement := map[string]FixedSlot{}
	for _, slot := range slots {
		byElement[slot.Element] = slot
	}
	if byElement["title"].Style.FontSize != 18 || byElement["subtitle"].Style.FontSize != 14 || byElement["title"].Box.X != .30 || byElement["subtitle"].Box.X != .50 {
		t.Fatal("source canonical sizes/positions lost")
	}
	page.Texts[0].Text = "Oct.04.2026 "
	metadata.Date = "2026-10-04"
	if padded, findings, err := coverMetadataSlots(page, metadata); err != nil || len(findings) != 0 || len(padded) != 4 {
		t.Fatal("a zero-padded source day was mistaken for a different canonical date")
	}
	metadata.Title = "修改後不同主題"
	_, issues, err = coverMetadataSlots(page, metadata)
	if err != nil || len(issues) != 1 || issues[0].Code != "canonical_source_mismatch" || !issues[0].Blocking {
		t.Fatal("canonical/source mismatch silently accepted")
	}
	metadata.Date = "2026-02-30"
	if _, _, err = coverMetadataSlots(page, metadata); err == nil {
		t.Fatal("invalid canonical date accepted")
	}
	if wholeSourceRuns([]GeometryText{{Text: "主題副標"}}, "主題") != nil {
		t.Fatal("partial source run was assigned fabricated glyph positions")
	}
}

func TestCanonicalTitleRetainsObservedSourceTrackingWithoutEditedTextAutoFit(t *testing.T) {
	page := GeometryPage{Number: 1, Width: templateV1Width, Height: templateV1Height, Texts: []GeometryText{
		{Text: "來源主題文字八字", FontFamily: "DFXingShu", FontSize: 18, Box: Box{X: .3, Y: .20, Width: 120 / templateV1Width, Height: 18 / templateV1Height}},
		{Text: "Sep.20.2026", FontFamily: "DFXingShu", FontSize: 11, Box: Box{X: .1, Y: .17, Width: .2, Height: .015}},
		{Text: "第1739期", FontFamily: "DFXingShu", FontSize: 11, Box: Box{X: .4, Y: .17, Width: .2, Height: .015}},
	}}
	metadata := CanonicalMetadata{Title: "來源主題文字八字", IssueNumber: 1739, Date: "2026-09-20"}
	slots, issues, err := coverMetadataSlots(page, metadata)
	if err != nil || len(issues) != 0 {
		t.Fatal("source canonical title unavailable")
	}
	for _, slot := range slots {
		if slot.Element == "title" {
			if math.Abs(slot.Style.LetterSpacing-(-24.0/(7*18))) > 1e-9 || slot.Style.FontSize != 18 || slot.Box.Width != 120/templateV1Width {
				t.Fatal("source compressed title was widened or its glyph size reduced")
			}
		}
	}
	metadata.Title = "後續修改的主題"
	slots, issues, err = coverMetadataSlots(page, metadata)
	if err != nil || len(issues) != 1 {
		t.Fatal("edited canonical source mismatch bypassed review")
	}
	for _, slot := range slots {
		if slot.Element == "title" {
			t.Fatal("edited title received a new auto-fitted source slot")
		}
	}
}

func TestBodyCanonicalArtIsNativeGeometryProposalNotSourceImageAcceptance(t *testing.T) {
	page := GeometryPage{Number: 2, Width: templateV1Width, Height: templateV1Height, Images: []GeometryImage{
		{Box: Box{X: .1026, Y: .09022, Width: .60502, Height: .0625}},
		{Box: Box{X: .51401, Y: .20909, Width: .38107, Height: .03627}},
		{Box: Box{X: 0, Y: 0, Width: 1, Height: 1}},
	}}
	metadata := CanonicalMetadata{Title: "永恆的命定和呼召", Subtitle: "～我們起來建造吧!"}
	slots, issues := bodyCanonicalArtSlots(page, metadata)
	if len(slots) != 3 || len(issues) != 1 || !issues[0].Blocking || issues[0].Code != "inferred_body_art_geometry" {
		t.Fatal("unproven body WordArt was accepted as source proof")
	}
	if issues[0].SourcePage != 2 || issues[0].ComponentID != "" {
		t.Fatal("article artwork review cannot locate its source page without inventing a body component")
	}
	for _, slot := range slots {
		if slot.sourcePage != 2 || !validBox(slot.Box) || !validParagraphStyle(slot.Style) {
			t.Fatal("native art proposal escaped source page or style bounds")
		}
	}
	if slots[0].Element != "title" || slots[1].Element != "subtitle" || slots[2].Element != "topRule" || slots[0].Style.LineHeight != slots[0].Style.FontSize*2 {
		t.Fatal("canonical title/subtitle and header rule lost their native profiles")
	}
	if page.Images[0].Box.X != .1026 || page.Images[1].Box.Y != .20909 {
		t.Fatal("native art proposal mutated source evidence")
	}
	latin := metadata
	latin.Subtitle = "神兒女的13個品格養成和生活規範"
	latinSlots, _ := bodyCanonicalArtSlots(page, latin)
	width := 0.0
	for _, r := range latin.Subtitle {
		if r >= '0' && r <= '9' {
			width += .65
		} else {
			width++
		}
	}
	style := latinSlots[1].Style
	if width*style.FontSize+float64(len([]rune(latin.Subtitle)))*style.LetterSpacing*style.FontSize > latinSlots[1].Box.Width*page.Width {
		t.Fatal("canonical art assumes bold Latin digits are half-width")
	}
	ambiguous := page
	ambiguous.Images = append(append([]GeometryImage(nil), page.Images...), page.Images[0])
	if slots, _ := bodyCanonicalArtSlots(ambiguous, metadata); len(slots) != 0 {
		t.Fatal("ambiguous shadow evidence chose an arbitrary title")
	}
	page.Images = page.Images[2:]
	slots, issues = bodyCanonicalArtSlots(page, metadata)
	if len(slots) != 0 || len(issues) == 0 || !issues[0].Blocking {
		t.Fatal("unknown full-page image fabricated title geometry")
	}
}

func TestBodyCanonicalArtKeepsWideTitleBelowObservedPageNumber(t *testing.T) {
	page := GeometryPage{Number: 2, Width: templateV1Width, Height: templateV1Height,
		Images: []GeometryImage{{Box: Box{X: .112679, Y: .096494, Width: .797118, Height: .054803}}},
		Texts:  []GeometryText{{Text: "1 ", FontSize: 10, Box: Box{X: .876806, Y: .050608, Width: .012467, Height: .014445}}},
	}
	slots, issues := bodyCanonicalArtSlots(page, CanonicalMetadata{Title: "與神的命定和呼召緊密連結、活出永恆生命"})
	if len(slots) != 2 || len(issues) != 1 || !issues[0].Blocking {
		t.Fatal("wide title proposal or its review disappeared")
	}
	if slots[0].Box.Y < page.Texts[0].Box.Y+page.Texts[0].Box.Height+2/page.Height {
		t.Fatal("native title proposal overlaps the source page number")
	}
	if page.Texts[0].Box.Y != .050608 || page.Images[0].Box.Y != .096494 {
		t.Fatal("source evidence was changed to hide the overlap")
	}
}

func TestBodyCanonicalArtLongTitleRetainsBothCanonicalFields(t *testing.T) {
	page := GeometryPage{Number: 2, Width: 612, Height: 792, Images: []GeometryImage{
		{Box: Box{X: .155294119, Y: .091515158, Width: .547450982, Height: .062727271}},
		{Box: Box{X: .461960773, Y: .211666667, Width: .406666662, Height: .037121213}},
	}}
	metadata := CanonicalMetadata{Title: "刻骨銘心、愛的碰觸(恭賀田蓉家園十週年)", Subtitle: "落實神兒女的四樣基本功"}
	slots, issues := bodyCanonicalArtSlots(page, metadata)
	if len(slots) != 3 || slots[0].Element != "title" || slots[1].Element != "subtitle" || len(issues) != 1 || !issues[0].Blocking {
		t.Fatal("long title removed canonical fields instead of proposing a measurable fit")
	}
	if !validParagraphStyle(slots[0].Style) || slots[0].Style.FontSize < 12 || slots[0].Style.FontSize >= page.Images[0].Box.Height*page.Height*.8 {
		t.Fatal("long title fitting must reduce font size within readable style limits")
	}
}

func TestBodyCanonicalArtUsesNative1738HeaderWithoutRasterShadow(t *testing.T) {
	page := GeometryPage{Number: 2, Width: 595.32, Height: 841.92}
	add := func(text string, x, y, width, height, size float64) {
		page.Texts = append(page.Texts, GeometryText{Text: text, FontFamily: "DFXingShu-Bd-HK-BF", FontSize: size, Box: Box{X: x / page.Width, Y: y / page.Height, Width: width / page.Width, Height: height / page.Height}})
	}
	add("敬畏神、謹守祂的誡命", 75.624, 41.972816, 298.891895, 99.984, 100)
	add("～", 308.230, 160.357040, 19.199726, 54.96, 55)
	add("將苦難轉為祝福", 327.430, 160.357040, 192.188327, 54.96, 55)
	metadata := CanonicalMetadata{Title: "敬畏神、謹守祂的誡命", Subtitle: "將苦難轉為祝福"}
	slots, issues := bodyCanonicalArtSlots(page, metadata)
	if len(slots) != 3 {
		t.Fatalf("native title/subtitle disappeared without raster shadows: %+v", slots)
	}
	if slots[0].Element != "title" || slots[0].Box != page.Texts[0].Box || slots[1].Element != "subtitle" || slots[1].Box != page.Texts[2].Box {
		t.Fatal("native canonical text must retain observed source bounds")
	}
	for _, slot := range slots {
		if !validParagraphStyle(slot.Style) {
			t.Fatal("WordArt font size escaped paragraph bounds")
		}
	}
	if len(issues) == 0 || !issues[0].Blocking {
		t.Fatal("native placement bypassed visual review")
	}
	ambiguous := page
	ambiguous.Texts = append(append([]GeometryText(nil), page.Texts...), page.Texts[0])
	ambiguous.Texts[len(ambiguous.Texts)-1].Box.Y = .18
	if rejected, _ := bodyCanonicalArtSlots(ambiguous, metadata); len(rejected) != 0 {
		t.Fatal("ambiguous native title chose arbitrary source bounds")
	}
	ambiguous.Texts[len(ambiguous.Texts)-1].Box.Y = page.Texts[0].Box.Y
	ambiguous.Texts[len(ambiguous.Texts)-1].Box.X = .45
	if rejected, _ := bodyCanonicalArtSlots(ambiguous, metadata); len(rejected) != 0 {
		t.Fatal("same-line duplicate title chose arbitrary source bounds")
	}
	metadata.Title = "不同主題"
	if rejected, _ := bodyCanonicalArtSlots(page, metadata); len(rejected) != 0 {
		t.Fatal("mismatched native title silently substituted")
	}
}

func TestBodyCanonicalArtRejectedProposalKeepsObservedSourcePage(t *testing.T) {
	page := GeometryPage{Number: 3, Width: templateV1Width, Height: templateV1Height}
	slots, issues := bodyCanonicalArtSlots(page, CanonicalMetadata{Title: "主題"})
	if len(slots) != 0 || len(issues) != 1 || issues[0].SourcePage != 3 {
		t.Fatal("rejected artwork proposal invented a different source page")
	}
}

func TestBodyCanonicalArtUsesConnectedSubtitleEnvelope(t *testing.T) {
	page := GeometryPage{Number: 2, Width: 612, Height: 792, Images: []GeometryImage{
		{Box: Box{X: .15, Y: .10, Width: .50, Height: .055}},
		{Box: Box{X: .255, Y: .217, Width: .317, Height: .034}},
		{Box: Box{X: .428, Y: .219, Width: .155, Height: .037}},
		{Box: Box{X: .446, Y: .217, Width: .417, Height: .034}},
		{Box: Box{X: .735, Y: .219, Width: .128, Height: .037}},
	}}
	metadata := CanonicalMetadata{Title: "需要完整圖層的新主題", Subtitle: "需要完整圖層範圍的副標題"}
	slots, issues := bodyCanonicalArtSlots(page, metadata)
	if len(slots) != 3 || slots[1].Box.X > .28 || slots[1].Box.Width < .58 || len(issues) != 1 {
		t.Fatal("subtitle placement used one WordArt fragment instead of connected source layers")
	}
	for i, j := 0, len(page.Images)-1; i < j; i, j = i+1, j-1 {
		page.Images[i], page.Images[j] = page.Images[j], page.Images[i]
	}
	other, _ := bodyCanonicalArtSlots(page, metadata)
	if len(other) != 3 || other[1] != slots[1] {
		t.Fatal("source image ordering changed subtitle placement")
	}
	duplicate := page
	duplicate.Images = append(append([]GeometryImage(nil), page.Images...), page.Images[0])
	if rejected, _ := bodyCanonicalArtSlots(duplicate, metadata); len(rejected) != 0 {
		t.Fatal("duplicate secondary artwork was treated as unique evidence")
	}
	page.Images = append(page.Images, GeometryImage{Box: Box{X: .02, Y: .217, Width: .10, Height: .034}})
	if rejected, findings := bodyCanonicalArtSlots(page, metadata); len(rejected) != 0 || len(findings) != 1 {
		t.Fatal("disconnected artwork was included in the canonical header envelope")
	}
}
