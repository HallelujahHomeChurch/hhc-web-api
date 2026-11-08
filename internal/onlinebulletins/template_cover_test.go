package onlinebulletins

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestVerseReferenceAllowsSingleChapterBooksOnlyWithoutChapter(t *testing.T) {
	for _, reference := range []string{"(猶/20-21)。", "(猶一/20-21)。", "(門/6)。", "(徒廿/22-24)。", "(詩篇廿三篇)"} {
		if !verseReferenceEnd.MatchString(reference) {
			t.Fatal("valid reference rejected", reference)
		}
	}
	if verseReferenceEnd.MatchString("(徒/22-24)。") {
		t.Fatal("missing chapter accepted")
	}
}

func TestCoverVerseUsesObservedFooterBoundary(t *testing.T) {
	page := candidateGeometry().Pages[0]
	page.Texts = append(page.Texts,
		GeometryText{Text: "經文末行。(徒廿/22-24)。", FontFamily: "MingLiU", FontSize: 10, Box: Box{X: .2, Y: .86, Width: .6, Height: .012}},
		GeometryText{Text: "教會地址：測試地址", FontFamily: "MingLiU", FontSize: 10, Box: Box{X: .06, Y: .90, Width: .3, Height: .012}},
	)
	component, _, _, _, err := coverComponent(page)
	if err != nil {
		t.Fatal(err)
	}
	text := privateBlockText(component.Cover.WeeklyVerses[0])
	if !strings.HasSuffix(text, "經文末行。(徒廿/22-24)。") || strings.Contains(text, "測試地址") {
		t.Fatalf("verse truncated or footer included: %q", text)
	}
}

func TestCoverRejectsUnanchoredFooterInsteadOfSilentlyTruncating(t *testing.T) {
	page := candidateGeometry().Pages[0]
	filtered := page.Texts[:0]
	for _, part := range page.Texts {
		if !strings.Contains(part.Text, "教會地址") {
			filtered = append(filtered, part)
		}
	}
	page.Texts = filtered
	if _, _, _, _, err := coverComponent(page); err == nil {
		t.Fatal("unanchored footer silently accepted")
	}
}

func TestCoverProposesOutlinedVerseLabelOnlyWithBoundedCitation(t *testing.T) {
	page := candidateGeometry().Pages[0]
	texts := []GeometryText{}
	for _, part := range page.Texts {
		if part.Text == "本週金句" {
			continue
		}
		if part.Text == "KEEP_verse。" {
			part.Text = "經文內容。(詩廿七/4-6)。"
			part.FontSize = 10
		}
		texts = append(texts, part)
	}
	page.Texts = texts
	component, _, fixed, issues, err := coverComponent(page)
	if err != nil || component.Cover == nil || len(component.Cover.WeeklyVerses) != 1 || len(fixed) != 5 {
		t.Fatalf("outlined verse: %v", err)
	}
	if len(issues) != 1 || issues[0].Code != "unmapped_fixed_template" || !issues[0].Blocking {
		t.Fatal("inferred label lacks review", issues)
	}
	if privateBlockText(component.Cover.WeeklyVerses[0]) != "經文內容。(詩廿七/4-6)。" {
		t.Fatal("verse text changed")
	}
	for i := range page.Texts {
		if page.Texts[i].Text == "經文內容。(詩廿七/4-6)。" {
			page.Texts[i].Text = "普通文字，沒有出處。"
		}
	}
	if _, _, _, _, err = coverComponent(page); err == nil {
		t.Fatal("missing citation accepted")
	}
}

func TestCoverCIDSongNumbersRequireEmbeddedFontEvidence(t *testing.T) {
	page := candidateGeometry().Pages[0]
	for i := range page.Texts {
		if strings.Contains("①②③", page.Texts[i].Text) {
			page.Texts[i].Text = string('\uf08c' + ([]rune(page.Texts[i].Text)[0] - '①'))
			page.Texts[i].FontFamily = "CIDFont+F9"
		}
	}
	if _, _, _, _, err := coverComponent(page); err == nil {
		t.Fatal("CID alias guessed as Wingdings")
	}
	doc := GeometryDocument{Pages: []GeometryPage{page}}
	if err := restoreEmbeddedFontNames(&doc, []byte(`[{"CIDFont+F9":["Wingdings"]}]`)); err != nil {
		t.Fatal(err)
	}
	component, _, _, _, err := coverComponent(doc.Pages[0])
	if err != nil || component.Cover == nil || len(component.Cover.Worship) != 3 {
		t.Fatalf("CID sequence: %v", err)
	}
	if !strings.HasPrefix(privateBlockText(component.Cover.Worship[0].Blocks[0]), "➊") {
		t.Fatal("verified numbering lost")
	}
}

