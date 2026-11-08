package onlinebulletins

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestLearningRulesApplyBeforeClassificationWithoutChangingContent(t *testing.T) {
	base, local, evidence := learningDocuments(t)
	correction := CollectBodyKindCorrections(base, local, evidence)[0]
	rules := []LearningRuleInput{{Signature: correction.Signature, Version: 2, Kind: "sermon"}}
	worker := ExtractionWorker{
		Extract: func(context.Context, ExtractionJob) (DocumentCandidate, []ReviewIssue, error) {
			return DocumentCandidate{Document: &base, Evidence: evidence}, nil, nil
		},
		Classify: func(context.Context, []AmbiguousBlock) ([]Classification, error) {
			t.Fatal("verified rule still sent to LLM")
			return nil, nil
		},
	}
	candidate, _, err := worker.process(context.Background(), ExtractionJob{TemplateVersion: "v1", ExtractorVersion: ExtractorVersion, Canonical: CanonicalIssue{Metadata: CanonicalMetadata{IssueNumber: 1741}}, LearningRules: rules})
	if err != nil || !reflect.DeepEqual(candidate.Document.Components, local.Components) || len(candidate.LearningApplications) != 1 || candidate.LearningApplications[0].Version != 2 || candidate.LearningApplications[0].Signature != correction.Signature {
		t.Fatal("rule lost provenance or changed content", candidate.LearningApplications, err)
	}
	if candidate.LearningMetrics == nil || candidate.LearningMetrics.SourceEligible != 1 || candidate.LearningMetrics.UnknownEligible != 1 || candidate.LearningMetrics.Applied != 1 || candidate.LearningMetrics.RuleCount != 1 {
		t.Fatal("learning denominators lost", candidate.LearningMetrics)
	}
}

func TestLearningWorkerDoesNotApplyRulesBeforeSupportedIssues(t *testing.T) {
	base, local, evidence := learningDocuments(t)
	correction := CollectBodyKindCorrections(base, local, evidence)[0]
	classified := false
	worker := ExtractionWorker{
		Extract: func(context.Context, ExtractionJob) (DocumentCandidate, []ReviewIssue, error) {
			return DocumentCandidate{Document: &base, Evidence: evidence}, nil, nil
		},
		Classify: func(context.Context, []AmbiguousBlock) ([]Classification, error) {
			classified = true
			return nil, nil
		},
	}
	candidate, _, err := worker.process(context.Background(), ExtractionJob{TemplateVersion: "v1", ExtractorVersion: ExtractorVersion, Canonical: CanonicalIssue{Metadata: CanonicalMetadata{IssueNumber: 1738}}, LearningRules: []LearningRuleInput{{Signature: correction.Signature, Version: 2, Kind: "sermon"}}})
	if err != nil || !classified || len(candidate.LearningApplications) != 0 || candidate.Document.Components[1].BodySection.Kind != "unknown" {
		t.Fatal("learning changed a historical issue", err)
	}
}

func TestLearningRulesRejectBadSnapshotsAndLeaveOtherPatternsUntouched(t *testing.T) {
	for _, name := range []string{"unknown", "known-kind", "other-source", "simplified", "bad-version", "duplicate", "bad-kind"} {
		t.Run(name, func(t *testing.T) {
			base, local, evidence := learningDocuments(t)
			correction := CollectBodyKindCorrections(base, local, evidence)[0]
			rules := []LearningRuleInput{{Signature: correction.Signature, Version: 2, Kind: "sermon"}}
			switch name {
			case "unknown":
				rules[0].Signature = strings.Repeat("c", 64)
			case "known-kind":
				base.Components[1].BodySection.Kind = "testimony"
			case "other-source":
				evidence.SourceSHA256 = strings.Repeat("d", 64)
			case "simplified":
				base.ContentLocale = "zh-Hans"
			case "bad-version":
				rules[0].Version = 0
			case "duplicate":
				rules = append(rules, rules[0])
			case "bad-kind":
				rules[0].Kind = "invented"
			}
			before, _ := json.Marshal(base)
			applied, err := ApplyBodyKindRules(&base, evidence, rules)
			bad := name == "bad-version" || name == "duplicate" || name == "bad-kind"
			if len(applied) != 0 || (err != nil) != bad {
				t.Fatal("unsafe or mismatched rule accepted", applied, err)
			}
			after, _ := json.Marshal(base)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("unmatched source changed")
			}
		})
	}
}

