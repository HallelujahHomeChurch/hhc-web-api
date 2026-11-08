package onlinebulletins

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestMergePreservesCrossPageFragmentsWithRestoredSentenceIDs(t *testing.T) {
	base := validDocument(4)
	body := base.Components[1].BodySection
	body.Blocks[0].Sentences[0].Spans[0].Text = "跨頁正文。"
	first := &base.LayoutManifest.Pages[1].Slots[0]
	first.Fragments[0].End = 2
	base.LayoutManifest.Pages[2].Slots = append(base.LayoutManifest.Pages[2].Slots, Slot{
		ID: "continuation", ComponentID: "body", BlockID: "block-body", ContinuationOf: first.ID,
		Box:       Box{X: .1, Y: .1, Width: .8, Height: .1},
		Fragments: []Fragment{{SentenceID: "sentence-body", Start: 2, End: 5}},
	})
	correction := Block{ID: "correction", Style: body.Blocks[0].Style, Sentences: []Sentence{{ID: "parser-correction", Spans: []Span{{Text: "錯字。", FontRole: "body"}}}}}
	body.Blocks = append(body.Blocks, correction)
	base.LayoutManifest.Pages[1].Slots = append(base.LayoutManifest.Pages[1].Slots, Slot{
		ID: "correction-slot", ComponentID: "body", BlockID: correction.ID,
		Box: Box{X: .1, Y: .3, Width: .8, Height: .1}, Fragments: []Fragment{{SentenceID: "parser-correction", End: 3}},
	})
	clone := func(document Document) Document {
		var result Document
		if err := json.Unmarshal(comparisonJSON(document), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	local, incoming := clone(base), clone(base)
	incoming.LayoutManifest.Pages[1].Slots[0].Box.Y = .2
	for _, block := range componentBlocks(&local.Components[1]) {
		for i := range block.Sentences {
			if block.ID == "block-body" || block.ID == "correction" {
				block.Sentences[i].ID = "published-" + block.Sentences[i].ID
			}
		}
	}
	for i := range local.LayoutManifest.Pages {
		for j := range local.LayoutManifest.Pages[i].Slots {
			slot := &local.LayoutManifest.Pages[i].Slots[j]
			if slot.BlockID == "block-body" || slot.BlockID == "correction" {
				for k := range slot.Fragments {
					slot.Fragments[k].SentenceID = "published-" + slot.Fragments[k].SentenceID
				}
			}
		}
	}
	local.Components[1].BodySection.Blocks[1].Sentences[0].Spans[0].Text = "正字。"
	before := string(comparisonJSON([]Document{base, local, incoming}))
	draft, err := MergeDocuments(base, local, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	if draft.Components[1].BodySection.Blocks[1].Sentences[0].Spans[0].Text != "正字。" {
		t.Fatal("authored correction lost")
	}
	// The repository restores published identities after merging; it must retain
	// the extraction's cross-page allocations as well as the reader anchors.
	draft, _, err = RestorePublishedSentenceIdentities(local, incoming, draft)
	if err != nil {
		t.Fatal(err)
	}
	for i, page := range incoming.LayoutManifest.Pages {
		var expected, actual []Slot
		for _, slot := range page.Slots {
			if slot.BlockID == "block-body" {
				slot.Fragments = append([]Fragment(nil), slot.Fragments...)
				for j := range slot.Fragments {
					slot.Fragments[j].SentenceID = "published-" + slot.Fragments[j].SentenceID
				}
				expected = append(expected, slot)
			}
		}
		for _, slot := range draft.LayoutManifest.Pages[i].Slots {
			if slot.BlockID == "block-body" {
				actual = append(actual, slot)
			}
		}
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("page %d lost cross-page allocation: got %+v want %+v", i+1, actual, expected)
		}
	}
	if string(comparisonJSON([]Document{base, local, incoming})) != before {
		t.Fatal("merge mutated immutable inputs")
	}
	for _, change := range []string{"different source", "different page", "unrecognized anchor"} {
		t.Run(change, func(t *testing.T) {
			next, chosen := clone(incoming), clone(draft)
			switch change {
			case "different source":
				next.SourceAssetChecksum = strings.Repeat("d", 64)
			case "different page":
				next.Pages[1].Width++
			case "unrecognized anchor":
				chosen.Components[1].BodySection.Blocks[1].Sentences[0].ID = "new-authored-anchor"
			}
			if _, ok := sameSourceMergeGeometry(local, next, chosen.Components); ok {
				t.Fatal("unproven local geometry was accepted")
			}
		})
	}
	fresh := ReviewIssue{Code: "unknown_font_role", ComponentID: "body", BlockID: "block-body", SentenceID: "sentence-body", Blocking: true}
	hard := ReviewIssue{Code: "future_hard_error", Blocking: true}
	stale := ReviewIssue{Code: "fixed_template_source_mismatch", Blocking: true}
	// A mixed draft does not count as whole-source adoption for review gates.
	if issues := MergeReviewIssues(incoming, draft, []ReviewIssue{stale, hard}, []ReviewIssue{fresh}); !reflect.DeepEqual(issues, []ReviewIssue{stale, hard, fresh}) {
		t.Fatalf("mixed-source review gates changed: %+v", issues)
	}
}