func TestOutlinedVerseLabelResidueDoesNotRejectOrEnterScripture(t *testing.T) {
	page := candidateGeometry().Pages[0]
	texts := []GeometryText{}
	for _, part := range page.Texts {
		if part.Text == "本週金句" {
			continue
		}
		if part.Text == "KEEP_verse。" {
			part.Text = "經文內容"
			part.FontSize = 10
			part.Box = Box{X: .23, Y: .842264, Width: .438, Height: .013066}
		}
		texts = append(texts, part)
	}
	page.Texts = append(texts,
		GeometryText{Text: "。", FontFamily: "MingLiU", FontSize: 10, Box: Box{X: .1, Y: .70, Width: .02, Height: .013066}},
		GeometryText{Text: "(士六/16)", FontFamily: "DFKaiShu", FontSize: 8, Box: Box{X: .67, Y: .844197, Width: .06, Height: .010652}},
		GeometryText{Text: "。", FontFamily: "DFKaiShu", FontSize: 10, Box: Box{X: .75, Y: .842264, Width: .02, Height: .013066}},
		GeometryText{Text: "。", FontFamily: "DFKaiShu", FontSize: 10, Box: Box{X: .103, Y: .835900, Width: .02, Height: .013066}},
	)
	component, _, _, issues, err := coverComponent(page)
	if err != nil {
		t.Fatal("outlined label punctuation rejected the cover", err)
	}
	if privateBlockText(component.Cover.WeeklyVerses[0]) != "經文內容(士六/16)。" {
		t.Fatal("outlined label residue entered scripture")
	}
	question := component.Cover.WordQuestions[len(component.Cover.WordQuestions)-1]
	if !strings.HasSuffix(privateBlockText(question.Blocks[len(question.Blocks)-1]), "。") {
		t.Fatal("punctuation outside the inferred verse band was discarded")
	}
	if len(issues) != 1 || issues[0].Code != "unmapped_fixed_template" {
		t.Fatal("inferred label review lost", issues)
	}
	page.Texts[len(page.Texts)-1].Text = "未識別文字"
	if _, _, _, _, err = coverComponent(page); err == nil {
		t.Fatal("unknown left-column text silently discarded")
	}
}

