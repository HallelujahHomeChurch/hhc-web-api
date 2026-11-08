package onlinebulletins

import (
	"encoding/json"
	"testing"
)

func TestRecognitionBindsCharacterPositionsAcrossTitleBorder(t *testing.T) {
	source := candidateGeometry()
	source.Pages[1].Texts = []GeometryText{
		{Text: "恩典～Ada", FontFamily: "MingLiU", FontSize: 14, Box: Box{X: .2, Y: .3, Width: .48, Height: .03}},
		{Text: "正文。", FontFamily: "MingLiU", FontSize: 14, Box: Box{X: .2, Y: .4, Width: .7, Height: .03}},
	}
	raw, _ := json.Marshal(source)
	var encoded map[string]any
	if err := json.Unmarshal(raw, &encoded); err != nil {
		t.Fatal(err)
	}
	encoded["Pages"].([]any)[1].(map[string]any)["Glyphs"] = []any{
		map[string]any{"Text": "恩", "Box": Box{X: .2, Y: .3, Width: .1, Height: .03}},
		map[string]any{"Text": "典", "Box": Box{X: .3, Y: .3, Width: .1, Height: .03}},
		map[string]any{"Text": "～", "Box": Box{X: .4, Y: .3, Width: .06, Height: .03}},
		map[string]any{"Text": "A", "Box": Box{X: .46, Y: .3, Width: .08, Height: .03}},
		map[string]any{"Text": "d", "Box": Box{X: .54, Y: .3, Width: .06, Height: .03}},
		map[string]any{"Text": "a", "Box": Box{X: .6, Y: .3, Width: .08, Height: .03}},
	}
	raw, _ = json.Marshal(encoded)
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	pages, err := PrepareRecognitionPages(source, recognitionRasters(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	if len(pages[1].Native[0].Characters) != 6 || pages[1].Native[0].Characters[3].X != .46 || pages[1].Native[0].Text != "恩典～Ada" {
		t.Fatal("native glyph positions were discarded or proportional positions guessed")
	}
}

func TestRecognitionGlyphDecoderRejectsMismatchedEvidenceAtomically(t *testing.T) {
	for _, raw := range []string{
		`{"pages":[{"page":2,"width":612,"height":792,"glyphs":[]}]}`,
		`{"pages":[{"page":1,"width":611,"height":792,"glyphs":[]}]}`,
		`{"pages":[{"page":1,"width":612,"height":792,"glyphs":[{"text":"AB","box":{"x":0.1,"y":0.1,"width":0.1,"height":0.1}}]}]}`,
		`{"pages":[{"page":1,"width":612,"height":792,"glyphs":[{"text":"A","box":{"x":0.95,"y":0.1,"width":0.1,"height":0.1}}]}]}`,
		`{"pages":[{"page":1,"width":612,"height":792,"glyphs":[]}]} {}`,
	} {
		source := GeometryDocument{Pages: []GeometryPage{{Number: 1, Width: 612, Height: 792, Glyphs: []GeometryGlyph{{Text: "original"}}}}}
		if decodeRecognitionGlyphs([]byte(raw), &source) == nil || source.Pages[0].Glyphs[0].Text != "original" {
			t.Fatal("invalid evidence changed trusted source")
		}
	}
}

func TestRecognitionGlyphPrivacyAndAmbiguousPositions(t *testing.T) {
	source := candidateGeometry()
	last := len(source.Pages) - 1
	source.Pages[last].Glyphs = []GeometryGlyph{
		{Text: "A", Box: Box{X: .2, Y: .1, Width: .02, Height: .02}},
		{Text: "B", Box: Box{X: .2, Y: .7, Width: .02, Height: .02}},
		{Text: "C", Box: Box{X: .2, Y: .54, Width: .02, Height: .1}},
	}
	allowed, issues, err := filterTemplateV1Geometry(source)
	if err != nil || len(issues) > 0 || len(allowed.Pages[last].Glyphs) != 1 || len(source.Pages[last].Glyphs) != 3 {
		t.Fatal("excluded glyphs retained or source mutated", issues, err)
	}
	part := GeometryText{Text: "AB", Box: Box{X: .1, Y: .1, Width: .2, Height: .03}}
	glyphs := []GeometryGlyph{{Text: "A", Box: Box{X: .1, Y: .1, Width: .1, Height: .03}}, {Text: "B", Box: Box{X: .2, Y: .1, Width: .1, Height: .03}}}
	if len(recognitionCharacterBoxes(part, glyphs)) != 2 {
		t.Fatal("exact binding lost")
	}
	glyphs = append(glyphs, glyphs[0])
	if recognitionCharacterBoxes(part, glyphs) != nil {
		t.Fatal("ambiguous glyph positions guessed")
	}
}