func TestLearningApplicationsCannotInventProvenance(t *testing.T) {
	base, local, evidence := learningDocuments(t)
	correction := CollectBodyKindCorrections(base, local, evidence)[0]
	rule := LearningRuleInput{Signature: correction.Signature, Version: 2, Kind: "sermon"}
	for _, name := range []string{"valid", "version", "component", "kind", "duplicate", "source"} {
		t.Run(name, func(t *testing.T) {
			_, local, evidence := learningDocuments(t)
			candidate := DocumentCandidate{Document: &local, Evidence: evidence, LearningApplications: []LearningApplication{{ComponentID: "body", LearningRuleInput: rule}}}
			switch name {
			case "version":
				candidate.LearningApplications[0].Version++
			case "component":
				candidate.LearningApplications[0].ComponentID = "cover"
			case "kind":
				candidate.LearningApplications[0].Kind = "testimony"
			case "duplicate":
				candidate.LearningApplications = append(candidate.LearningApplications, candidate.LearningApplications[0])
			case "source":
				candidate.Evidence.Pages[0].Frames = nil
			}
			if ValidLearningApplications(candidate, []LearningRuleInput{rule}) != (name == "valid") {
				t.Fatal("invented application provenance accepted")
			}
		})
	}
}

func TestLearningRulesDecodeRejectsUnknownAndIncompleteInput(t *testing.T) {
	for _, value := range []string{`null`, `{}`, `[] []`, `[{"signature":"` + strings.Repeat("a", 64) + `","version":1,"kind":"sermon","enabled":true}]`} {
		if _, err := DecodeLearningRules([]byte(value)); err == nil {
			t.Fatal("invalid frozen input accepted", value)
		}
	}
	if rules, err := DecodeLearningRules(nil); err != nil || rules != nil {
		t.Fatal("legacy input changed")
	}
	if rules, err := DecodeLearningRules([]byte(`[]`)); err != nil || rules == nil || len(rules) != 0 {
		t.Fatal("empty explicit snapshot changed")
	}
}

func learningDocuments(t *testing.T) (Document, Document, GeometryDocument) {
	t.Helper()
	doc := validDocument(4)
	doc.LayoutManifest.RendererVersion = "v8"
	doc.Components[1].BodySection.Kind = "unknown"
	doc.Components[1].BodySection.Title.Sentences[0].Source = &SourceLocation{Page: 2, Box: Box{X: .1, Y: .3, Width: .05, Height: .02}}
	doc.Components[1].BodySection.Contributors[0].Name.Sentences[0].Source = &SourceLocation{Page: 2, Box: Box{X: .18, Y: .3, Width: .07, Height: .02}}
	geometry := GeometryDocument{SourceSHA256: doc.SourceAssetChecksum, PopplerVersion: PopplerVersion, Pages: []GeometryPage{{Number: 2, Width: 595.32, Height: 841.92,
		Texts: []GeometryText{
			{Text: "信息", FontFamily: "DFXingShu", FontSize: 16, Box: Box{X: .1, Y: .3, Width: .05, Height: .02}},
			{Text: "～", FontFamily: "DFXingShu", FontSize: 16, Box: Box{X: .16, Y: .3, Width: .02, Height: .02}},
			{Text: "講員", FontFamily: "DFXingShu", FontSize: 16, Box: Box{X: .18, Y: .3, Width: .07, Height: .02}},
		}, Frames: []GeometryFrame{{Element: "bodyHeadingFrame", Box: Box{X: .098, Y: .298, Width: .054, Height: .024}}}}}}
	var local Document
	raw, _ := json.Marshal(doc)
	if err := json.Unmarshal(raw, &local); err != nil {
		t.Fatal(err)
	}
	local.Components[1].BodySection.Kind = "sermon"
	return doc, local, geometry
}

func TestLearningCollectsOnlyActualSourceBackedKindCorrections(t *testing.T) {
	base, local, evidence := learningDocuments(t)
	before, _ := json.Marshal(base)
	corrections := CollectBodyKindCorrections(base, local, evidence)
	if len(corrections) != 1 || corrections[0].ComponentID != "body" || corrections[0].SourcePage != 2 || corrections[0].Kind != "sermon" || corrections[0].Title != "信息" || corrections[0].Speaker != "講員" || len(corrections[0].Signature) != 64 {
		t.Fatalf("missing actual located correction: %+v", corrections)
	}
	if got := CollectBodyKindCorrections(base, base, evidence); len(got) != 0 {
		t.Fatal("unchanged or waived warning became training data")
	}
	after, _ := json.Marshal(base)
	if !reflect.DeepEqual(before, after) || local.Components[1].BodySection.Kind != "sermon" {
		t.Fatal("learning mutated source or authored Local")
	}
}