func TestCoverComponentsRetainVariableListsAndInlineSongLayout(t *testing.T) {
	for _, questions := range []int{6, 8, 10, 11} {
		page := GeometryPage{Number: 1, Width: templateV1Width, Height: templateV1Height}
		add := func(text string, x, y, width float64) {
			page.Texts = append(page.Texts, GeometryText{Text: text, FontFamily: "MingLiU", FontSize: 12, Box: Box{X: x, Y: y, Width: width, Height: .015}})
		}
		for _, label := range []struct {
			text string
			y    float64
		}{{"Welcome", .28}, {"Worship", .31}, {"Work", .34}, {"Word", .42}} {
			if questions == 8 && label.text == "Welcome" {
				add(label.text, .1, label.y, .08)
				add("：歡迎句。", .19, label.y, .23)
			} else if questions < 10 {
				add(label.text, .1, label.y, .08)
				colon := ":"
				if questions == 8 {
					colon = "："
				}
				add(colon, .19, label.y, .015)
			} else {
				add(label.text+"：", .1, label.y, .1)
			}
		}
		if questions != 8 {
			add("歡迎句。", .22, .28, .2)
		}
		for index, marker := range []string{"①", "②", "③"} {
			if questions == 6 {
				marker = string('\uf08c' + rune(index))
			}
			add(marker, .22+float64(index)*.2, .31, .025)
			if questions == 6 {
				page.Texts[len(page.Texts)-1].FontFamily = "Wingdings"
			}
			add(fmt.Sprintf("歌曲%d/新", index+1), .25+float64(index)*.2, .31, .14)
		}
		for index := 0; index < 3; index++ {
			add(fmt.Sprintf("%d.工作句。", index+1), .22, .34+float64(index)*.024, .5)
		}
		for index := 0; index < questions; index++ {
			add(fmt.Sprintf("%d.問題句？", index+1), .08, .445+float64(index)*.027, .7)
		}
		add("本週金句", .08, .80, .09)
		add("第一行經文", .20, .784, .6)
		page.Texts[len(page.Texts)-1].FontFamily = "Undefined"
		add("第二行經文。", .20, .803, .6)
		page.Texts[len(page.Texts)-1].FontFamily = "DFGirl"
		add("教會地址：固定頁尾不應進動態內容", .08, .87, .6)
		component, slots, fixed, issues, err := coverComponent(page)
		if err != nil || len(issues) != 0 || component.Cover == nil || len(component.Cover.Worship) != 3 || len(component.Cover.Work) != 3 || len(component.Cover.WordQuestions) != questions || len(fixed) != 5 {
			t.Fatalf("cover component questions=%d: %v %v", questions, issues, err)
		}
		if slots[1].BlockID != component.Cover.Worship[0].Blocks[0].ID || slots[len(slots)-1].BlockID != component.Cover.WeeklyVerses[0].ID {
			t.Fatal("paper keyboard order does not follow semantic cover order")
		}
		for _, sentence := range component.Cover.WeeklyVerses[0].Sentences {
			for _, span := range sentence.Spans {
				if span.FontRole != "scripture" {
					t.Fatalf("weekly verse must be scripture independently of PDF font: %s", span.FontRole)
				}
			}
		}
		for _, label := range fixed {
			if label.Style.LineHeight != math.Max(label.Style.FontSize, label.Box.Height*page.Height) {
				t.Fatal("fixed label leading is not source-sized")
			}
		}
		var text strings.Builder
		for _, blocks := range [][]Block{component.Cover.Welcome, component.Cover.WeeklyVerses} {
			for _, block := range blocks {
				for _, sentence := range block.Sentences {
					for _, span := range sentence.Spans {
						text.WriteString(span.Text)
					}
				}
			}
		}
		for _, items := range [][]Item{component.Cover.Worship, component.Cover.Work, component.Cover.WordQuestions} {
			for _, item := range items {
				for _, block := range item.Blocks {
					for _, sentence := range block.Sentences {
						for _, span := range sentence.Spans {
							text.WriteString(span.Text)
						}
					}
				}
			}
		}
		if strings.ContainsAny(text.String(), ":：") || strings.Contains(text.String(), "Welcome") || strings.Contains(text.String(), "金句") || strings.Contains(text.String(), "頁尾") || strings.Count(text.String(), "歌曲") != 3 || strings.Count(text.String(), "問題句") != questions || !strings.Contains(text.String(), "第一行經文第二行經文。") {
			t.Fatal("cover text was duplicated, lost or mixed with fixed labels")
		}
		if questions == 6 && (!strings.Contains(text.String(), "➊") || strings.ContainsRune(text.String(), '\uf08c')) {
			t.Fatal("source black-circle marker was not normalized")
		}
		for _, item := range component.Cover.Worship {
			found := false
			for _, slot := range slots {
				if slot.BlockID == item.Blocks[0].ID {
					found = true
					if slot.Box.Width > .18 || slot.Box.Y > .32 {
						t.Fatal("inline songs became stacked full-width rows")
					}
				}
			}
			if !found {
				t.Fatal("song has no slot")
			}
		}
		for index := range page.Texts {
			if page.Texts[index].Text != "第一行經文" {
				continue
			}
			original := page.Texts[index]
			page.Texts[index].Text, page.Texts[index].FontFamily = "\uf0ae", "Wingdings"
			_, _, _, findings, err := coverComponent(page)
			if err != nil || len(findings) != 1 || findings[0].Code != "unknown_font_role" {
				t.Fatalf("scripture semantics must not hide an unmapped symbol: %v %v", findings, err)
			}
			page.Texts[index] = original
		}
		for index := range page.Texts {
			if strings.Contains(page.Texts[index].Text, "歡迎句") {
				page.Texts[index].FontFamily = "Undefined"
			}
		}
		_, _, _, findings, err := coverComponent(page)
		if err != nil || len(findings) != 1 || findings[0].Code != "unknown_font_role" {
			t.Fatalf("verse inference must not suppress uncertainty in other fields: %v %v", findings, err)
		}
	}
}

func TestCoverCannotInventMissingSection(t *testing.T) {
	if _, _, _, _, err := coverComponent(GeometryPage{Number: 1, Width: templateV1Width, Height: templateV1Height}); err == nil {
		t.Fatal("missing template anchors accepted")
	}
}
