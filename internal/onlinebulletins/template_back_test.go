package onlinebulletins

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestBackComponentsPreserveUnmatchedSummaryMarginText(t *testing.T) {
	allowed, _, err := filterTemplateV1Geometry(candidateGeometry())
	if err != nil {
		t.Fatal(err)
	}
	page := allowed.Pages[len(allowed.Pages)-1]
	for _, column := range []struct {
		text string
		x, y float64
	}{{"一看再看、百看不厭的", 40.2, 50.077}, {"家教會週報", 53.28, 77.077}} {
		for index, point := range []rune(column.text) {
			page.Texts = append(page.Texts, GeometryText{Text: string(point), FontFamily: "DFXingShu", FontSize: 11, Box: Box{X: column.x / page.Width, Y: (column.y + float64(index)*10.8) / page.Height, Width: 11.04 / page.Width, Height: 11.04 / page.Height}})
		}
	}
	if slots, issues := backSidebarSlots(page); len(slots) != 2 || len(issues) != 0 {
		t.Fatal("fixture needs two proven sidebar columns", issues)
	}
	page.Texts = append(page.Texts, GeometryText{Text: "KEEP_unmatched_margin。", FontFamily: "MingLiU", FontSize: 11, Box: Box{X: .14, Y: .10, Width: .2, Height: .015}})
	page.Texts = append(page.Texts, GeometryText{Text: "KEEP_inside_sidebar_box。", FontFamily: "MingLiU", FontSize: 6, Box: Box{X: 44.2 / page.Width, Y: 90 / page.Height, Width: 6 / page.Width, Height: 6 / page.Height}})
	components, _, _, issues, err := backComponents(page)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(components)
	if strings.Count(string(data), "KEEP_unmatched_margin") != 1 || strings.Count(string(data), "KEEP_inside_sidebar_box") != 1 {
		t.Fatal("unmatched source margin text was discarded or duplicated")
	}
	found := false
	for _, issue := range issues {
		found = found || issue.Code == "fixed_template_source_mismatch" && issue.ComponentID == "back-summary" && issue.SourcePage == page.Number
	}
	if !found || ValidateReview(issues, issues) == nil {
		t.Fatal("unmatched margin text needs a located, non-waivable check", issues)
	}
}

func TestBackComponentsUseOnlyFilteredSummaryAnnouncementsAndPrayers(t *testing.T) {
	for _, count := range []int{7, 8} {
		geometry := GeometryDocument{Pages: make([]GeometryPage, 4)}
		for index := range geometry.Pages {
			geometry.Pages[index] = GeometryPage{Number: index + 1, Width: templateV1Width, Height: templateV1Height}
		}
		back := &geometry.Pages[3]
		add := func(text string, x, y float64) {
			back.Texts = append(back.Texts, GeometryText{Text: text, FontFamily: "MingLiU", FontSize: 12, Box: Box{X: x, Y: y, Width: .7, Height: .015}})
		}
		add("KEEP_summary。", .2, .05)
		add("家教會公佈欄", .3, .23)
		back.Texts[len(back.Texts)-1].Box.Width = .3
		for index := 0; index < count; index++ {
			add(fmt.Sprintf("%d.KEEP_announcement。", index+1), .06, .27+float64(index)*.024)
		}
		add("得勝與代求", .04, .47)
		back.Texts[len(back.Texts)-1].Box.Width = .15
		for index := 0; index < 3; index++ {
			add(fmt.Sprintf("%d.KEEP_prayer。", index+1), .06, .49+float64(index)*.023)
		}
		add("主日敬拜服事表", .2, 470/templateV1Height)
		add("DROP_roster", .2, 495/templateV1Height)
		add("DROP_attendance", .2, 570/templateV1Height)
		add("DROP_offerings", .2, 620/templateV1Height)
		allowed, _, err := filterTemplateV1Geometry(geometry)
		if err != nil {
			t.Fatal(err)
		}
		components, slots, fixed, issues, err := backComponents(allowed.Pages[3])
		if err != nil || len(components) != 3 || len(components[0].Items) != 1 || len(components[1].Items) != count || len(components[2].Items) != 3 || len(slots) != count+4 || len(fixed) != 2 || len(issues) != 0 {
			t.Fatalf("back component grouping: %v %v", issues, err)
		}
		data, _ := json.Marshal(components)
		if strings.Contains(string(data), "DROP_") || strings.Contains(string(data), "公佈欄") || strings.Contains(string(data), "得勝與代求") || strings.Count(string(data), "KEEP_announcement") != count || strings.Count(string(data), "KEEP_prayer") != 3 {
			t.Fatal("excluded or fixed text escaped into dynamic back components")
		}
		if _, _, _, _, err := backComponents(*back); err == nil {
			t.Fatal("back normalizer accepted an unfiltered page")
		}
	}
}

