package onlinebulletins

import "testing"

func TestBodyHeaderKeepsLectureDateIndependentAndRemovesOnlyFixedPrefixes(t *testing.T) {
	page := GeometryPage{Number: 2, Width: templateV1Width, Height: templateV1Height, Texts: []GeometryText{
		{Text: "‧Sep.13.2026 (1739共10頁)", FontFamily: "DFXingShu", FontSize: 11, Box: Box{X: 68.064 / templateV1Width, Y: 234.17696 / templateV1Height, Width: .35, Height: 11.04 / templateV1Height}},
		{Text: "‧講員：甲牧師", FontFamily: "DFXingShu", FontSize: 11, Box: Box{X: 68.064 / templateV1Width, Y: 254.21696 / templateV1Height, Width: 48.24 / templateV1Width, Height: 11.04 / templateV1Height}},
		{Text: "謄修：甲乙", FontFamily: "DFXingShu", FontSize: 11, Box: Box{X: 381.91 / templateV1Width, Y: 234.17696 / templateV1Height, Width: 46.68 / templateV1Width, Height: 11.04 / templateV1Height}},
		{Text: "完稿：丙丁", FontFamily: "DFXingShu", FontSize: 11, Box: Box{X: 381.31 / templateV1Width, Y: 254.21696 / templateV1Height, Width: 46.68 / templateV1Width, Height: 11.04 / templateV1Height}},
	}}
	lines, err := sourceLines(page, false)
	if err != nil {
		t.Fatal(err)
	}
	metadata := CanonicalMetadata{Title: "主題", IssueNumber: 1739, Date: "2026-09-20"}
	mapping, err := bodyHeaderComponent(lines, metadata, 12, "body-section-1")
	if err != nil || mapping.header == nil || len(mapping.slots) != 4 || len(mapping.fixed) != 5 {
		t.Fatalf("header mapping: %v", err)
	}
	if privateBlockText(mapping.header.LectureDate) != "Sep.13.2026" || len(mapping.header.Contributors) != 3 {
		t.Fatal("lecture date was replaced by issue date")
	}
	for index, want := range []string{"甲牧師", "甲乙", "丙丁"} {
		if privateBlockText(mapping.header.Contributors[index].Name) != want {
			t.Fatal("fixed contributor prefix leaked into editable name")
		}
	}
	if len(mapping.issues) == 0 {
		t.Fatal("proposed partial-run/profile geometry was accepted without review")
	}
	if mapping.issues[0].SourcePage != 2 || mapping.issues[0].ComponentID != "body-section-1" {
		t.Fatal("article header review lost its exact source page")
	}
	mapping, err = bodyHeaderComponent(lines, metadata, 16, "body-section-1")
	if err != nil || mapping.header != nil || len(mapping.issues) != 1 || mapping.issues[0].Code != "canonical_source_mismatch" {
		t.Fatal("printed page count mismatch silently accepted")
	}
	metadata.IssueNumber = 1740
	mapping, err = bodyHeaderComponent(lines, metadata, 12, "body-section-1")
	if err != nil || mapping.header != nil || len(mapping.issues) == 0 {
		t.Fatal("printed issue mismatch silently overwritten with CMS metadata")
	}
}

func TestHeaderNameRunsRejectsProportionalMergedPrefix(t *testing.T) {
	line := geometryLine{width: templateV1Width, height: templateV1Height, parts: []GeometryText{{Text: "謄修：Alice", FontFamily: "DFXingShu", FontSize: 11, Box: Box{X: .6, Y: .3, Width: .15, Height: .02}}}}
	if _, _, _, ok := headerNameRuns(line, "謄修："); ok {
		t.Fatal("proportional merged prefix geometry was guessed")
	}
	line.parts = []GeometryText{
		{Text: "謄修：", FontFamily: "DFXingShu", FontSize: 11, Box: Box{X: .6, Y: .3, Width: .05, Height: .02}},
		{Text: "Alice", FontFamily: "Times", FontSize: 11, Box: Box{X: .65, Y: .3, Width: .08, Height: .02}},
	}
	parts, _, _, ok := headerNameRuns(line, "謄修：")
	if !ok || len(parts) != 1 || parts[0] != line.parts[1] {
		t.Fatal("independently bounded Latin name was not preserved")
	}
}
