package onlinebulletins

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func simplifiedAssessmentFixture(t *testing.T) (Document, CanonicalMetadata, Document, CanonicalMetadata) {
	t.Helper()
	source := validDocument(4)
	target := semanticMappingCopy(t, source)
	target.ContentLocale, target.TemplateVersion, target.LayoutManifest.TemplateVersion, target.LayoutManifest.RendererVersion = "zh-Hans", "v2", "v2", "v2"
	semanticMappingRename(&target, "sentence-speaker", "sentence-speaker", "讲员")
	return source, CanonicalMetadata{Title: "週報", IssueNumber: 2000, Date: "2026-11-08"}, target, CanonicalMetadata{Title: "周报", IssueNumber: 2000, Date: "2026-11-08"}
}

func TestSimplifiedAssessmentUsesReleasedSourceWithoutRewritingContent(t *testing.T) {
	source, sourceMetadata, target, metadata := simplifiedAssessmentFixture(t)
	input, err := SimplifiedAssessmentInput(source, sourceMetadata, target, metadata)
	if err != nil {
		t.Fatal(err)
	}
	client, calls := updateServer(t, []any{readyUpdatePlan(), readyUpdatePlan()}, func(payload map[string]any) {
		raw, _ := json.Marshal(payload)
		if !strings.Contains(string(raw), "derivedSource") || !strings.Contains(string(raw), "讲员") || !strings.Contains(string(raw), "講員") {
			t.Error("lost immutable source/derived comparison")
		}
	})
	evidence, err := client.PlanVerifiedUpdate(context.Background(), input, nil)
	if err != nil || *calls != 2 || !ValidUpdateEvidence(input, evidence) {
		t.Fatal("derived assessment failed", err)
	}
	semanticMappingRename(&target, "sentence-speaker", "foreign-speaker", "")
	if _, err = SimplifiedAssessmentInput(source, sourceMetadata, target, metadata); err == nil {
		t.Fatal("conversion changed annotation/article identity")
	}
	_, _, target, metadata = simplifiedAssessmentFixture(t)
	metadata.Date = "2026-11-15"
	if _, err = SimplifiedAssessmentInput(source, sourceMetadata, target, metadata); err == nil {
		t.Fatal("conversion crossed source edition")
	}
}

func TestCompletedUpdateEvidenceRequiresBothSuccessfulIndependentPasses(t *testing.T) {
	input := updateFixture(t)
	client, _ := updateServer(t, []any{readyUpdatePlan(), readyUpdatePlan()}, nil)
	verified, err := client.PlanVerifiedUpdate(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	rebuilt, err := CompletedUpdateEvidence(input, verified.Attempts)
	if err != nil || !ValidUpdateEvidence(input, rebuilt) {
		t.Fatal(err)
	}
	if _, err = CompletedUpdateEvidence(input, verified.Attempts[:1]); err == nil {
		t.Fatal("one pass supplied publication proof")
	}
	verified.Attempts[0].ErrorCode = "provider_failed"
	if _, err = CompletedUpdateEvidence(input, verified.Attempts); err == nil {
		t.Fatal("failed assessment supplied publication proof")
	}
}

func TestDerivedMetadataKeepsOnlyAgreedCompatibleAuthoredChoice(t *testing.T) {
	source, sourceMetadata, target, metadata := simplifiedAssessmentFixture(t)
	input, err := SimplifiedAssessmentInput(source, sourceMetadata, target, metadata)
	if err != nil {
		t.Fatal(err)
	}
	local := metadata
	local.Title = "周报修正标题"
	input.LocalMetadata = &local
	first, second := readyUpdatePlan(), readyUpdatePlan()
	first.MetadataChoice, second.MetadataChoice = "local", "local"
	client, _ := updateServer(t, []any{first, second}, nil)
	evidence, err := client.PlanVerifiedUpdate(context.Background(), input, nil)
	if err != nil || evidence.Plan.MetadataChoice != "local" {
		t.Fatal("lost agreed authored metadata", err)
	}
	second.MetadataChoice = "source"
	client, _ = updateServer(t, []any{first, second}, nil)
	evidence, err = client.PlanVerifiedUpdate(context.Background(), input, nil)
	if err != nil || evidence.Plan.MetadataChoice != "source" {
		t.Fatal("metadata conflict did not prefer valid source", err)
	}
}
