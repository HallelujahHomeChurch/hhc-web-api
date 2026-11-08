package onlinebulletins

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestComponentCorrectionPreservesAnchorsAndSourceButInvalidatesProof(t *testing.T) {
	current := validDocument(4)
	current.LayoutManifest.ContentHash, current.LayoutManifest.LayoutValidationHash = strings.Repeat("a", 64), strings.Repeat("b", 64)
	text := &current.Components[1].BodySection.Blocks[0].Sentences[0]
	text.Source = &SourceLocation{Page: 2, Box: Box{X: .1, Y: .1, Width: .5, Height: .1}}
	before, _ := json.Marshal(current)
	var components []Component
	raw, _ := json.Marshal(current.Components)
	if err := json.Unmarshal(raw, &components); err != nil {
		t.Fatal(err)
	}
	changed := &components[1].BodySection.Blocks[0].Sentences[0]
	changed.Source = nil
	changed.Spans = []Span{{Text: "𠮷修正文字。", FontRole: "scripture"}}
	draft, err := EditComponents(current, components)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(current)
	if string(after) != string(before) {
		t.Fatal("edited immutable input")
	}
	got := draft.Components[1].BodySection.Blocks[0].Sentences[0]
	if got.ID != text.ID || !reflect.DeepEqual(got.Source, text.Source) || got.Spans[0].Text != "𠮷修正文字。" || draft.LayoutManifest.ContentHash != "" || draft.LayoutManifest.LayoutValidationHash != "" {
		t.Fatal("correction changed identity/provenance or retained proof")
	}
	if _, err = ValidateDocument(draft); err != nil {
		t.Fatal("updated Unicode fragment coverage invalid", err)
	}
	changed.Source = &SourceLocation{Page: 1, Box: Box{X: .2, Y: .2, Width: .5, Height: .1}}
	if _, err = EditComponents(current, components); err == nil {
		t.Fatal("client source coordinates accepted")
	}
}

func TestComponentCorrectionResizesUnicodeContinuation(t *testing.T) {
	for _, text := range []string{"𠮷", "𠮷正", "𠮷正文。", "𠮷正文延長。"} {
		t.Run(text, func(t *testing.T) {
			current := validDocument(12)
			current.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = "𠮷正文。"
			first := &current.LayoutManifest.Pages[1].Slots[0]
			first.Fragments[0].End = 2
			current.LayoutManifest.Pages[2].Slots = []Slot{{ID: "continuation", ComponentID: first.ComponentID, BlockID: first.BlockID, ContinuationOf: first.ID, Box: first.Box, Fragments: []Fragment{{SentenceID: "sentence-body", Start: 2, End: 4}}}}
			raw, _ := json.Marshal(current.Components)
			var components []Component
			if err := json.Unmarshal(raw, &components); err != nil {
				t.Fatal(err)
			}
			components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = text
			draft, err := EditComponents(current, components)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = ValidateDocument(draft); err != nil {
				t.Fatal("invalid continuation coverage", err)
			}
			if len([]rune(text)) <= 2 && len(draft.LayoutManifest.Pages[2].Slots) != 0 {
				t.Fatal("empty continuation retained")
			}
		})
	}
}

