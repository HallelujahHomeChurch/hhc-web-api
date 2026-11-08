package onlinebulletins

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMergeMovedParagraphRequiresChoiceForAuthoredText(t *testing.T) {
	base, local := validDocument(4), validDocument(4)
	local.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = "修正。"
	incoming, _, err := ReconcileExtraction(base, resegmentedDocument(t))
	if err != nil {
		t.Fatal(err)
	}
	newID := incoming.Components[2].ID
	before := comparisonJSON([]Document{base, local, incoming})
	choices := []ComparisonChoice{{ComponentID: "body", Choice: "incoming"}}
	if _, err := MergeDocuments(base, local, incoming, choices); err == nil {
		t.Fatal("accepting new structure silently discarded moved authored text")
	}
	for _, choice := range []string{"local", "incoming"} {
		draft, err := MergeDocuments(base, local, incoming, append(choices, ComparisonChoice{ComponentID: newID, Choice: choice}))
		if err != nil {
			t.Fatal(err)
		}
		want := "修正。"
		if choice == "incoming" {
			want = "正文。"
		}
		block := draft.Components[2].BodySection.Blocks[0]
		if block.Sentences[0].Spans[0].Text != want || block.ID != "block-body" {
			t.Fatalf("moved content choice %s lost: %+v", choice, block)
		}
	}
	if string(before) != string(comparisonJSON([]Document{base, local, incoming})) {
		t.Fatal("comparison or merge mutated immutable inputs")
	}
}

func TestMovedAuthoredParagraphIntoExistingSectionRequiresChoice(t *testing.T) {
	base, local, incoming := resegmentedDocument(t), resegmentedDocument(t), resegmentedDocument(t)
	local.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = "修正。"
	moved := incoming.Components[1].BodySection.Blocks[0]
	incoming.Components[2].BodySection.Blocks = append(incoming.Components[2].BodySection.Blocks, moved)
	replacement := Block{ID: "replacement", Style: moved.Style, Sentences: []Sentence{{ID: "replacement-sentence", Spans: []Span{{Text: "新增。", FontRole: "body"}}}}}
	incoming.Components[1].BodySection.Blocks = []Block{replacement}
	for i := range incoming.LayoutManifest.Pages[1].Slots {
		slot := &incoming.LayoutManifest.Pages[1].Slots[i]
		if slot.BlockID == moved.ID {
			slot.ComponentID = incoming.Components[2].ID
		}
	}
	incoming.LayoutManifest.Pages[1].Slots = append(incoming.LayoutManifest.Pages[1].Slots, Slot{ID: "replacement-slot", ComponentID: "body", BlockID: replacement.ID, Box: Box{X: .1, Y: .6, Width: .8, Height: .1}, Fragments: []Fragment{{SentenceID: "replacement-sentence", End: 3}}})
	if _, err := ValidateDocument(incoming); err != nil {
		t.Fatal("invalid fixture", err)
	}
	if _, err := MergeDocuments(base, base, incoming, nil); err != nil {
		t.Fatal("unedited movement should not require a decision", err)
	}
	before := comparisonJSON([]Document{base, local, incoming})
	choices := []ComparisonChoice{{ComponentID: "body", Choice: "incoming"}}
	if _, err := MergeDocuments(base, local, incoming, choices); err == nil {
		t.Fatal("existing destination silently replaced moved authored text")
	}
	units, err := CompareDocuments(base, local, incoming)
	if err != nil {
		t.Fatal(err)
	}
	for _, unit := range units {
		if unit.ComponentID == incoming.Components[2].ID {
			if unit.Suggestion != "conflict" || string(unit.Local) != string(comparisonJSON(local.Components[2])) {
				t.Fatalf("destination must retain the actual local structure: %+v", unit)
			}
		}
	}
	for _, choice := range []string{"local", "incoming"} {
		draft, err := MergeDocuments(base, local, incoming, []ComparisonChoice{{ComponentID: "body", Choice: choice}, {ComponentID: incoming.Components[2].ID, Choice: choice}})
		if err != nil {
			t.Fatal(err)
		}
		if choice == "local" && draft.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text != "修正。" {
			t.Fatal("keeping local lost authored text")
		}
		if choice == "incoming" && len(draft.Components[2].BodySection.Blocks) != 2 {
			t.Fatal("explicit incoming did not accept the moved paragraph")
		}
	}
	// Keeping the old source and accepting its moved destination would duplicate
	// the same authored anchor. Reject the whole merge instead of saving either.
	if _, err := MergeDocuments(base, local, incoming, []ComparisonChoice{{ComponentID: "body", Choice: "local"}, {ComponentID: incoming.Components[2].ID, Choice: "incoming"}}); err != ErrComparison {
		t.Fatalf("incompatible section choices must fail atomically: %v", err)
	}
	if string(before) != string(comparisonJSON([]Document{base, local, incoming})) {
		t.Fatal("comparison mutated immutable documents")
	}
}

