package onlinebulletins

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBackSidebarUsesExactSourceColumnsNotEditableSummaryText(t *testing.T) {
	page := GeometryPage{Number: 12, Width: templateV1Width, Height: templateV1Height}
	for _, column := range []struct {
		text string
		x, y float64
	}{{"一看再看、百看不厭的", 40.2, 50.077}, {"家教會週報", 53.28, 77.077}} {
		for index, point := range []rune(column.text) {
			page.Texts = append(page.Texts, GeometryText{Text: string(point), FontFamily: "DFXingShu", FontSize: 11, Box: Box{X: column.x / page.Width, Y: (column.y + float64(index)*10.8) / page.Height, Width: 11.04 / page.Width, Height: 11.04 / page.Height}})
		}
	}
	slots, issues := backSidebarSlots(page)
	if len(slots) != 2 || len(issues) != 0 || slots[0].Element != "summarySidebarTagline" || slots[1].Element != "summarySidebarTitle" || slots[0].sourcePage != 12 || slots[0].Style.LetterSpacing >= 0 {
		t.Fatal("fixed vertical source columns lost")
	}
	page.Texts[0].Text = "改"
	slots, issues = backSidebarSlots(page)
	if len(slots) != 1 || len(issues) != 1 || !issues[0].Blocking {
		t.Fatal("changed source template silently replaced")
	}
}

func TestOutlinedBackSidebarIsLocatedReviewProposal(t *testing.T) {
	page := outlinedBackGeometry(t)
	slots, issues := backSidebarSlots(page)
	if len(slots) != 2 || len(issues) != 1 || issues[0].Code != "unmapped_fixed_template" || issues[0].SourcePage != page.Number || !issues[0].Blocking {
		t.Fatal("missing outlined columns must be a located proposal, not a proven text mismatch")
	}
	for _, slot := range slots {
		if !validBox(slot.Box) || !validParagraphStyle(slot.Style) || slot.sourcePage != page.Number {
			t.Fatal("invalid sidebar proposal")
		}
	}
	if ValidateReview(issues, nil) == nil || ValidateReview(issues, issues) != nil {
		t.Fatal("outlined wording requires explicit review")
	}
	for _, change := range []string{"native-conflict", "missing-frame", "unfiltered"} {
		t.Run(change, func(t *testing.T) {
			page := outlinedBackGeometry(t)
			switch change {
			case "native-conflict":
				page.Texts = append(page.Texts, GeometryText{Text: "不同", FontFamily: "PMingLiU", FontSize: 11, Box: Box{X: 40.2 / page.Width, Y: 50 / page.Height, Width: 11 / page.Width, Height: 22 / page.Height}})
			case "missing-frame":
				page.Frames = nil
			case "unfiltered":
				page.backContentBottom = 0
			}
			slots, issues := backSidebarSlots(page)
			if len(slots) != 0 || len(issues) == 0 || ValidateReview(issues, issues) == nil {
				t.Fatal("unproven sidebar became waivable")
			}
		})
	}
}

func TestTranslatedNativeSidebarDoesNotEnterSummary(t *testing.T) {
	for _, change := range []string{"valid", "changed", "duplicate", "extra-text", "ambiguous-column", "too-many-origins"} {
		t.Run(change, func(t *testing.T) {
			page := outlinedBackGeometry(t)
			start := len(page.Texts)
			for _, column := range []struct {
				text string
				x, y float64
			}{{"㇐看再看、百看不厭的", 64.318, 57.38}, {"家教會週報", 76.561, 83.19}} {
				for i, point := range []rune(column.text) {
					page.Texts = append(page.Texts, GeometryText{Text: string(point), FontFamily: "CIDFont+F1", FontSize: 10, Box: Box{X: column.x / page.Width, Y: (column.y + float64(i)*10.3) / page.Height, Width: 10.4 / page.Width, Height: 13.5 / page.Height}})
				}
			}
			switch change {
			case "changed":
				page.Texts[start].Text = "改"
			case "duplicate":
				page.Texts = append(page.Texts, page.Texts[start])
			case "extra-text":
				part := page.Texts[start]
				part.Text, part.Box.Y = "保留", 170/page.Height
				page.Texts = append(page.Texts, part)
			case "ambiguous-column":
				for _, part := range page.Texts[start : start+10] {
					part.Box.X -= 24 / page.Width
					page.Texts = append(page.Texts, part)
				}
			case "too-many-origins":
				for i := 1; i <= 65; i++ {
					part := page.Texts[start]
					part.Box.X = float64(i) / 1000
					page.Texts = append(page.Texts, part)
				}
			}
			slots, _ := backSidebarSlots(page)
			if (len(slots) == 2) != (change == "valid") {
				t.Fatal("sidebar needs complete, unique native columns")
			}
			for _, slot := range slots {
				for _, column := range backSidebarColumns {
					advance := float64(len([]rune(column.text))) * slot.Style.FontSize * (1 + slot.Style.LetterSpacing)
					if slot.Element == column.element && advance > slot.Box.Height*page.Height {
						t.Fatal("CSS trailing tracking overflows the observed sidebar column")
					}
				}
			}
			components, _, _, issues, err := backComponents(page)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(components)
			if strings.Count(string(raw), "KEEP_summary") != 1 {
				t.Fatal("summary content was lost")
			}
			if change == "valid" {
				if strings.Contains(string(raw), "㇐") || strings.Contains(string(raw), `"text":"看"`) {
					t.Fatal("native sidebar glyphs leaked into editable summary")
				}
			} else {
				if !strings.Contains(string(raw), "㇐") && !strings.Contains(string(raw), "改") {
					t.Fatal("unmatched sidebar source text was discarded")
				}
				found := false
				for _, issue := range issues {
					found = found || issue.Code == "fixed_template_source_mismatch" && issue.ComponentID == "back-summary"
				}
				if !found || ValidateReview(issues, issues) == nil {
					t.Fatal("unmatched sidebar text lost its non-waivable review gate")
				}
			}
		})
	}
}