func TestComponentEditAllocatesServerSeedsForAddedParagraphAndSentence(t *testing.T) {
	current := validDocument(4)
	current.LayoutManifest.Pages[1].Slots[0].Box.Y = .3
	current.LayoutManifest.Pages[1].Slots[1].Box.Y = .1
	current.LayoutManifest.Pages[1].Slots[2].Box.Y = .2
	current.LayoutManifest.Pages[1].Slots[2].Box.Width = .2
	var components []Component
	if err := json.Unmarshal(comparisonJSON(current.Components), &components); err != nil {
		t.Fatal(err)
	}
	body := components[1].BodySection
	body.Blocks[0].Sentences = append(body.Blocks[0].Sentences, Sentence{ID: "new-sentence", Spans: []Span{{Text: "新增𠮷。", FontRole: "body"}}})
	newBlock := Block{ID: "new-block", Style: body.Blocks[0].Style, Sentences: []Sentence{{ID: "new-paragraph-sentence", Spans: []Span{{Text: "新增段。", FontRole: "body"}}}}}
	body.Blocks = append([]Block{newBlock}, body.Blocks...)
	draft, err := EditComponents(current, components)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ValidateDocument(draft); err != nil {
		t.Fatal(err)
	}
	if draft.Components[1].BodySection.Blocks[1].Sentences[0].ID != "sentence-body" {
		t.Fatal("existing annotation anchor changed")
	}
	if draft.LayoutManifest.ContentHash != "" || draft.LayoutManifest.LayoutValidationHash != "" {
		t.Fatal("draft retained proof")
	}
	seen := map[string]int{}
	for _, page := range draft.LayoutManifest.Pages {
		for _, slot := range page.Slots {
			for _, fragment := range slot.Fragments {
				seen[fragment.SentenceID] += fragment.End - fragment.Start
			}
		}
	}
	if seen["new-sentence"] != 4 || seen["new-paragraph-sentence"] != 4 {
		t.Fatal("wrong Unicode seed coverage", seen)
	}
	if len(current.Components[1].BodySection.Blocks) != 1 {
		t.Fatal("immutable source changed")
	}
	for _, slot := range draft.LayoutManifest.Pages[1].Slots {
		if slot.BlockID == "new-block" && slot.Box.Width != .8 {
			t.Fatal("paragraph borrowed contributor width", slot.Box)
		}
		if slot.BlockID == "new-block" && (slot.Box.Y <= .2 || slot.Box.Y >= .3) {
			t.Fatal("new paragraph placed outside its semantic neighbors", slot.Box)
		}
	}
}

func TestComponentEditReordersParagraphsUsingServerPositions(t *testing.T) {
	current := validDocument(4)
	first := &current.Components[1].BodySection.Blocks[0]
	second := Block{ID: "second-block", Style: first.Style, Sentences: []Sentence{{ID: "second-sentence", Spans: []Span{{Text: "第二。", FontRole: "body"}}}}}
	current.Components[1].BodySection.Blocks = append(current.Components[1].BodySection.Blocks, second)
	current.LayoutManifest.Pages[1].Slots[0].Box.Y = .3
	current.LayoutManifest.Pages[1].Slots[1].Box.Y = .1
	current.LayoutManifest.Pages[1].Slots[2].Box.Y = .2
	current.LayoutManifest.Pages[1].Slots = append(current.LayoutManifest.Pages[1].Slots, Slot{ID: "second-slot", ComponentID: "body", BlockID: second.ID, Box: Box{X: .1, Y: .4, Width: .8, Height: .1}, Fragments: []Fragment{{SentenceID: "second-sentence", End: 3}}})
	var components []Component
	if err := json.Unmarshal(comparisonJSON(current.Components), &components); err != nil {
		t.Fatal(err)
	}
	blocks := components[1].BodySection.Blocks
	blocks[0], blocks[1] = blocks[1], blocks[0]
	draft, err := EditComponents(current, components)
	if err != nil {
		t.Fatal(err)
	}
	positions := map[string]float64{}
	for _, slot := range draft.LayoutManifest.Pages[1].Slots {
		positions[slot.BlockID] = slot.Box.Y
	}
	if positions["second-block"] >= positions["block-body"] {
		t.Fatal("semantic order did not update layout seed", positions)
	}
}

func TestComponentEditAddsFirstAnnouncement(t *testing.T) {
	current := validDocument(4)
	item := current.Components[4].Items[0]
	current.Components[4].Items = []Item{}
	page := &current.LayoutManifest.Pages[3]
	page.Slots = append(page.Slots[:1], page.Slots[2:]...)
	var components []Component
	if err := json.Unmarshal(comparisonJSON(current.Components), &components); err != nil {
		t.Fatal(err)
	}
	components[4].Items = []Item{item}
	draft, err := EditComponents(current, components)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = ValidateDocument(draft); err != nil {
		t.Fatal(err)
	}
}