func TestMergeMovedUneditedParagraphNeedsNoContentDecision(t *testing.T) {
	base, local := validDocument(4), validDocument(4)
	incoming, _, err := ReconcileExtraction(base, resegmentedDocument(t))
	if err != nil {
		t.Fatal(err)
	}
	draft, err := MergeDocuments(base, local, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	block := draft.Components[2].BodySection.Blocks[0]
	if block.ID != "block-body" || block.Sentences[0].Spans[0].Text != "正文。" {
		t.Fatalf("unchanged moved paragraph lost: %+v", block)
	}
}

func TestAuthoredParagraphSplitAndMergeRequireExplicitChoice(t *testing.T) {
	build := func(split bool) Document {
		document := validDocument(4)
		body := document.Components[1].BodySection
		second := Sentence{ID: "second-sentence", Spans: []Span{{Text: "後段。", FontRole: "body"}}}
		if split {
			body.Blocks = append(body.Blocks, Block{ID: "second-block", Style: body.Blocks[0].Style, Sentences: []Sentence{second}})
			document.LayoutManifest.Pages[1].Slots = append(document.LayoutManifest.Pages[1].Slots, Slot{ID: "second-slot", ComponentID: "body", BlockID: "second-block", Box: Box{X: .1, Y: .4, Width: .8, Height: .1}, Fragments: []Fragment{{SentenceID: second.ID, End: 3}}})
		} else {
			body.Blocks[0].Sentences = append(body.Blocks[0].Sentences, second)
			document.LayoutManifest.Pages[1].Slots[0].Fragments = append(document.LayoutManifest.Pages[1].Slots[0].Fragments, Fragment{SentenceID: second.ID, End: 3})
		}
		return document
	}
	for _, split := range []bool{false, true} {
		name := "merge"
		if split {
			name = "split"
		}
		t.Run(name, func(t *testing.T) {
			base, local, raw := build(!split), build(!split), build(split)
			local.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = "修正。"
			raw.SourceAssetChecksum = strings.Repeat("b", 64)
			incoming, _, err := ReconcileExtraction(base, raw)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := MergeDocuments(base, local, incoming, nil); err == nil {
				t.Fatal("structural change discarded an authored correction without a choice")
			}
			before := comparisonJSON([]Document{base, local, incoming})
			for _, choice := range []string{"local", "incoming"} {
				draft, err := MergeDocuments(base, local, incoming, []ComparisonChoice{{ComponentID: "body", Choice: choice}})
				if err != nil {
					t.Fatalf("%s: %v", choice, err)
				}
				var text strings.Builder
				for _, block := range draft.Components[1].BodySection.Blocks {
					for _, sentence := range block.Sentences {
						text.WriteString(sentenceIdentityText(sentence))
					}
				}
				want := "修正。後段。"
				if choice == "incoming" {
					want = "正文。後段。"
				}
				if text.String() != want {
					t.Fatalf("%s lost or duplicated text: %q", choice, text.String())
				}
			}
			if string(before) != string(comparisonJSON([]Document{base, local, incoming})) {
				t.Fatal("merge mutated immutable inputs")
			}
		})
	}
}

func TestThreeWayComparisonPreservesLocalAndRequiresConcurrentResolution(t *testing.T) {
	for _, scenario := range []struct{ local, incoming, want string }{
		{"正文。", "正文。", "same"},
		{"修正。", "正文。", "local"},
		{"正文。", "更新。", "incoming"},
		{"相同。", "相同。", "same"},
		{"修正。", "更新。", "conflict"},
	} {
		t.Run(scenario.want+scenario.local+scenario.incoming, func(t *testing.T) {
			base, local, incoming := validDocument(4), validDocument(4), validDocument(4)
			local.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = scenario.local
			incoming.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = scenario.incoming
			// Re-extraction changes IDs/provenance; those are not authored edits.
			incoming.Components[1].BodySection.Blocks[0].Sentences[0].ID = "new-body-sentence"
			incoming.LayoutManifest.Pages[1].Slots[0].Fragments[0].SentenceID = "new-body-sentence"
			incoming.Components[1].BodySection.Blocks[0].Sentences[0].Source = &SourceLocation{Page: 2, Box: Box{X: .1, Y: .1, Width: .8, Height: .1}}
			before, _ := json.Marshal(incoming)
			comparison, err := CompareDocuments(base, local, incoming)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, unit := range comparison {
				if unit.ComponentID == "body" && unit.BlockID == "block-body" {
					found = true
					if unit.Suggestion != scenario.want || unit.Base == nil || unit.Local == nil || unit.Incoming == nil {
						t.Fatalf("difference=%+v", unit)
					}
				}
			}
			if !found {
				t.Fatal("missing changed block")
			}
			after, _ := json.Marshal(incoming)
			if string(before) != string(after) {
				t.Fatal("comparison mutated source")
			}
		})
	}
}

func TestThreeWayComparisonMakesStructuralChangesExplicit(t *testing.T) {
	base, local, incoming := validDocument(4), validDocument(4), validDocument(4)
	incoming.Components[4].Items = []Item{}
	incoming.LayoutManifest.Pages[3].Slots = append(incoming.LayoutManifest.Pages[3].Slots[:1], incoming.LayoutManifest.Pages[3].Slots[2:]...)
	local.Components[4].Items[0].Blocks[0].Sentences[0].Spans[0].Text = "修正。"
	comparison, err := CompareDocuments(base, local, incoming)
	if err != nil {
		t.Fatal(err)
	}
	for _, unit := range comparison {
		if unit.ComponentID == "announcements" {
			if unit.BlockID != "" || unit.Suggestion != "conflict" {
				t.Fatalf("delete/edit was silently resolved: %+v", unit)
			}
			return
		}
	}
	t.Fatal("missing structural conflict")
}

func TestMergeDocumentsRequiresConflictChoiceAndPreservesTypoIdentity(t *testing.T) {
	base, local, incoming := validDocument(4), validDocument(4), validDocument(4)
	local.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = "管理改。"
	local.LayoutManifest.Pages[1].Slots[0].Fragments[0].End = 4
	incoming.Components[1].BodySection.Blocks[0].Sentences[0] = Sentence{ID: "incoming-id", Spans: []Span{{Text: "新修正。", FontRole: "body"}}}
	incoming.LayoutManifest.Pages[1].Slots[0].Fragments[0] = Fragment{SentenceID: "incoming-id", End: 4}
	if _, err := MergeDocuments(base, local, incoming, nil); err == nil {
		t.Fatal("concurrent edit overwritten")
	}
	for _, choice := range []string{"local", "incoming", "manual"} {
		decision := ComparisonChoice{ComponentID: "body", BlockID: "block-body", Choice: choice}
		if choice == "manual" {
			var block Block
			if err := json.Unmarshal(comparisonJSON(local.Components[1].BodySection.Blocks[0]), &block); err != nil {
				t.Fatal(err)
			}
			block.Sentences[0].Spans = []Span{{Text: "人工合併。", FontRole: "body"}}
			decision.Value = comparisonJSON(block)
		}
		draft, err := MergeDocuments(base, local, incoming, []ComparisonChoice{decision})
		if err != nil {
			t.Fatal(err)
		}
		sentence := draft.Components[1].BodySection.Blocks[0].Sentences[0]
		want := map[string]string{"local": "管理改。", "incoming": "新修正。", "manual": "人工合併。"}[choice]
		if sentence.ID != "sentence-body" || sentence.Spans[0].Text != want || draft.LayoutManifest.LayoutValidationHash != "" {
			t.Fatal("wrong merge or anchor", sentence)
		}
	}
}

func TestMergeDocumentsAutomaticallyCombinesIndependentBlocks(t *testing.T) {
	base, local, incoming := validDocument(4), validDocument(4), validDocument(4)
	local.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = "我改。"
	incoming.Components[4].Items[0].Blocks[0].Sentences[0].Spans[0].Text = "他改。"
	draft, err := MergeDocuments(base, local, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text != "我改。" || draft.Components[4].Items[0].Blocks[0].Sentences[0].Spans[0].Text != "他改。" {
		t.Fatal("independent edit lost")
	}
	if _, err = MergeDocuments(base, local, incoming, []ComparisonChoice{{ComponentID: "unknown", Choice: "local"}}); err == nil {
		t.Fatal("unknown decision ignored")
	}
}

func TestReextractionAdoptsNewRendererWithoutOverwritingAuthoredText(t *testing.T) {
	base, local, incoming := validDocument(4), validDocument(4), validDocument(4)
	local.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = "手改。"
	incoming.LayoutManifest.RendererVersion = "v3"
	incoming.LayoutManifest.RendererArtifactSha256 = strings.Repeat("c", 64)
	draft, err := MergeDocuments(base, local, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	if draft.LayoutManifest.RendererVersion != "v3" || draft.LayoutManifest.RendererArtifactSha256 != incoming.LayoutManifest.RendererArtifactSha256 || draft.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text != "手改。" {
		t.Fatal("re-extraction lost renderer or authored text")
	}
	if local.LayoutManifest.RendererVersion != "v1" {
		t.Fatal("prior revision mutated")
	}
}

func TestMergeRejectsManualSourceCoordinates(t *testing.T) {
	base, local, incoming := validDocument(4), validDocument(4), validDocument(4)
	block := local.Components[1].BodySection.Blocks[0]
	block.Sentences[0].Source = &SourceLocation{Page: 1, Box: Box{X: .1, Y: .1, Width: .8, Height: .1}}
	choice := ComparisonChoice{ComponentID: "body", BlockID: "block-body", Choice: "manual", Value: comparisonJSON(block)}
	if _, err := MergeDocuments(base, local, incoming, []ComparisonChoice{choice}); err == nil {
		t.Fatal("manual source coordinates accepted")
	}
}

func TestMergeIncomingParagraphAdditionKeepsExistingAnchors(t *testing.T) {
	base, local, incoming := validDocument(4), validDocument(4), validDocument(4)
	body := incoming.Components[1].BodySection
	body.Blocks = append(body.Blocks, Block{ID: "added-block", Style: body.Blocks[0].Style, Sentences: []Sentence{{ID: "added-sentence", Spans: []Span{{Text: "新增。", FontRole: "body"}}}}})
	incoming.LayoutManifest.Pages[1].Slots = append(incoming.LayoutManifest.Pages[1].Slots, Slot{ID: "added-slot", ComponentID: "body", BlockID: "added-block", Box: Box{X: .1, Y: .3, Width: .8, Height: .1}, Fragments: []Fragment{{SentenceID: "added-sentence", End: 3}}})
	draft, err := MergeDocuments(base, local, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Components[1].BodySection.Blocks) != 2 || draft.Components[1].BodySection.Blocks[0].Sentences[0].ID != "sentence-body" {
		t.Fatal("addition displaced original anchor")
	}
	if _, err = ValidateDocument(draft); err != nil {
		t.Fatal(err)
	}
}

func TestMergeRetainedLocalSectionKeepsSourceReviewGate(t *testing.T) {
	base, local, incoming := validDocument(4), validDocument(4), validDocument(4)
	incoming.Components[1].ID = "new-body"
	for i := range incoming.LayoutManifest.Pages[1].Slots {
		incoming.LayoutManifest.Pages[1].Slots[i].ComponentID = "new-body"
	}
	incoming.LayoutManifest.Pages[0].FixedSlots = []FixedSlot{{ID: "restored-title", Element: "title", Box: Box{X: .1, Y: .02, Width: .8, Height: .05}, Style: ParagraphStyle{FontSize: 16, LineHeight: 24}}}
	choices := []ComparisonChoice{{Choice: "local"}, {ComponentID: "body", Choice: "local"}, {ComponentID: "new-body", Choice: "local"}}
	draft, err := MergeDocuments(base, local, incoming, choices)
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.LayoutManifest.Pages[0].FixedSlots) != 0 || IncomingGeometryCovers(incoming, draft.Components) {
		t.Fatal("retained local geometry must retain its source review gate")
	}
	draft, err = MergeDocuments(base, local, incoming, nil)
	if err != nil || len(draft.LayoutManifest.Pages[0].FixedSlots) != 1 || !IncomingGeometryCovers(incoming, draft.Components) {
		t.Fatal("incoming geometry must refresh its source review gate", err)
	}
}

func TestMergeSameWordsUsesIncomingSourceAnchors(t *testing.T) {
	for _, hymn := range []bool{false, true} {
		t.Run(map[bool]string{false: "paragraph", true: "hymn"}[hymn], func(t *testing.T) {
			base, local, incoming := validDocument(4), validDocument(4), validDocument(4)
			block := &incoming.Components[1].BodySection.Blocks[0]
			component := "body"
			if hymn {
				block = &incoming.Components[2].HymnLyrics.Hymns[0].Sections[0].Lines[0]
				component = "hymns"
			}
			oldBlock, oldSentence := block.ID, block.Sentences[0].ID
			block.ID, block.Sentences[0].ID = "new-"+oldBlock, "new-"+oldSentence
			for i := range incoming.LayoutManifest.Pages {
				for j := range incoming.LayoutManifest.Pages[i].Slots {
					slot := &incoming.LayoutManifest.Pages[i].Slots[j]
					if slot.BlockID == oldBlock {
						slot.BlockID = block.ID
						slot.Fragments[0].SentenceID = block.Sentences[0].ID
						slot.Box.X = .15
					}
				}
			}
			incoming.LayoutManifest.Pages[0].FixedSlots = []FixedSlot{{ID: "new-title", Element: "title", Box: Box{X: .1, Y: .02, Width: .8, Height: .05}, Style: ParagraphStyle{FontSize: 16, LineHeight: 24}}}
			before := comparisonJSON([]Document{base, local, incoming})
			draft, err := MergeDocuments(base, local, incoming, nil)
			if err != nil {
				t.Fatal(err)
			}
			if !IncomingGeometryCovers(incoming, draft.Components) || len(draft.LayoutManifest.Pages[0].FixedSlots) != 1 {
				t.Fatal("same words retained obsolete source geometry")
			}
			if issues := MergeReviewIssues(incoming, draft, []ReviewIssue{{Code: "unmapped_fixed_template", Blocking: true}}, nil); len(issues) != 0 {
				t.Fatal("adopted source retained obsolete findings", issues)
			}
			mappings, err := PublicationMappings(local, draft, nil)
			if err != nil {
				t.Fatal(err)
			}
			pending := false
			for _, mapping := range mappings {
				if mapping.FromSentenceID == oldSentence {
					pending = mapping.Status == "requires_review"
				}
			}
			if !pending {
				t.Fatalf("%s reidentified anchor silently moved notes", component)
			}
			if string(before) != string(comparisonJSON([]Document{base, local, incoming})) {
				t.Fatal("merge mutated immutable inputs")
			}
		})
	}
}

func TestMergeIncomingSectionUsesServerExtractionGeometry(t *testing.T) {
	base, local, incoming := validDocument(4), validDocument(4), validDocument(4)
	var component Component
	if err := json.Unmarshal(comparisonJSON(incoming.Components[1]), &component); err != nil {
		t.Fatal(err)
	}
	component.ID = "new-section"
	for _, block := range componentBlocks(&component) {
		block.ID = "new-" + block.ID
		block.Sentences[0].ID = "new-" + block.Sentences[0].ID
		incoming.LayoutManifest.Pages[1].Slots = append(incoming.LayoutManifest.Pages[1].Slots, Slot{ID: "slot-" + block.ID, ComponentID: component.ID, BlockID: block.ID, Box: Box{X: .1, Y: .5, Width: .8, Height: .1}, Fragments: []Fragment{{SentenceID: block.Sentences[0].ID, End: len([]rune(sentenceIdentityText(block.Sentences[0])))}}})
	}
	incoming.Components = append(incoming.Components[:2], append([]Component{component}, incoming.Components[2:]...)...)
	if _, err := ValidateDocument(incoming); err != nil {
		t.Fatal("incoming fixture", err)
	}
	draft, err := MergeDocuments(base, local, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Components) != 7 || draft.Components[2].ID != "new-section" {
		t.Fatal("section addition lost")
	}
	if _, err = ValidateDocument(draft); err != nil {
		t.Fatal(err)
	}
}

func TestSourceIllustrationsSurviveSameSourceAndRequireCurrentSourceSelectionAfterReplacement(t *testing.T) {
	base := validDocument(4)
	base.TemplateSnapshot = &TemplateSettings{Version: 1, TemplateText: TemplateText{VisionMission: "使命", VisionFellowship: "同行", VisionCommitment: "回應"}}
	base.LayoutManifest.RendererVersion, base.LayoutManifest.RendererArtifactSha256 = "v10", RendererV11SHA256
	part := SourcePart{ID: strings.Repeat("d", 64), Kind: "image", Page: 2, Box: Box{X: .2, Y: .4, Width: .4, Height: .1}}
	local, err := ApplySourceRepair(base, PreparedSourceRepair{Part: part, Input: SourceRepairInput{SourceChecksum: base.SourceAssetChecksum, PartID: part.ID, ComponentID: "body", Alt: "Source picture"}, RendererSHA256: RendererV11SHA256, Illustration: &Illustration{AssetID: "b08a80e8-6388-4166-9a2a-23d336f74b7b", SHA256: strings.Repeat("e", 64), SizeBytes: 128}})
	if err != nil {
		t.Fatal(err)
	}
	merged, err := MergeDocuments(base, local, base, nil)
	if err != nil || len(merged.Components[1].Illustrations) != 1 {
		t.Fatal("same-source picture lost", err)
	}
	incoming := base
	incoming.SourceAssetChecksum = strings.Repeat("f", 64)
	units, err := CompareDocuments(base, local, incoming)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, unit := range units {
		if unit.ComponentID == "body" && unit.BlockID == "" {
			found = unit.SourceRepairRequired && unit.Suggestion == "conflict"
		}
	}
	if !found {
		t.Fatal("replaced source did not require explicit image selection")
	}
	if _, err := MergeDocuments(base, local, incoming, []ComparisonChoice{{ComponentID: "body", Choice: "local"}}); err == nil {
		t.Fatal("old-source image copied without source proof")
	}
	merged, err = MergeDocuments(base, local, incoming, []ComparisonChoice{{ComponentID: "body", Choice: "incoming"}})
	if err != nil || len(merged.Components[1].Illustrations) != 0 {
		t.Fatal("incoming source could not resolve picture", err)
	}
}
