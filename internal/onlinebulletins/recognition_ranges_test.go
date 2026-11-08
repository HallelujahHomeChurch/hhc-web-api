package onlinebulletins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAzureRecognitionSplitsTitleSeparatorAndSpeakerWithinOneNativeRun(t *testing.T) {
	input := recognitionFixture(t)
	input.Page, input.TotalPages = 2, 4
	// Literal source positions include proportional Latin glyphs outside the title.
	raw, _ := json.Marshal(input)
	var encoded map[string]any
	if err := json.Unmarshal(raw, &encoded); err != nil {
		t.Fatal(err)
	}
	encoded["Native"] = []any{
		map[string]any{"id": "p2-t1", "text": "恩典～Ada", "box": Box{X: .2, Y: .3, Width: .48, Height: .03}, "characters": []Box{
			{X: .2, Y: .3, Width: .1, Height: .03}, {X: .3, Y: .3, Width: .1, Height: .03},
			{X: .4, Y: .3, Width: .06, Height: .03}, {X: .46, Y: .3, Width: .08, Height: .03},
			{X: .54, Y: .3, Width: .06, Height: .03}, {X: .6, Y: .3, Width: .08, Height: .03},
		}},
		map[string]any{"id": "p2-t2", "text": "正文。", "box": Box{X: .2, Y: .4, Width: .7, Height: .03}},
	}
	raw, _ = json.Marshal(encoded)
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	block := func(role, kind string, start, end int) map[string]any {
		return map[string]any{"mode": "native", "role": role, "sectionKind": kind, "group": "article", "text": "", "sourceIds": []string{}, "sourceRanges": []any{map[string]any{"id": "p2-t1", "start": start, "end": end}}, "imageIds": []string{}, "box": Box{}}
	}
	proposal := map[string]any{"page": 2, "blocks": []any{
		block("body_title", "testimony", 0, 2), block("body_separator", "", 2, 3), block("body_speaker", "", 3, 6),
		map[string]any{"mode": "native", "role": "body_paragraph", "sectionKind": "", "group": "article", "text": "", "sourceIds": []string{"p2-t2"}, "sourceRanges": []any{}, "imageIds": []string{}, "box": Box{}},
	}, "unassignedIds": []string{}}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(recognitionResponse(proposal))
	}))
	defer server.Close()
	client, err := NewAzureExtractor(server.URL, "candidate", "test-key", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := client.RecognizePage(context.Background(), input)
	if err != nil {
		t.Fatalf("a faithful title/name split was rejected: %v", err)
	}
	if len(result.Blocks) != 4 || result.Blocks[0].Text != "恩典" || result.Blocks[1].Text != "～" || result.Blocks[2].Text != "Ada" || result.Blocks[3].Text != "正文。" {
		t.Fatal("source characters were rewritten or assigned to the wrong field")
	}
	if result.Blocks[0].Box.X != .2 || result.Blocks[2].Box.X != .46 || result.Blocks[2].Box.X+result.Blocks[2].Box.Width < .679 {
		t.Fatal("partial fields lost their trusted character positions")
	}
}

func rangeFixture(t *testing.T) RecognitionPage {
	t.Helper()
	input := recognitionFixture(t)
	input.Native = []RecognitionText{{ID: "p1-t1", Text: "ABCD", Box: Box{X: .2, Y: .3, Width: .4, Height: .02}, Characters: []Box{
		{X: .2, Y: .3, Width: .1, Height: .02}, {X: .3, Y: .3, Width: .1, Height: .02},
		{X: .4, Y: .3, Width: .1, Height: .02}, {X: .5, Y: .3, Width: .1, Height: .02},
	}}}
	return input
}

func TestRecognitionRejectsVisualRangeDuplicatingAnotherBlocksNativeGlyph(t *testing.T) {
	input := rangeFixture(t)
	result := RecognitionResult{Page: 1, UnassignedIDs: []string{}, Blocks: []RecognitionBlock{
		{Mode: "visual", Role: "cover_verse", Group: "a", Text: "AB", SourceIDs: []string{}, SourceRanges: []RecognitionSourceRange{{ID: "p1-t1", Start: 0, End: 1}}, Box: Box{X: .2, Y: .3, Width: .2, Height: .02}},
		{Mode: "native", Role: "cover_verse", Group: "b", SourceIDs: []string{}, SourceRanges: []RecognitionSourceRange{{ID: "p1-t1", Start: 1, End: 4}}},
	}}
	if validRecognitionResult(input, result) {
		t.Fatal("visual AB was accepted although its B glyph is assigned to a native block")
	}
}

func TestRecognitionRejectsCharacterGeometryOutsideItsNativeRun(t *testing.T) {
	input := rangeFixture(t)
	input.Native[0].Characters[0] = Box{X: .01, Y: .01, Width: .95, Height: .95}
	if validRecognitionPage(input) {
		t.Fatal("overlap alone incorrectly authorized an unrelated character box")
	}
}

func TestRecognitionRangesRejectInvalidCoverageAndOrdering(t *testing.T) {
	for _, name := range []string{"unknown", "negative", "empty", "oversize", "out_of_order", "duplicate", "missing", "whole_and_range", "missing_character_geometry"} {
		t.Run(name, func(t *testing.T) {
			input := rangeFixture(t)
			block := RecognitionBlock{Mode: "native", Role: "cover_verse", Group: "a", SourceIDs: []string{}, SourceRanges: []RecognitionSourceRange{{ID: "p1-t1", Start: 0, End: 2}, {ID: "p1-t1", Start: 2, End: 4}}}
			switch name {
			case "unknown":
				block.SourceRanges[0].ID = "invented"
			case "negative":
				block.SourceRanges[0].Start = -1
			case "empty":
				block.SourceRanges[0].End = 0
			case "oversize":
				block.SourceRanges[1].End = 5
			case "out_of_order":
				block.SourceRanges[0], block.SourceRanges[1] = block.SourceRanges[1], block.SourceRanges[0]
			case "duplicate":
				block.SourceRanges[1].Start = 1
			case "missing":
				block.SourceRanges[1].End = 3
			case "whole_and_range":
				block.SourceIDs = []string{"p1-t1"}
			case "missing_character_geometry":
				input.Native[0].Characters = nil
			}
			if validRecognitionResult(input, RecognitionResult{Page: 1, Blocks: []RecognitionBlock{block}, UnassignedIDs: []string{}}) {
				t.Fatal("untrustworthy native range accepted")
			}
		})
	}
}
