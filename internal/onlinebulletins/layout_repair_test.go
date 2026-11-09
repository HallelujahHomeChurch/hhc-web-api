package onlinebulletins

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestLayoutRepairChangesOnlyLocatedPresentationWithinReadableBounds(t *testing.T) {
	document := validDocument(4)
	job := LayoutJob{Document: document, Canonical: CanonicalMetadata{Title: "週報", IssueNumber: 2000, Date: "2026-11-08"}, Automatic: true}
	issues := []ReviewIssue{{Code: "layout_validation_failed", ComponentID: "body", BlockID: "block-body", SourcePage: 2, Blocking: true}}
	plan := LayoutRepairPlan{Styles: []LayoutStyleDecision{{BlockID: "block-body", FontSize: 14, LineHeight: 20, SpaceBefore: 0, SpaceAfter: 0, LetterSpacing: 0}}}
	before := semanticMappingCopy(t, document)
	got, err := ApplyLayoutRepair(job, issues, plan)
	if err != nil || got.Components[1].BodySection.Blocks[0].Style.FontSize != 14 {
		t.Fatal("presentation repair rejected", err)
	}
	if !reflect.DeepEqual(before, document) || got.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text != "正文。" || !validComposedPresentation(document, got) {
		t.Fatal("repair changed source semantics/provenance")
	}
	for _, bad := range []LayoutRepairPlan{
		{Styles: []LayoutStyleDecision{{BlockID: "invented", FontSize: 14, LineHeight: 20}}},
		{Styles: []LayoutStyleDecision{{BlockID: "block-welcome", FontSize: 14, LineHeight: 20}}},
		{Styles: []LayoutStyleDecision{{BlockID: "block-body", FontSize: 5, LineHeight: 20}}},
		{Styles: []LayoutStyleDecision{{BlockID: "block-body", FontSize: 24, LineHeight: 30}}},
		{Styles: []LayoutStyleDecision{{BlockID: "block-body", FontSize: 14, LineHeight: 14}}},
	} {
		if _, err = ApplyLayoutRepair(job, issues, bad); err == nil {
			t.Fatal("unsafe/unlocated repair accepted")
		}
	}
	job.Automatic = false
	if _, err = ApplyLayoutRepair(job, issues, plan); err == nil {
		t.Fatal("draft acquired automatic repair authority")
	}
}

func TestWorkerRepairsOnlyAuthorizedLocatedLayoutFailure(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		doc := validDocument(4)
		store := &extractionStoreStub{layoutJobs: []LayoutJob{{Document: doc, Automatic: automatic}}}
		calls := 0
		worker := ExtractionWorker{Store: store, BatchSize: 1, MaxAttempts: 3, Timeout: 120 * time.Second, Extract: func(context.Context, ExtractionJob) (DocumentCandidate, []ReviewIssue, error) {
			t.Fatal("unexpected extraction")
			return DocumentCandidate{}, nil, nil
		}, Validate: func(context.Context, Document, CanonicalMetadata) (LayoutResult, error) {
			return LayoutResult{}, &LayoutReviewError{Issues: []ReviewIssue{{Code: "layout_validation_failed", ComponentID: "body", BlockID: "block-body", SourcePage: 2, Blocking: true}}}
		}, RepairLayout: func(ctx context.Context, job LayoutJob, issues []ReviewIssue) (LayoutResult, error) {
			calls++
			if len(issues) != 1 || issues[0].BlockID != "block-body" {
				t.Fatal("lost native diagnostic")
			}
			return LayoutResult{Document: doc}, nil
		}}
		if err := worker.RunBatch(context.Background()); err != nil {
			t.Fatal(err)
		}
		if automatic && (calls != 1 || len(store.layoutFailed) != 0) || !automatic && (calls != 0 || len(store.layoutFailed) != 1) {
			t.Fatal("repair authority/attempt bound", automatic, calls)
		}
	}
}

func TestAzureLayoutRepairBindsEvidenceAndRejectsInjectedContent(t *testing.T) {
	job := LayoutJob{Document: validDocument(4), Canonical: CanonicalMetadata{Title: "週報", IssueNumber: 2000, Date: "2026-11-08"}, Automatic: true}
	issues := []ReviewIssue{{Code: "layout_validation_failed", ComponentID: "body", BlockID: "block-body", Blocking: true}}
	plan := LayoutRepairPlan{Styles: []LayoutStyleDecision{{BlockID: "block-body", FontSize: 14, LineHeight: 20}}}
	client, calls := updateServer(t, []any{plan}, nil)
	evidence, err := client.PlanLayoutRepair(context.Background(), job, issues)
	if err != nil || *calls != 1 {
		t.Fatal(err)
	}
	if _, err = ApplyLayoutRepairEvidence(job, issues, evidence); err != nil {
		t.Fatal("own evidence rejected", err)
	}
	job.Canonical.Title = "metadata changed"
	if _, err = ApplyLayoutRepairEvidence(job, issues, evidence); err == nil {
		t.Fatal("repair survived metadata change")
	}
	client, _ = updateServer(t, []any{map[string]any{"styles": []any{}, "text": "model-authored words"}}, nil)
	if _, err = client.PlanLayoutRepair(context.Background(), job, issues); err == nil {
		t.Fatal("model content injection accepted")
	}
}
