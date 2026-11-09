package onlinebulletins

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func updateFixture(t *testing.T) UpdateInput {
	t.Helper()
	d := validDocument(4)
	return UpdateInput{Canonical: CanonicalIssue{IssueID: d.IssueID, Series: d.Series, ContentLocale: d.ContentLocale, Metadata: CanonicalMetadata{Title: "週報", IssueNumber: 2000, Date: "2026-11-08"}}, Incoming: d, Evidence: GeometryDocument{SourceSHA256: d.SourceAssetChecksum, PopplerVersion: PopplerVersion, Pages: []GeometryPage{{Number: 1, Texts: []GeometryText{{Text: "2000期 2026-11-08 週報"}}}}}}
}

func readyUpdatePlan() UpdatePlan {
	return UpdatePlan{MetadataChoice: "source", Readiness: "ready", ChangeKind: "first", Summary: "已核對來源與保留內容。", Choices: []UpdateChoice{}, Mappings: []SemanticMappingDecision{}}
}

func updateServer(t *testing.T, plans []any, inspect func(map[string]any)) (*AzureExtractor, *int) {
	t.Helper()
	calls := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if r.URL.Path != "/openai/v1/responses" || r.Header.Get("api-key") != "test-key" || json.NewDecoder(r.Body).Decode(&payload) != nil {
			t.Error("invalid Azure contract")
		}
		if inspect != nil {
			inspect(payload)
		}
		index := calls
		calls++
		if index >= len(plans) {
			t.Error("unbounded inference")
			http.Error(w, "failed", 500)
			return
		}
		if plans[index] == nil {
			http.Error(w, "failed", 503)
			return
		}
		json.NewEncoder(w).Encode(recognitionResponse(plans[index]))
	}))
	t.Cleanup(server.Close)
	client, err := NewAzureExtractor(server.URL, "candidate", "test-key", "", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	return client, &calls
}

func TestVerifiedUpdatePlanUsesIndependentSourceAndRecordsBeforeSpending(t *testing.T) {
	input := updateFixture(t)
	input.Evidence.Pages[0].Glyphs = []GeometryGlyph{{Text: "unused-glyph-catalog-marker"}}
	plan := readyUpdatePlan()
	saved := 0
	client, calls := updateServer(t, []any{plan, plan}, func(payload map[string]any) {
		data, _ := json.Marshal(payload)
		if !strings.Contains(string(data), "2000期") || !strings.Contains(string(data), "正文。") || strings.Contains(string(data), "previousProposal") || strings.Contains(string(data), "/assets/weekly/") {
			t.Error("source projection/independence")
		}
		if strings.Contains(string(data), "unused-glyph-catalog-marker") {
			t.Error("sent the rendering glyph catalog instead of compact source evidence")
		}
		if payload["store"] != false || payload["background"] != false {
			t.Error("provider persistence")
		}
		metadataEnum := payload["text"].(map[string]any)["format"].(map[string]any)["schema"].(map[string]any)["properties"].(map[string]any)["metadataChoice"].(map[string]any)["enum"].([]any)
		if len(metadataEnum) != 1 || metadataEnum[0] != "source" {
			t.Error("offered an unavailable metadata action to the model")
		}
	})
	result, err := client.PlanVerifiedUpdate(context.Background(), input, func(attempt UpdateAttempt) error {
		if saved+1 != *calls || attempt.InputSHA256 == "" || attempt.Usage.Model == "" {
			t.Error("missing ordered evidence")
		}
		saved++
		return nil
	})
	if err != nil || *calls != 2 || saved != 2 || result.Plan.Readiness != "ready" || !ValidUpdateEvidence(input, result) {
		t.Fatalf("verified result: %v, calls %d", err, *calls)
	}
	changed := input
	changed.Canonical.Metadata.Title = "另一個版本"
	if ValidUpdateEvidence(changed, result) {
		t.Fatal("receipt survived metadata change")
	}
}

func TestVerifiedUpdatePlanUsesOriginalCoverForOutlinedSourceIdentity(t *testing.T) {
	input := updateFixture(t)
	input.SourceCoverPNG = recognitionFixture(t).PNG
	client, calls := updateServer(t, []any{readyUpdatePlan(), readyUpdatePlan()}, func(payload map[string]any) {
		raw, _ := json.Marshal(payload)
		if !strings.Contains(string(raw), "input_image") || !strings.Contains(string(raw), "data:image/png;base64,") {
			t.Error("outlined source identity lost its original image")
		}
	})
	evidence, err := client.PlanVerifiedUpdate(context.Background(), input, nil)
	if err != nil || *calls != 2 {
		t.Fatal(err)
	}
	input.SourceCoverPNG = []byte("not a png")
	if ValidUpdateEvidence(input, evidence) {
		t.Fatal("cover replacement preserved receipt")
	}
	client, calls = updateServer(t, nil, nil)
	if _, err = client.PlanVerifiedUpdate(context.Background(), input, nil); err == nil || *calls != 0 {
		t.Fatal("unbounded/invalid image reached provider")
	}
}

