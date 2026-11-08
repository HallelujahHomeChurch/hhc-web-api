package onlinebulletins

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestHymnsPreserveColumnsLinesAndNumberOrder(t *testing.T) {
	page := GeometryPage{Number: 11, Width: templateV1Width, Height: templateV1Height}
	add := func(text string, x, y float64) {
		page.Texts = append(page.Texts, GeometryText{Text: text, FontFamily: "MingLiU", FontSize: 12, Box: Box{X: x, Y: y, Width: .30, Height: .015}})
	}
	add("10", .92, .035)
	page.Texts[0].Box.Width = .03
	add("小組詩歌", .08, .12)
	add("1.第一首(歌 1)", .08, .20)
	add("第一行", .08, .225)
	add("第二行", .10, .25)
	add("3.第三首(新歌)", .08, .40)
	add("第三首第一行", .08, .425)
	add("2.第二首(新歌)", .55, .20)
	add("第二首第一行", .55, .225)
	component, slots, fixed, issues, err := hymnComponent(page)
	if err != nil || len(issues) != 0 || len(fixed) != 2 || fixed[0].Element != "hymnLabel" || fixed[1].Element != "pageNumber" || fixed[1].sourcePage != 11 || fixed[1].Box.X != .92 || math.Abs(fixed[1].Box.Width-.03) > 1e-9 || len(slots) != 7 || len(component.HymnLyrics.Hymns) != 3 {
		t.Fatalf("hymns: %v %v", err, issues)
	}
	hymns := component.HymnLyrics.Hymns
	if len(hymns[0].Sections[0].Lines) != 2 || hymns[1].ID != "hymns-song-2" || hymns[2].ID != "hymns-song-3" {
		t.Fatal("source lines or numbered order lost")
	}
	encoded, _ := json.Marshal(component)
	if strings.Contains(string(encoded), "小組詩歌") || !strings.Contains(string(encoded), "第二行") {
		t.Fatal("fixed label duplicated or lyrics missing")
	}
	if slots[3].Box.X < .5 || slots[3].BlockID != hymns[1].Title.ID {
		t.Fatal("song order changed physical column geometry")
	}
	for _, slot := range slots {
		if slot.ComponentID != component.ID {
			t.Fatal("hymn slot refers to a nested song instead of its component")
		}
	}
	page.Texts[3].Box.Width = .46
	if wider, _, _, findings, err := hymnComponent(page); err != nil || len(findings) != 0 || len(wider.HymnLyrics.Hymns) != 3 {
		t.Fatal("left lyrics fitting before the right column were rejected")
	}
	page.Texts[3].Box.Width = .30
	page.Texts[0].Text = "9"
	if _, _, _, _, err := hymnComponent(page); err == nil {
		t.Fatal("wrong source hymn page number accepted")
	}
	page.Texts[0].Text = "10"
	page.Texts = append(page.Texts, GeometryText{Text: "1.重複(新歌)", FontFamily: "MingLiU", FontSize: 12, Box: Box{X: .55, Y: .60, Width: .3, Height: .015}})
	if _, _, _, _, err := hymnComponent(page); err == nil {
		t.Fatal("duplicate song number accepted")
	}
}

func TestHymnsDoNotFabricateUnknownHeadings(t *testing.T) {
	page := GeometryPage{Number: 3, Width: templateV1Width, Height: templateV1Height}
	if _, _, _, _, err := hymnComponent(page); err == nil {
		t.Fatal("unrecognized lyrics page accepted")
	}
}

func TestSplitHymnPageDoesNotMergeLabelWithRightColumn(t *testing.T) {
	page := GeometryPage{Number: 3, Width: templateV1Width, Height: templateV1Height}
	for _, value := range []struct {
		text string
		x, y float64
	}{{"小組詩歌", .08, .12}, {"1.歌曲(新歌)", .55, .12}, {"歌詞", .55, .15}} {
		page.Texts = append(page.Texts, GeometryText{Text: value.text, FontFamily: "MingLiU", FontSize: 12, Box: Box{X: value.x, Y: value.y, Width: .3, Height: .015}})
	}
	if _, _, _, _, err := hymnComponent(page); err != nil {
		t.Fatal(err)
	}
	body, hymns, err := splitHymnPage(page)
	if err != nil || body != nil || len(hymns.Texts) != len(page.Texts) {
		t.Fatalf("column-aware label rejected: %v", err)
	}
}

func TestSplitHymnPageKeepsArticleTailBeforeLyrics(t *testing.T) {
	page := GeometryPage{Number: 19, Width: templateV1Width, Height: templateV1Height}
	for _, value := range []struct {
		text string
		y    float64
	}{{"18", .035}, {"接續上一頁正文。", .10}, {"小組詩歌", .40}, {"1.歌曲(新歌)", .45}, {"歌詞", .48}} {
		page.Texts = append(page.Texts, GeometryText{Text: value.text, FontFamily: "MingLiU", FontSize: 12, Box: Box{X: .1, Y: value.y, Width: .3, Height: .015}})
	}
	body, hymns, err := splitHymnPage(page)
	if err != nil || body == nil || len(body.Texts) != 2 || len(hymns.Texts) != 4 || len(page.Texts) != 5 {
		t.Fatalf("split lost or duplicated content: %v", err)
	}
	if body.Texts[1].Text != "接續上一頁正文。" || hymns.Texts[1].Text != "小組詩歌" {
		t.Fatal("wrong semantic boundary")
	}
	if _, _, _, _, err := hymnComponent(hymns); err != nil {
		t.Fatal(err)
	}
	duplicate := page
	duplicate.Texts = append(append([]GeometryText(nil), page.Texts...), page.Texts[2])
	if _, _, err := splitHymnPage(duplicate); err == nil {
		t.Fatal("duplicate hymn boundary accepted")
	}
	crossing := page
	crossing.Texts = append([]GeometryText(nil), page.Texts...)
	crossing.Texts[1].Box.Y = .39
	crossing.Texts[1].Box.Height = .04
	if _, _, err := splitHymnPage(crossing); err == nil {
		t.Fatal("content crossing hymn boundary accepted")
	}
	page.Texts = append(page.Texts[:1], page.Texts[2:]...)
	body, _, err = splitHymnPage(page)
	if err != nil || body != nil {
		t.Fatal("pure lyrics invented body tail")
	}
	page.Texts = page.Texts[:1]
	if _, _, err = splitHymnPage(page); err == nil {
		t.Fatal("missing hymn anchor accepted")
	}
}
