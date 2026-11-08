package onlinebulletins

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestExtractionWhitespaceDoesNotReplaceUnchangedAnchors(t *testing.T) {
	base, incoming := validDocument(4), validDocument(4)
	for _, document := range []*Document{&base, &incoming} {
		block := &document.Components[1].BodySection.Blocks[0]
		block.Sentences = []Sentence{
			{ID: "old-first", Spans: []Span{{Text: "神的話 和神的靈。 ", FontRole: "body"}}},
			{ID: "old-second", Spans: []Span{{Text: "必要歡呼收割。 ", FontRole: "body"}}},
		}
		document.LayoutManifest.Pages[1].Slots[0].Fragments = []Fragment{{SentenceID: "old-first", End: 10}, {SentenceID: "old-second", End: 8}}
	}
	incoming.Components[1].BodySection.Blocks[0].ID = "new-block"
	incoming.LayoutManifest.Pages[1].Slots[0].BlockID = "new-block"
	for i, value := range []struct {
		id, text string
		length   int
	}{{"new-first", "神的話和神的靈。", 8}, {"new-second", "必要歡呼收割。", 7}} {
		incoming.Components[1].BodySection.Blocks[0].Sentences[i].ID = value.id
		incoming.Components[1].BodySection.Blocks[0].Sentences[i].Spans[0].Text = value.text
		incoming.LayoutManifest.Pages[1].Slots[0].Fragments[i] = Fragment{SentenceID: value.id, End: value.length}
	}
	got, _, err := ReconcileExtraction(base, incoming)
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{"old-first", "old-second"} {
		if got.Components[1].BodySection.Blocks[0].Sentences[i].ID != want {
			t.Fatalf("whitespace replaced anchor %s", want)
		}
	}
}

func TestIdenticalOrderedDuplicateLinesKeepTheirOwnAnchors(t *testing.T) {
	got := identityPairs([]identityCandidate{{"old-a", "line", "歡喜"}, {"old-b", "line", "歡喜"}}, []identityCandidate{{"new-a", "line", "歡喜"}, {"new-b", "line", "歡喜"}})
	if got["new-a"] != "old-a" || got["new-b"] != "old-b" {
		t.Fatal("unchanged repeated lines became ambiguous", got)
	}
	got = identityPairs([]identityCandidate{{"old-a", "line", "歡喜"}}, []identityCandidate{{"new-a", "line", "歡喜"}, {"new-b", "line", "歡喜"}})
	if len(got) != 0 {
		t.Fatal("new identical line stole an ambiguous anchor", got)
	}
}

func TestAnchoredTypoDoesNotBecomeAmbiguousBecauseAnotherSentenceSplit(t *testing.T) {
	got := identityPairs([]identityCandidate{{"typo", "sentence", "耶華"}, {"fixed", "sentence", "中間"}, {"split", "sentence", "前後"}}, []identityCandidate{{"corrected", "sentence", "耶和華"}, {"new-fixed", "sentence", "中間"}, {"front", "sentence", "前"}, {"back", "sentence", "後"}})
	if got["corrected"] != "typo" || got["new-fixed"] != "fixed" {
		t.Fatal("bounded typo lost its anchor", got)
	}
	if got["front"] != "" || got["back"] != "" {
		t.Fatal("split was guessed instead of reviewed", got)
	}
}

func TestRepeatedExtractionKeepsKindOnlyForUnchangedKnownSections(t *testing.T) {
	for _, name := range []string{"unchanged", "text", "title", "speaker", "style", "unknown-base", "unknown-incoming"} {
		t.Run(name, func(t *testing.T) {
			base, incoming := validDocument(4), validDocument(4)
			base.Components[1].BodySection.Kind = "reflection"
			incoming.Components[1].BodySection.Kind = "teaching"
			incoming.SourceAssetChecksum = strings.Repeat("d", 64)
			want := "teaching"
			switch name {
			case "unchanged":
				want = "reflection"
			case "text":
				incoming.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = "改文。"
			case "title":
				incoming.Components[1].BodySection.Title.Sentences[0].Spans[0].Text = "新題"
			case "speaker":
				incoming.Components[1].BodySection.Contributors[0].Name.Sentences[0].Spans[0].Text = "新人"
			case "style":
				incoming.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].FontRole = "scripture"
			case "unknown-base":
				base.Components[1].BodySection.Kind = "unknown"
			case "unknown-incoming":
				incoming.Components[1].BodySection.Kind = "unknown"
				want = "unknown"
			}
			before := string(comparisonJSON(incoming))
			got, _, err := ReconcileExtraction(base, incoming)
			if err != nil {
				t.Fatal(err)
			}
			if kind := got.Components[1].BodySection.Kind; kind != want {
				t.Fatalf("kind=%q want=%q", kind, want)
			}
			if string(comparisonJSON(incoming)) != before {
				t.Fatal("raw source evidence mutated")
			}
			if name == "unchanged" {
				units, err := CompareDocuments(base, base, got)
				if err != nil {
					t.Fatal(err)
				}
				for _, unit := range units {
					if unit.Suggestion != "same" {
						t.Fatalf("spurious source difference: %+v", unit)
					}
				}
			}
		})
	}
}