func outlinedBackGeometry(t *testing.T) GeometryPage {
	t.Helper()
	page := GeometryPage{Number: 20, Width: 612, Height: 792, backContentBottom: 429.112811}
	var err error
	page.Frames, err = parseBackFrameSVG(strings.NewReader(scaledBackFrameSVG()), 612, 792)
	if err != nil {
		t.Fatal(err)
	}
	add := func(text string, y float64) {
		page.Texts = append(page.Texts, GeometryText{Text: text, FontFamily: "MingLiU", FontSize: 11, Box: Box{X: .2, Y: y / 792, Width: .65, Height: 12 / 792.0}})
	}
	add("KEEP_summary。", 100)
	for i := 1; i <= 8; i++ {
		add(fmt.Sprintf("%d.KEEP_announcement。", i), 205+float64(i-1)*18)
	}
	for i := 1; i <= 3; i++ {
		add(fmt.Sprintf("%d.KEEP_prayer。", i), 377+float64(i-1)*15)
	}
	return page
}

func TestOutlinedBackLabelsRequireReviewAndPreserveAllItems(t *testing.T) {
	page := outlinedBackGeometry(t)
	components, _, fixed, issues, err := backComponents(page)
	if err != nil || len(components) != 3 || len(components[0].Items) != 1 || len(components[1].Items) != 8 || len(components[2].Items) != 3 || len(fixed) != 2 {
		t.Fatalf("outlined back unavailable: components=%d fixed=%d err=%v", len(components), len(fixed), err)
	}
	if len(issues) != 1 || issues[0].Code != "unmapped_fixed_template" || issues[0].SourcePage != 20 || !issues[0].Blocking {
		t.Fatalf("inferred labels silently approved: %+v", issues)
	}
	data, _ := json.Marshal(components)
	if strings.Count(string(data), "KEEP_summary") != 1 || strings.Count(string(data), "KEEP_announcement") != 8 || strings.Count(string(data), "KEEP_prayer") != 3 {
		t.Fatal("native back content lost or duplicated")
	}
	for _, slot := range fixed {
		for _, text := range page.Texts {
			if slot.Box.Y < text.Box.Y+text.Box.Height && slot.Box.Y+slot.Box.Height > text.Box.Y {
				t.Fatal("inferred label overlaps native content")
			}
		}
	}
}

func TestOutlinedBackLabelsRejectAmbiguousEvidence(t *testing.T) {
	for _, name := range []string{"missing frame", "duplicate frame", "unfiltered", "missing item", "unknown heading", "overlapping frames"} {
		t.Run(name, func(t *testing.T) {
			page := outlinedBackGeometry(t)
			switch name {
			case "missing frame":
				page.Frames = page.Frames[:1]
			case "duplicate frame":
				page.Frames = append(page.Frames, page.Frames[0])
			case "unfiltered":
				page.backContentBottom = 0
			case "missing item":
				page.Texts[2].Text = "3.KEEP_announcement。"
			case "unknown heading":
				page.Texts = append(page.Texts, GeometryText{Text: "未知標題", FontFamily: "MingLiU", FontSize: 11, Box: Box{X: .2, Y: 190 / 792.0, Width: .4, Height: 12 / 792.0}})
			case "overlapping frames":
				page.Frames[0].Box.Height = .5
			}
			if _, _, _, _, err := backComponents(page); err == nil {
				t.Fatal("unproven back structure accepted")
			}
		})
	}
}
