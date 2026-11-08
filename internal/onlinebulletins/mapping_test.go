package onlinebulletins

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestPublicationMappingUsesStableIdentityAndExplicitStructuralDecisions(t *testing.T) {
	previous := validDocument(4)
	copyDocument := func() Document {
		var next Document
		raw, _ := json.Marshal(previous)
		if err := json.Unmarshal(raw, &next); err != nil {
			t.Fatal(err)
		}
		return next
	}
	for _, scenario := range []string{"unchanged", "typo", "moved", "removed", "unresolved", "split", "merged"} {
		t.Run(scenario, func(t *testing.T) {
			next := copyDocument()
			var decisions []SentenceMapping
			want := scenario
			switch scenario {
			case "typo":
				next.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = "修正。"
				want = "unchanged"
			case "moved":
				next.Components[3], next.Components[4] = next.Components[4], next.Components[3]
			case "removed":
				next.Components[4].Items = []Item{}
				next.LayoutManifest.Pages[3].Slots = append(next.LayoutManifest.Pages[3].Slots[:1], next.LayoutManifest.Pages[3].Slots[2:]...)
			case "unresolved", "split", "merged":
				block := &next.Components[1].BodySection.Blocks[0]
				block.Sentences = []Sentence{{ID: "new-a", Spans: []Span{{Text: "正文。", FontRole: "body"}}}}
				next.LayoutManifest.Pages[1].Slots[0].Fragments = []Fragment{{SentenceID: "new-a", Start: 0, End: 3}}
				want = "requires_review"
				if scenario == "split" {
					block.Sentences = append(block.Sentences, Sentence{ID: "new-b", Spans: []Span{{Text: "延伸。", FontRole: "body"}}})
					next.LayoutManifest.Pages[1].Slots[0].Fragments = append(next.LayoutManifest.Pages[1].Slots[0].Fragments, Fragment{SentenceID: "new-b", End: 3})
					decisions = []SentenceMapping{{FromSentenceID: "sentence-body", Status: "split", ToSentenceIDs: []string{"new-a", "new-b"}}}
					want = "split"
				}
				if scenario == "merged" {
					next.Components[1].BodySection.Contributors = nil
					next.LayoutManifest.Pages[1].Slots = next.LayoutManifest.Pages[1].Slots[:2]
					decisions = []SentenceMapping{{FromSentenceID: "sentence-body", Status: "merged", ToSentenceIDs: []string{"new-a"}}, {FromSentenceID: "sentence-speaker", Status: "merged", ToSentenceIDs: []string{"new-a"}}}
					want = "merged"
				}
			}
			got, err := PublicationMappings(previous, next, decisions)
			if err != nil {
				t.Fatal(err)
			}
			id := "sentence-body"
			if scenario == "moved" {
				id = "sentence-summary"
			}
			if scenario == "removed" {
				id = "sentence-announcement"
			}
			found := false
			for _, mapping := range got {
				if mapping.FromSentenceID == id {
					found = true
					if mapping.Status != want {
						t.Fatalf("mapping=%+v want=%s", mapping, want)
					}
				}
			}
			if !found || len(got) != 13 {
				t.Fatalf("incomplete checkpoint: %+v", got)
			}
			again, err := PublicationMappings(previous, next, decisions)
			if err != nil || !reflect.DeepEqual(got, again) {
				t.Fatal("nondeterministic checkpoint", err)
			}
		})
	}
}

func TestPublicationMappingRejectsInvalidDecisionsAndCrossEdition(t *testing.T) {
	previous, next := validDocument(4), validDocument(4)
	for _, decisions := range [][]SentenceMapping{
		{{FromSentenceID: "unknown", Status: "removed", ToSentenceIDs: []string{}}},
		{{FromSentenceID: "sentence-body", Status: "unchanged", ToSentenceIDs: []string{"sentence-prayer"}}},
		{{FromSentenceID: "sentence-body", Status: "split", ToSentenceIDs: []string{"unknown", "sentence-prayer"}}},
		{{FromSentenceID: "sentence-body", Status: "split", ToSentenceIDs: []string{"sentence-body", "sentence-body"}}},
		{{FromSentenceID: "sentence-body", Status: "removed", ToSentenceIDs: []string{}}},
		{{FromSentenceID: "sentence-body", Status: "merged", ToSentenceIDs: []string{"sentence-body"}}},
		{{FromSentenceID: "sentence-body", Status: "requires_review", ToSentenceIDs: []string{}}},
	} {
		if _, err := PublicationMappings(previous, next, decisions); err == nil {
			t.Fatalf("invalid decisions accepted: %+v", decisions)
		}
	}
	next.ContentLocale = "en"
	if _, err := PublicationMappings(previous, next, nil); err == nil {
		t.Fatal("cross-edition migration")
	}
}

func TestPublicationMappingIncludesCanonicalAnchorsOnce(t *testing.T) {
	previous := validDocument(4)
	style := previous.Components[1].BodySection.Title.Style
	for _, page := range []int{0, 1} {
		previous.LayoutManifest.Pages[page].FixedSlots = []FixedSlot{{ID: "fixed-" + previous.Pages[page].ID, Element: "title", Box: Box{X: .1, Y: .1, Width: .8, Height: .1}, Style: style}}
	}
	got, err := PublicationMappings(previous, previous, nil)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, mapping := range got {
		if mapping.FromSentenceID == "canonical-title" {
			count++
			if mapping.Status != "unchanged" {
				t.Fatal(mapping)
			}
		}
	}
	if count != 1 || len(got) != 14 {
		t.Fatal("missing or duplicate canonical anchor", got)
	}
}