func TestRepeatedExtractionPreservesAuthoredKindInThreeWayComparison(t *testing.T) {
	base, local, incoming := validDocument(4), validDocument(4), validDocument(4)
	base.Components[1].BodySection.Kind = "reflection"
	local.Components[1].BodySection.Kind = "testimony"
	incoming.Components[1].BodySection.Kind = "teaching"
	got, _, err := ReconcileExtraction(base, incoming)
	if err != nil {
		t.Fatal(err)
	}
	units, err := CompareDocuments(base, local, got)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, unit := range units {
		if unit.ComponentID == "body" && unit.BlockID == "" {
			found = true
			if unit.Suggestion != "local" {
				t.Fatalf("authored kind not retained: %s", unit.Suggestion)
			}
		}
	}
	if !found {
		t.Fatal("missing authored component comparison")
	}
	if local.Components[1].BodySection.Kind != "testimony" {
		t.Fatal("local document mutated")
	}
}

func TestResegmentationKeepsMovedParagraphIdentityWithoutStealingIt(t *testing.T) {
	base, incoming := validDocument(4), resegmentedDocument(t)
	got, identities, err := ReconcileExtraction(base, incoming)
	if err != nil {
		t.Fatal(err)
	}
	retained := got.Components[2].BodySection.Blocks[0]
	if retained.ID != "block-body" || retained.Sentences[0].ID != "sentence-body" {
		t.Fatalf("moved unchanged paragraph lost anchors: %+v", retained)
	}
	if identities.Blocks["incoming-moved"] != retained.ID || identities.Sentences["s-incoming-moved"] != retained.Sentences[0].ID || identities.Components["incoming-section"] != got.Components[2].ID {
		t.Fatal("review locator identity maps lost moved content")
	}
	remaining := got.Components[1].BodySection.Blocks[0]
	if remaining.ID == "block-body" || remaining.Sentences[0].ID == "sentence-body" {
		t.Fatal("new text stole moved paragraph identity")
	}
	if _, err := ValidateDocument(got); err != nil {
		t.Fatal("rewritten layout invalid", err)
	}
}

func resegmentedDocument(t *testing.T) Document {
	t.Helper()
	incoming := validDocument(4)
	incoming.SourceAssetChecksum = strings.Repeat("d", 64)
	block := func(id, text string) Block {
		return Block{ID: id, Style: ParagraphStyle{FontSize: 16, LineHeight: 24}, Sentences: []Sentence{{ID: "s-" + id, Spans: []Span{{Text: text, FontRole: "body"}}}}}
	}
	moved := block("incoming-moved", "正文。")
	newSection := Component{ID: "incoming-section", Type: "bodySection", BodySection: &BodySection{Kind: "sermon", Title: block("incoming-title", "新段"), Blocks: []Block{moved}}}
	incoming.Components = append(incoming.Components[:2], append([]Component{newSection}, incoming.Components[2:]...)...)
	incoming.Components[1].BodySection.Blocks = []Block{block("incoming-staying", "留下。")}
	incoming.LayoutManifest.Pages[1].Slots[0].ComponentID = "incoming-section"
	incoming.LayoutManifest.Pages[1].Slots[0].BlockID = moved.ID
	incoming.LayoutManifest.Pages[1].Slots[0].Fragments[0].SentenceID = moved.Sentences[0].ID
	for _, item := range []struct {
		component string
		block     Block
		length    int
	}{{"body", incoming.Components[1].BodySection.Blocks[0], 3}, {"incoming-section", newSection.BodySection.Title, 2}} {
		incoming.LayoutManifest.Pages[1].Slots = append(incoming.LayoutManifest.Pages[1].Slots, Slot{ID: "slot-" + item.block.ID, ComponentID: item.component, BlockID: item.block.ID, Box: Box{X: .1, Y: .4, Width: .8, Height: .1}, Fragments: []Fragment{{SentenceID: item.block.Sentences[0].ID, End: item.length}}})
	}
	if _, err := ValidateDocument(incoming); err != nil {
		t.Fatal("invalid test source", err)
	}
	return incoming
}