func TestVerifiedUpdatePlanRejectsUnsafeSourceAndProviderFailure(t *testing.T) {
	for _, kind := range []string{"wrong_issue", "wrong_edition", "incomplete", "disagreement", "invented_choice", "injected_action", "provider"} {
		t.Run(kind, func(t *testing.T) {
			input := updateFixture(t)
			first, second := readyUpdatePlan(), readyUpdatePlan()
			switch kind {
			case "wrong_issue", "wrong_edition", "incomplete":
				first.Readiness, second.Readiness = kind, kind
			case "disagreement":
				second.Readiness = "incomplete"
			case "invented_choice":
				first.Choices = []UpdateChoice{{ComponentID: "foreign", Choice: "local"}}
			}
			var a any = first
			if kind == "provider" {
				a = nil
			}
			if kind == "injected_action" {
				a = map[string]any{"readiness": "ready", "changeKind": "first", "summary": "ok", "choices": []any{}, "mappings": []any{}, "publish": true}
			}
			client, calls := updateServer(t, []any{a, second}, nil)
			result, err := client.PlanVerifiedUpdate(context.Background(), input, nil)
			if err == nil || ValidUpdateEvidence(input, result) || *calls > 2 {
				t.Fatalf("unsafe source accepted: %v, calls %d", err, *calls)
			}
		})
	}
}

func TestVerifiedUpdatePlanInvalidAnnotationProposalCannotBlockVerifiedSource(t *testing.T) {
	input := updateFixture(t)
	previous := validDocument(4)
	input.Published = &previous
	semanticMappingRename(&input.Incoming, "sentence-speaker", "fresh-speaker", "講員")
	first, second := readyUpdatePlan(), readyUpdatePlan()
	first.Mappings = []SemanticMappingDecision{{FromSentenceID: "sentence-speaker", Kind: "equivalent", ToSentenceIDs: []string{"invented"}}}
	second.Mappings = first.Mappings
	client, calls := updateServer(t, []any{first, second}, nil)
	evidence, err := client.PlanVerifiedUpdate(context.Background(), input, nil)
	if err != nil || *calls != 2 || !ValidUpdateEvidence(input, evidence) {
		t.Fatal("annotation-only defect blocked verified source", err)
	}
	_, mappings, err := ApplyUpdatePlan(input, evidence.Plan)
	if err != nil || semanticMappingFor(t, mappings, "sentence-speaker").Status != "removed" {
		t.Fatal("unsafe mapping did not retain history", err)
	}
	if len(evidence.Attempts[0].Plan.Mappings) != 1 {
		t.Fatal("lost rejected proposal evidence")
	}
}

func TestVerifiedUpdatePlanStopsWhenEvidenceCannotPersist(t *testing.T) {
	input := updateFixture(t)
	client, calls := updateServer(t, []any{readyUpdatePlan(), readyUpdatePlan()}, nil)
	storage := errors.New("lease lost")
	_, err := client.PlanVerifiedUpdate(context.Background(), input, func(UpdateAttempt) error { return storage })
	if !errors.Is(err, storage) || *calls != 1 {
		t.Fatal("spent after evidence/lease failure", err, *calls)
	}
}