func TestLearningRejectsChangedOrUnprovenSources(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Document, *Document, *GeometryDocument)
	}{
		{"source-replacement", func(_, local *Document, _ *GeometryDocument) { local.SourceAssetChecksum = "different" }},
		{"different-issue", func(_, local *Document, _ *GeometryDocument) { local.IssueID = "different" }},
		{"simplified", func(base, local *Document, _ *GeometryDocument) {
			base.ContentLocale, local.ContentLocale = "zh-Hans", "zh-Hans"
		}},
		{"changed-title", func(_, local *Document, _ *GeometryDocument) {
			local.Components[1].BodySection.Title.Sentences[0].Spans[0].Text = "改寫標題"
		}},
		{"changed-body", func(_, local *Document, _ *GeometryDocument) {
			local.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = "改寫內文"
		}},
		{"changed-anchor", func(_, local *Document, _ *GeometryDocument) {
			local.Components[1].BodySection.Title.Sentences[0].Source.Page++
		}},
		{"unmapped-frame", func(_, _ *Document, evidence *GeometryDocument) { evidence.Pages[0].Frames = nil }},
		{"ambiguous-frame", func(_, _ *Document, evidence *GeometryDocument) {
			evidence.Pages[0].Frames = append(evidence.Pages[0].Frames, evidence.Pages[0].Frames[0])
		}},
		{"unmatched-source-title", func(_, _ *Document, evidence *GeometryDocument) { evidence.Pages[0].Texts[0].Text = "另一標題" }},
		{"different-evidence", func(_, _ *Document, evidence *GeometryDocument) { evidence.SourceSHA256 = "different" }},
		{"unknown-kind", func(_, local *Document, _ *GeometryDocument) { local.Components[1].BodySection.Kind = "unknown" }},
		{"new-kind", func(_, local *Document, _ *GeometryDocument) { local.Components[1].BodySection.Kind = "invented" }},
		{"old-renderer", func(base, local *Document, _ *GeometryDocument) {
			base.LayoutManifest.RendererVersion, local.LayoutManifest.RendererVersion = "v1", "v1"
		}},
		{"oversized-title", func(base, local *Document, evidence *GeometryDocument) {
			value := strings.Repeat("字", 513)
			base.Components[1].BodySection.Title.Sentences[0].Spans[0].Text = value
			local.Components[1].BodySection.Title.Sentences[0].Spans[0].Text = value
			evidence.Pages[0].Texts[0].Text = value
		}},
		{"oversized-speaker", func(base, local *Document, evidence *GeometryDocument) {
			value := strings.Repeat("字", 257)
			base.Components[1].BodySection.Contributors[0].Name.Sentences[0].Spans[0].Text = value
			local.Components[1].BodySection.Contributors[0].Name.Sentences[0].Spans[0].Text = value
			evidence.Pages[0].Texts[2].Text = value
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, local, evidence := learningDocuments(t)
			tc.change(&base, &local, &evidence)
			if got := CollectBodyKindCorrections(base, local, evidence); len(got) != 0 {
				t.Fatalf("unproven correction learned: %+v", got)
			}
		})
	}
}

func TestLearningSignatureSeparatesSpeakerAndSourceTypography(t *testing.T) {
	base, local, evidence := learningDocuments(t)
	initial := CollectBodyKindCorrections(base, local, evidence)
	if len(initial) != 1 {
		t.Fatal("missing initial correction")
	}
	for _, tc := range []struct {
		name   string
		change func(*Document, *Document, *GeometryDocument)
	}{
		{"speaker", func(base, local *Document, evidence *GeometryDocument) {
			base.Components[1].BodySection.Contributors[0].Name.Sentences[0].Spans[0].Text = "牧師"
			local.Components[1].BodySection.Contributors[0].Name.Sentences[0].Spans[0].Text = "牧師"
			evidence.Pages[0].Texts[2].Text = "牧師"
		}},
		{"source-font", func(_, _ *Document, evidence *GeometryDocument) { evidence.Pages[0].Texts[0].FontFamily = "PMingLiU" }},
		{"source-size", func(_, _ *Document, evidence *GeometryDocument) { evidence.Pages[0].Texts[0].FontSize = 14 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, local, evidence := learningDocuments(t)
			tc.change(&base, &local, &evidence)
			got := CollectBodyKindCorrections(base, local, evidence)
			if len(got) != 1 || got[0].Signature == initial[0].Signature {
				t.Fatal("font-only or cross-speaker inference accepted")
			}
		})
	}
}