func TestExtractionIdentitySurvivesTypoAndInsertedParagraph(t *testing.T) {
	for _, insert := range []bool{false, true} {
		base, incoming := validDocument(4), validDocument(4)
		incoming.SourceAssetChecksum = strings.Repeat("d", 64)
		body := incoming.Components[1].BodySection
		body.Blocks[0].ID = "source-block-new"
		body.Blocks[0].Sentences[0].ID = "source-sentence-new"
		incoming.LayoutManifest.Pages[1].Slots[0].BlockID = "source-block-new"
		incoming.LayoutManifest.Pages[1].Slots[0].Fragments[0].SentenceID = "source-sentence-new"
		if insert {
			added := Block{ID: "block-body", Style: body.Blocks[0].Style, Sentences: []Sentence{{ID: "inserted-sentence", Spans: []Span{{Text: "新增。", FontRole: "body"}}}}}
			body.Blocks = append([]Block{added}, body.Blocks...)
			incoming.LayoutManifest.Pages[1].Slots = append(incoming.LayoutManifest.Pages[1].Slots, Slot{ID: "inserted-slot", ComponentID: "body", BlockID: "block-body", Box: Box{X: .1, Y: .2, Width: .8, Height: .1}, Fragments: []Fragment{{SentenceID: "inserted-sentence", End: 3}}})
		} else {
			body.Blocks[0].Sentences[0].Spans[0].Text = "修正。"
		}
		before, _ := json.Marshal(incoming)
		got, _, err := ReconcileExtraction(base, incoming)
		if err != nil {
			t.Fatal(err)
		}
		index := 0
		if insert {
			index = 1
			if got.Components[1].BodySection.Blocks[0].ID == "block-body" {
				t.Fatal("new paragraph stole existing block identity")
			}
		}
		block := got.Components[1].BodySection.Blocks[index]
		if block.ID != "block-body" || block.Sentences[0].ID != "sentence-body" {
			t.Fatal("lost stable anchor", block)
		}
		if _, err = ValidateDocument(got); err != nil {
			t.Fatal("layout references not rewritten", err)
		}
		after, _ := json.Marshal(incoming)
		if string(before) != string(after) {
			t.Fatal("source mutated")
		}
	}
}

func TestReconcileExtractionPreservesSpeakerSeparatorBinding(t *testing.T) {
	base := validDocument(4)
	raw, _ := json.Marshal(base)
	var incoming Document
	if err := json.Unmarshal(raw, &incoming); err != nil {
		t.Fatal(err)
	}
	oldID := incoming.Components[1].ID
	incoming.Components[1].ID = "new-provider-article"
	for i := range incoming.LayoutManifest.Pages {
		for j := range incoming.LayoutManifest.Pages[i].Slots {
			slot := &incoming.LayoutManifest.Pages[i].Slots[j]
			if slot.ComponentID == oldID {
				slot.ComponentID = "new-provider-article"
			}
		}
	}
	separator := FixedSlot{ID: "fixed-new-provider-article-speakerSeparator", Element: "speakerSeparator", Box: Box{X: .4, Y: .3, Width: .03, Height: .03}, Style: ParagraphStyle{FontSize: 14, LineHeight: 14}}
	incoming.LayoutManifest.Pages[1].FixedSlots = append(incoming.LayoutManifest.Pages[1].FixedSlots, separator)
	normalized, _, err := ReconcileExtraction(base, incoming)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Components[1].ID != base.Components[1].ID {
		t.Fatal("fixture did not reconcile the article identity")
	}
	actual := normalized.LayoutManifest.Pages[1].FixedSlots[len(normalized.LayoutManifest.Pages[1].FixedSlots)-1]
	if actual.ID != "fixed-"+normalized.Components[1].ID+"-speakerSeparator" {
		t.Fatal("reader anchor reconciliation detached the native title separator")
	}
	if actual.Box != separator.Box || actual.Style != separator.Style {
		t.Fatal("identity reconciliation changed trusted separator geometry")
	}
}