func TestVerifiedUpdatePlanResumesOnlyExactSuccessfulIndependentPass(t *testing.T) {
	input := updateFixture(t)
	client, _ := updateServer(t, []any{readyUpdatePlan(), readyUpdatePlan()}, nil)
	prior, err := client.PlanVerifiedUpdate(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	client, calls := updateServer(t, []any{readyUpdatePlan()}, nil)
	got, err := client.ResumeVerifiedUpdate(context.Background(), input, prior.Attempts[:1], nil)
	if err != nil || *calls != 1 || !ValidUpdateEvidence(input, got) {
		t.Fatal("repeated paid verified work", err, *calls)
	}
	prior.Attempts[0].InputSHA256 = strings.Repeat("b", 64)
	client, calls = updateServer(t, nil, nil)
	if _, err = client.ResumeVerifiedUpdate(context.Background(), input, prior.Attempts[:1], nil); err == nil || *calls != 0 {
		t.Fatal("reused a different input", err)
	}
}

func TestVerifiedUpdatePlanAdoptsPDFAndRetainsOnlyAgreedCompatibleAuthoredChanges(t *testing.T) {
	input := updateFixture(t)
	base, local := validDocument(4), validDocument(4)
	semanticMappingRename(&local, "sentence-body", "sentence-body", "修正版")
	input.Base, input.Local, input.Published = &base, &local, &base
	first, second := readyUpdatePlan(), readyUpdatePlan()
	first.ChangeKind, second.ChangeKind = "correction", "correction"
	first.Choices = []UpdateChoice{{ComponentID: "body", BlockID: "block-body", Choice: "local"}}
	second.Choices = first.Choices
	client, _ := updateServer(t, []any{first, second}, nil)
	result, err := client.PlanVerifiedUpdate(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := ApplyUpdatePlan(input, result.Plan)
	if err != nil || got.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text != "修正版" {
		t.Fatal("lost compatible authored correction", err)
	}
	second.Choices = []UpdateChoice{{ComponentID: "body", BlockID: "block-body", Choice: "incoming"}}
	client, _ = updateServer(t, []any{first, second}, nil)
	result, err = client.PlanVerifiedUpdate(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err = ApplyUpdatePlan(input, result.Plan)
	if err != nil || got.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text != "正文。" {
		t.Fatal("disagreement did not prefer valid PDF", err)
	}
}

func TestVerifiedUpdateCannotReplaceNewSourceWithUnmodifiedOldContent(t *testing.T) {
	input := updateFixture(t)
	base, local := validDocument(4), validDocument(4)
	input.Base, input.Local = &base, &local
	input.Incoming.SourceAssetChecksum = strings.Repeat("d", 64)
	input.Evidence.SourceSHA256 = input.Incoming.SourceAssetChecksum
	semanticMappingRename(&input.Incoming, "sentence-body-title", "sentence-body-title", "新題")
	proposal := readyUpdatePlan()
	proposal.ChangeKind = "correction"
	proposal.Choices = []UpdateChoice{{ComponentID: "body", BlockID: "block-body-title", Choice: "local"}}
	client, _ := updateServer(t, []any{proposal, proposal}, nil)
	evidence, err := client.PlanVerifiedUpdate(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := ApplyUpdatePlan(input, evidence.Plan)
	if err != nil || got.Components[1].BodySection.Title.Sentences[0].Spans[0].Text != "新題" {
		t.Fatal("unmodified old content overrode the newest valid source", err)
	}
}

func TestVerifiedUpdatePlanMappingDisagreementRetainsHistoryWithoutBlockingContent(t *testing.T) {
	input := updateFixture(t)
	previous := validDocument(4)
	input.Published = &previous
	semanticMappingRename(&input.Incoming, "sentence-speaker", "fresh-speaker", "講員")
	first, second := readyUpdatePlan(), readyUpdatePlan()
	first.Mappings = []SemanticMappingDecision{{FromSentenceID: "sentence-speaker", Kind: "equivalent", ToSentenceIDs: []string{"fresh-speaker"}}}
	second.Mappings = []SemanticMappingDecision{{FromSentenceID: "sentence-speaker", Kind: "historical", ToSentenceIDs: []string{}}}
	client, _ := updateServer(t, []any{first, second}, nil)
	result, err := client.PlanVerifiedUpdate(context.Background(), input, nil)
	if err != nil {
		t.Fatal(err)
	}
	got, mappings, err := ApplyUpdatePlan(input, result.Plan)
	if err != nil || got.Components[1].BodySection.Contributors[0].Name.Sentences[0].ID != "fresh-speaker" || semanticMappingFor(t, mappings, "sentence-speaker").Status != "removed" {
		t.Fatal("mapping uncertainty blocked content or guessed identity", err)
	}
}

func TestVerifiedUpdateProjectionOmitsTypographyAndReferencesIdenticalSnapshots(t *testing.T) {
	input := updateFixture(t)
	base, local := input.Incoming, input.Incoming
	input.Base, input.Local, input.Published = &base, &local, &base
	client, _ := updateServer(t, []any{readyUpdatePlan(), readyUpdatePlan()}, func(payload map[string]any) {
		content := payload["input"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
		var source map[string]any
		if json.Unmarshal([]byte(strings.TrimPrefix(content, "SOURCE DATA:\n")), &source) != nil {
			t.Fatal("invalid source projection")
		}
		raw, _ := json.Marshal(source)
		if strings.Contains(string(raw), "fontRole") || strings.Contains(string(raw), "lineHeight") {
			t.Error("paid for renderer typography")
		}
		if source["Base"] != "Incoming" || source["Local"] != "Incoming" || source["Published"] != "Incoming" {
			t.Error("duplicated identical semantic snapshots")
		}
		if !strings.Contains(string(raw), "sentence-speaker") || !strings.Contains(string(raw), "正文。") {
			t.Error("lost semantic IDs or content")
		}
	})
	if _, err := client.PlanVerifiedUpdate(context.Background(), input, nil); err != nil {
		t.Fatal(err)
	}
}
