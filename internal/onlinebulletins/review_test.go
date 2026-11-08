package onlinebulletins

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func TestPrivateAdoptedReviewEvidence(t *testing.T) {
	beforePath, incomingPath, afterPath := os.Getenv("HHC_PRIVATE_COMPARE_BEFORE"), os.Getenv("HHC_PRIVATE_COMPARE_INCOMING"), os.Getenv("HHC_PRIVATE_COMPARE_AFTER")
	if beforePath == "" || incomingPath == "" || afterPath == "" {
		t.Skip("requires private comparison snapshots")
	}
	type revision struct {
		Document     Document      `json:"document"`
		ReviewIssues []ReviewIssue `json:"reviewIssues"`
	}
	type envelope struct {
		Data struct {
			revision
			Local revision `json:"local"`
		} `json:"data"`
	}
	load := func(path string) envelope {
		t.Helper()
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var result envelope
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	before, incoming, after := load(beforePath), load(incomingPath), load(afterPath)
	got := MergeReviewIssues(incoming.Data.Document, after.Data.Local.Document, before.Data.Local.ReviewIssues, incoming.Data.ReviewIssues)
	if len(got) != 5 {
		t.Fatalf("adopted review count=%d, want 5", len(got))
	}
	for _, issue := range got {
		switch issue.Code {
		case "fixed_template_text_variant", "inferred_body_art_geometry", "unmapped_back_decoration", "unknown_source_image":
			if issue.SourcePage < 1 {
				t.Fatal("adopted finding lost current source location")
			}
		case "unmapped_fixed_template":
		default:
			t.Fatalf("stale finding retained: %s", issue.Code)
		}
	}
}

func TestMergeReviewIssuesRefreshesAdoptedVisualEvidence(t *testing.T) {
	component := Component{ID: "cover", Type: "cover", Cover: &Cover{Welcome: []Block{{ID: "welcome", Sentences: []Sentence{{ID: "one", Spans: []Span{{Text: "來源", FontRole: "body"}}}}}}}}
	incoming := Document{Components: []Component{component}}
	for _, code := range []string{"inferred_body_art_geometry", "inferred_body_header_geometry", "unmapped_back_decoration", "unknown_source_image", "unmapped_fixed_template", "fixed_template_text_variant", "source_metadata_confirmation", "canonical_source_mismatch"} {
		t.Run(code, func(t *testing.T) {
			old := ReviewIssue{Code: code, ComponentID: "cover", Blocking: true}
			fresh := old
			fresh.SourcePage = 1
			hard := ReviewIssue{Code: "future_hard_error", Blocking: true}
			got := MergeReviewIssues(incoming, incoming, []ReviewIssue{old, hard}, []ReviewIssue{fresh})
			if !reflect.DeepEqual(got, []ReviewIssue{hard, fresh}) {
				t.Fatalf("stale or missing evidence: %#v", got)
			}
			if got := MergeReviewIssues(incoming, incoming, []ReviewIssue{old}, nil); len(got) != 0 {
				t.Fatalf("corrected source retained old finding: %#v", got)
			}
			authored := Component{ID: "cover", Type: "cover", Cover: &Cover{Welcome: []Block{{ID: "welcome", Sentences: []Sentence{{ID: "one", Spans: []Span{{Text: "人工修改", FontRole: "body"}}}}}}}}
			got = MergeReviewIssues(incoming, Document{Components: []Component{authored}}, []ReviewIssue{old}, nil)
			if !reflect.DeepEqual(got, []ReviewIssue{old}) {
				t.Fatalf("authored evidence erased: %#v", got)
			}
			authored.Cover.Welcome[0].Sentences[0].Spans[0].Text = "來源"
			authored.Cover.Welcome[0].Sentences[0].ID = "local-only-anchor"
			got = MergeReviewIssues(incoming, Document{Components: []Component{authored}}, []ReviewIssue{old}, nil)
			if !reflect.DeepEqual(got, []ReviewIssue{old}) {
				t.Fatalf("unadopted geometry lost visual evidence: %#v", got)
			}
		})
	}
}

func TestMergeReviewIssuesDropsRetiredSectionFindings(t *testing.T) {
	incoming := Document{Components: []Component{{ID: "new-section", Type: "bodySection", BodySection: &BodySection{}}}}
	for _, code := range []string{"unknown_font_role", "ambiguous_body_heading", "ambiguous_inline_speaker", "inferred_inline_speaker_geometry", "non_uniform_source_tracking"} {
		old := ReviewIssue{Code: code, ComponentID: "retired-section", Blocking: true}
		if got := MergeReviewIssues(incoming, incoming, []ReviewIssue{old}, nil); len(got) != 0 {
			t.Fatalf("retired section still needs review: %#v", got)
		}
	}
}

func TestMergeReviewIssuesKeepsAuthoredAndUnknownFindings(t *testing.T) {
	component := Component{ID: "cover", Type: "cover", Cover: &Cover{Welcome: []Block{{ID: "welcome", Sentences: []Sentence{{ID: "one", Spans: []Span{{Text: "來源", FontRole: "body"}}}}}}}}
	incoming := Document{Components: []Component{component}}
	font := ReviewIssue{Code: "unknown_font_role", ComponentID: "cover", Blocking: true}
	hard := ReviewIssue{Code: "future_hard_error", ComponentID: "cover", Blocking: true}
	got := MergeReviewIssues(incoming, incoming, []ReviewIssue{font, hard}, nil)
	if len(got) != 1 || got[0] != hard {
		t.Fatal("adopted source did not refresh only known findings", got)
	}
	if got = MergeReviewIssues(incoming, incoming, []ReviewIssue{font}, []ReviewIssue{font}); len(got) != 1 || got[0] != font {
		t.Fatal("new source uncertainty lost", got)
	}
	authored := component
	authored.Cover = &Cover{Welcome: []Block{{ID: "welcome", Sentences: []Sentence{{ID: "one", Spans: []Span{{Text: "人工修改", FontRole: "body"}}}}}}}
	if got = MergeReviewIssues(incoming, Document{Components: []Component{authored}}, []ReviewIssue{font}, nil); len(got) != 1 || got[0] != font {
		t.Fatal("authored evidence cleared", got)
	}
	if got = MergeReviewIssues(incoming, Document{}, []ReviewIssue{font}, []ReviewIssue{font}); len(got) != 0 {
		t.Fatal("deleted component retained warnings", got)
	}
	geometry := ReviewIssue{Code: "non_uniform_source_tracking", ComponentID: "cover", Blocking: true}
	local := Document{Components: []Component{component, {ID: "local-only", Type: "bodySection"}}}
	if got = MergeReviewIssues(incoming, local, []ReviewIssue{geometry}, nil); len(got) != 1 || got[0] != geometry {
		t.Fatal("retained local geometry lost its review", got)
	}
}

func TestReviewRequiresEveryFindingAndCannotOverrideHardErrors(t *testing.T) {
	warning := ReviewIssue{Code: "inferred_body_art_geometry", ComponentID: "body", Blocking: true}
	for _, test := range []struct {
		name             string
		issues, accepted []ReviewIssue
		valid            bool
	}{
		{"clean", []ReviewIssue{}, []ReviewIssue{}, true},
		{"explicit", []ReviewIssue{warning}, []ReviewIssue{warning}, true},
		{"metadata", []ReviewIssue{{Code: "source_metadata_confirmation", SourcePage: 2, Blocking: true}}, []ReviewIssue{{Code: "source_metadata_confirmation", SourcePage: 2, Blocking: true}}, true},
		{"missing", []ReviewIssue{warning}, nil, false},
		{"duplicate", []ReviewIssue{warning}, []ReviewIssue{warning, warning}, false},
		{"invented", nil, []ReviewIssue{warning}, false},
		{"layout", []ReviewIssue{{Code: "layout_validation_required", Blocking: true}}, []ReviewIssue{{Code: "layout_validation_required", Blocking: true}}, false},
		{"unknown", []ReviewIssue{{Code: "future_error"}}, []ReviewIssue{{Code: "future_error"}}, false},
		{"unassigned", []ReviewIssue{{Code: "unsupported_source_structure", Blocking: true}}, []ReviewIssue{{Code: "unsupported_source_structure", Blocking: true}}, false},
		{"canonical", []ReviewIssue{{Code: "canonical_source_mismatch", ComponentID: "cover", SourcePage: 1, Blocking: true}}, []ReviewIssue{{Code: "canonical_source_mismatch", ComponentID: "cover", SourcePage: 1, Blocking: true}}, false},
		{"fixed-source", []ReviewIssue{{Code: "fixed_template_source_mismatch", ComponentID: "cover", SourcePage: 1, Blocking: true}}, []ReviewIssue{{Code: "fixed_template_source_mismatch", ComponentID: "cover", SourcePage: 1, Blocking: true}}, false},
		{"historical-proposal", []ReviewIssue{{Code: "unmapped_fixed_template", ComponentID: "cover", SourcePage: 1, Blocking: true}}, []ReviewIssue{{Code: "unmapped_fixed_template", ComponentID: "cover", SourcePage: 1, Blocking: true}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidateReview(test.issues, test.accepted); (err == nil) != test.valid {
				t.Fatalf("valid=%v err=%v", test.valid, err)
			}
		})
	}
}

func TestReviewRequiresEachDistinctSourceLocator(t *testing.T) {
	first := ReviewIssue{Code: "unknown_font_role", ComponentID: "body", BlockID: "first", SentenceID: "s1", SourcePage: 2, Blocking: true}
	second := first
	second.BlockID, second.SentenceID, second.SourcePage = "second", "s2", 3
	if err := ValidateReview([]ReviewIssue{first, second}, []ReviewIssue{first, second}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReview([]ReviewIssue{first, second}, []ReviewIssue{first, first}); err == nil {
		t.Fatal("one acceptance waived two source findings")
	}
}

func TestInformationalClassificationDoesNotRequireAcceptanceOrWaiveUnknownFailures(t *testing.T) {
	info := ReviewIssue{Code: "unknown_body_kind", Blocking: false}
	verify := ReviewIssue{Code: "unknown_font_role", Blocking: true}
	if err := ValidateReview([]ReviewIssue{info, verify}, []ReviewIssue{verify}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateReview([]ReviewIssue{info, verify}, []ReviewIssue{info, verify}); err != nil {
		t.Fatal("legacy exact acceptance rejected", err)
	}
	for _, issue := range []ReviewIssue{{Code: "unknown_body_kind", Blocking: true}, {Code: "future_failure", Blocking: false}, {Code: "source_crosses_exclusion_boundary", Blocking: false}} {
		if err := ValidateReview([]ReviewIssue{issue}, []ReviewIssue{issue}); err == nil {
			t.Fatal("unknown/integrity finding became informational", issue)
		}
	}
	if err := ValidateReview([]ReviewIssue{info, verify}, nil); err == nil {
		t.Fatal("required verification disappeared")
	}
	if err := ValidateReview([]ReviewIssue{info}, []ReviewIssue{info, info}); err == nil {
		t.Fatal("duplicate acceptance allowed")
	}
}
