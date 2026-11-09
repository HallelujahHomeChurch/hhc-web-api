package onlinebulletins

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func semanticMappingCopy(t *testing.T, d Document) Document {
	t.Helper()
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var copy Document
	if err = json.Unmarshal(data, &copy); err != nil {
		t.Fatal(err)
	}
	return copy
}
func semanticMappingRename(d *Document, from, to, text string) {
	for i := range d.Components {
		for _, b := range componentBlocks(&d.Components[i]) {
			for j := range b.Sentences {
				s := &b.Sentences[j]
				if s.ID == from {
					s.ID = to
					if text != "" {
						s.Spans = []Span{{Text: text, FontRole: "body"}}
					}
				}
			}
		}
	}
	for i := range d.LayoutManifest.Pages {
		for j := range d.LayoutManifest.Pages[i].Slots {
			for k := range d.LayoutManifest.Pages[i].Slots[j].Fragments {
				f := &d.LayoutManifest.Pages[i].Slots[j].Fragments[k]
				if f.SentenceID == from {
					f.SentenceID = to
					if text != "" {
						f.End = utf8.RuneCountInString(text)
					}
				}
			}
		}
	}
}
func semanticMappingFor(t *testing.T, mappings []SentenceMapping, id string) SentenceMapping {
	t.Helper()
	for _, m := range mappings {
		if m.FromSentenceID == id {
			return m
		}
	}
	t.Fatalf("missing %s", id)
	return SentenceMapping{}
}
func TestSemanticMappingPreservesEquivalentIdentityAndTrustedContent(t *testing.T) {
	before := validDocument(4)
	next := semanticMappingCopy(t, before)
	semanticMappingRename(&next, "sentence-speaker", "new-speaker", "Speaker")
	next.Components[1].BodySection.Contributors[0].Name.Sentences[0].Source = &SourceLocation{Page: 2, Box: Box{X: .1, Y: .1, Width: .2, Height: .02}}
	next.LayoutManifest.ContentHash = strings.Repeat("b", 64)
	next.LayoutManifest.LayoutValidationHash = strings.Repeat("c", 64)
	savedBefore, savedNext := semanticMappingCopy(t, before), semanticMappingCopy(t, next)
	got, mappings, err := ApplySemanticMappings(before, next, []SemanticMappingDecision{{FromSentenceID: "sentence-speaker", Kind: "equivalent", ToSentenceIDs: []string{"new-speaker"}}})
	if err != nil {
		t.Fatal(err)
	}
	name := got.Components[1].BodySection.Contributors[0].Name.Sentences[0]
	if name.ID != "sentence-speaker" || name.Spans[0].Text != "Speaker" || name.Source == nil || name.Source.Page != 2 {
		t.Fatal("lost trusted content/source", name)
	}
	m := semanticMappingFor(t, mappings, "sentence-speaker")
	if len(m.ToSentenceIDs) != 1 || m.ToSentenceIDs[0] != "sentence-speaker" || m.Status == "requires_review" {
		t.Fatal(m)
	}
	if got.LayoutManifest.ContentHash != "" || got.LayoutManifest.LayoutValidationHash != "" {
		t.Fatal("reused stale proof")
	}
	if !reflect.DeepEqual(before, savedBefore) || !reflect.DeepEqual(next, savedNext) {
		t.Fatal("mutated source snapshots")
	}
	if _, err = ValidateDocument(got); err != nil {
		t.Fatal(err)
	}
}
func TestSemanticMappingHistoricalFallbackNeverAttachesToChangedText(t *testing.T) {
	for _, retained := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh_id", true: "reused_id"}[retained], func(t *testing.T) {
			before := validDocument(4)
			next := semanticMappingCopy(t, before)
			target := "new-body"
			if retained {
				target = "sentence-body"
			}
			semanticMappingRename(&next, "sentence-body", target, "Different message.")
			got, mappings, err := ApplySemanticMappings(before, next, nil)
			if err != nil {
				t.Fatal(err)
			}
			m := semanticMappingFor(t, mappings, "sentence-body")
			if m.Status != "removed" || len(m.ToSentenceIDs) != 0 {
				t.Fatal("uncertain notes attached", m)
			}
			s := got.Components[1].BodySection.Blocks[0].Sentences[0]
			if s.ID == "sentence-body" || s.Spans[0].Text != "Different message." {
				t.Fatal(s)
			}
			if _, err = ValidateDocument(got); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestSemanticMappingEquivalentCorrectionCanKeepExistingIdentity(t *testing.T) {
	before := validDocument(4)
	next := semanticMappingCopy(t, before)
	semanticMappingRename(&next, "sentence-body", "sentence-body", "Corrected message.")
	got, mappings, err := ApplySemanticMappings(before, next, []SemanticMappingDecision{{FromSentenceID: "sentence-body", Kind: "equivalent", ToSentenceIDs: []string{"sentence-body"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Components[1].BodySection.Blocks[0].Sentences[0].ID != "sentence-body" || semanticMappingFor(t, mappings, "sentence-body").Status != "unchanged" {
		t.Fatal("correction lost anchor")
	}
}
func TestSemanticMappingUsesExistingSplitAndMergeContract(t *testing.T) {
	for _, kind := range []string{"split", "merged"} {
		t.Run(kind, func(t *testing.T) {
			before := validDocument(4)
			next := semanticMappingCopy(t, before)
			semanticMappingRename(&next, "sentence-body", "new-a", "")
			decisions := []SemanticMappingDecision{{FromSentenceID: "sentence-body", Kind: kind, ToSentenceIDs: []string{"new-a"}}}
			if kind == "split" {
				b := &next.Components[1].BodySection.Blocks[0]
				b.Sentences = append(b.Sentences, Sentence{ID: "new-b", Spans: []Span{{Text: "More.", FontRole: "body"}}})
				next.LayoutManifest.Pages[1].Slots[0].Fragments = append(next.LayoutManifest.Pages[1].Slots[0].Fragments, Fragment{SentenceID: "new-b", End: 5})
				decisions[0].ToSentenceIDs = append(decisions[0].ToSentenceIDs, "new-b")
			} else {
				next.Components[1].BodySection.Contributors = nil
				next.LayoutManifest.Pages[1].Slots = next.LayoutManifest.Pages[1].Slots[:2]
				decisions = append(decisions, SemanticMappingDecision{FromSentenceID: "sentence-speaker", Kind: kind, ToSentenceIDs: []string{"new-a"}})
			}
			_, mappings, err := ApplySemanticMappings(before, next, decisions)
			if err != nil {
				t.Fatal(err)
			}
			if m := semanticMappingFor(t, mappings, "sentence-body"); m.Status != kind {
				t.Fatal(m)
			}
		})
	}
}
func TestSemanticMappingRejectsInventedOrStolenCorrespondence(t *testing.T) {
	before := validDocument(4)
	next := semanticMappingCopy(t, before)
	semanticMappingRename(&next, "sentence-body", "new-body", "")
	semanticMappingRename(&next, "sentence-speaker", "new-speaker", "")
	cases := [][]SemanticMappingDecision{
		{{FromSentenceID: "unknown", Kind: "historical", ToSentenceIDs: []string{}}},
		{{FromSentenceID: "sentence-body", Kind: "equivalent", ToSentenceIDs: []string{"unknown"}}},
		{{FromSentenceID: "sentence-body", Kind: "equivalent", ToSentenceIDs: []string{"sentence-body-title"}}},
		{{FromSentenceID: "sentence-body", Kind: "equivalent", ToSentenceIDs: []string{"new-body"}}, {FromSentenceID: "sentence-speaker", Kind: "equivalent", ToSentenceIDs: []string{"new-body"}}},
		{{FromSentenceID: "sentence-body", Kind: "equivalent", ToSentenceIDs: []string{"new-body", "new-speaker"}}},
		{{FromSentenceID: "sentence-body", Kind: "merged", ToSentenceIDs: []string{"new-body"}}},
		{{FromSentenceID: "sentence-body", Kind: "split", ToSentenceIDs: []string{"new-body", "new-body"}}},
		{{FromSentenceID: "sentence-body", Kind: "historical", ToSentenceIDs: []string{"new-body"}}},
	}
	for i, d := range cases {
		if _, _, err := ApplySemanticMappings(before, next, d); err == nil {
			t.Fatalf("accepted case %d", i)
		}
	}
	next.ContentLocale = "en"
	if _, _, err := ApplySemanticMappings(before, next, nil); err == nil {
		t.Fatal("cross-edition migration")
	}
}
