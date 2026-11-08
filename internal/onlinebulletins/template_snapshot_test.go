package onlinebulletins

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func snapshotDocument(t *testing.T, version string, snapshot any) (Document, []byte) {
	t.Helper()
	d := validDocument(4)
	d.LayoutManifest.RendererVersion = version
	raw, _ := json.Marshal(d)
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if snapshot != nil {
		fields["templateSnapshot"] = snapshot
	}
	raw, _ = json.Marshal(fields)
	// Malformed fixtures must reach the document decoder under test.
	type fixtureDocument Document
	var fixture fixtureDocument
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return Document(fixture), raw
}

func snapshotFields(version int64) map[string]any {
	return map[string]any{"version": version, "visionMission": "遍地華人興起、福音傳到地極", "visionFellowship": "共同生活、愛與成全、恩膏傳承", "visionCommitment": "宣教主導、靈恩神學、團隊事奉、門徒訓練"}
}

func TestDocumentTemplateSnapshotValidation(t *testing.T) {
	_, raw := snapshotDocument(t, "v9", snapshotFields(3))
	if _, err := DecodeDocument(strings.NewReader(string(raw))); err != nil {
		t.Fatalf("valid frozen snapshot rejected: %v", err)
	}
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"missing", nil},
		{"zero version", snapshotFields(0)}, {"unsafe version", snapshotFields(9007199254740992)},
		{"empty", func() any { v := snapshotFields(1); v["visionMission"] = ""; return v }()},
		{"untrimmed", func() any { v := snapshotFields(1); v["visionMission"] = " 經文"; return v }()},
		{"control", func() any { v := snapshotFields(1); v["visionMission"] = "經\n文"; return v }()},
		{"unknown", func() any { v := snapshotFields(1); v["title"] = "不可編輯"; return v }()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, raw := snapshotDocument(t, "v9", tc.value)
			if _, err := DecodeDocument(strings.NewReader(string(raw))); err == nil {
				t.Fatal("invalid snapshot accepted")
			}
		})
	}
	_, raw = snapshotDocument(t, "v8", nil)
	if _, err := DecodeDocument(strings.NewReader(string(raw))); err != nil {
		t.Fatal("legacy revision rejected", err)
	}
	_, raw = snapshotDocument(t, "v8", snapshotFields(1))
	if _, err := DecodeDocument(strings.NewReader(string(raw))); err == nil {
		t.Fatal("legacy renderer silently ignored snapshot")
	}
}

func TestDocumentRejectsLiteralNullTemplateSnapshot(t *testing.T) {
	for _, version := range []string{"v8", "v9", "v10"} {
		t.Run(version, func(t *testing.T) {
			_, raw := snapshotDocument(t, version, nil)
			raw = append([]byte(`{"templateSnapshot":null,`), raw[1:]...)
			if _, err := DecodeDocument(strings.NewReader(string(raw))); err == nil {
				t.Fatal("literal null snapshot accepted at document boundary")
			}
			var document Document
			if err := json.Unmarshal(raw, &document); err == nil {
				t.Fatal("nested document decoder ignored literal null snapshot")
			}
		})
	}
}

func TestSimplifiedConvertsTemplateSnapshotWithoutMutatingSource(t *testing.T) {
	source, _ := snapshotDocument(t, "v9", snapshotFields(7))
	before, _ := json.Marshal(source)
	got, err := simplifiedRunner(t).ConvertSimplified(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	want := map[string]any{"version": float64(7), "visionMission": "遍地华人兴起、福音传到地极", "visionFellowship": "共同生活、爱与成全、恩膏传承", "visionCommitment": "宣教主导、灵恩神学、团队事奉、门徒训练"}
	if !reflect.DeepEqual(fields["templateSnapshot"], want) {
		t.Fatalf("unconverted frozen settings: %v", fields["templateSnapshot"])
	}
	after, _ := json.Marshal(source)
	if string(before) != string(after) {
		t.Fatal("source revision mutated")
	}
}

func TestMergeAdoptsIncomingTemplateSnapshotPreservingLocalText(t *testing.T) {
	base, _ := snapshotDocument(t, "v8", nil)
	local, _ := snapshotDocument(t, "v8", nil)
	local.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text = "手改。"
	incoming, _ := snapshotDocument(t, "v9", snapshotFields(5))
	got, err := MergeDocuments(base, local, incoming, nil)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(got)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	if fields["templateSnapshot"] == nil || fields["templateSnapshot"].(map[string]any)["version"] != float64(5) || got.Components[1].BodySection.Blocks[0].Sentences[0].Spans[0].Text != "手改。" {
		t.Fatal("lost incoming snapshot or authored text")
	}
	if _, err := ValidateDocument(local); err != nil {
		t.Fatal("prior Local changed")
	}
}

func TestTemplateSnapshotSurvivesEditingAndCannotBeComposedAway(t *testing.T) {
	source, _ := snapshotDocument(t, "v9", snapshotFields(9))
	_, metadata, _ := layoutRunnerFixture(t)
	submission, _ := json.Marshal(layoutSubmission{source, metadata})
	if _, err := verifyLayoutResult(layoutRunnerOutput(t, source, submission, nil), source, metadata, true); err != nil {
		t.Fatal("snapshot composition rejected", err)
	}
	for _, mode := range []string{"changed", "deleted"} {
		changed, _ := snapshotDocument(t, "v9", snapshotFields(10))
		if mode == "deleted" {
			changed.TemplateSnapshot = nil
		}
		submission, _ := json.Marshal(layoutSubmission{changed, metadata})
		if _, err := verifyLayoutResult(layoutRunnerOutput(t, changed, submission, nil), source, metadata, true); err == nil {
			t.Fatal("composer changed frozen input", mode)
		}
	}
	edited, err := EditComponents(source, source.Components)
	if err != nil || edited.TemplateSnapshot == nil || *edited.TemplateSnapshot != *source.TemplateSnapshot {
		t.Fatal("draft edit lost snapshot", err)
	}
}

func TestMemberProjectionRetainsTemplateSnapshotButNotPrivateSource(t *testing.T) {
	source, _ := snapshotDocument(t, "v9", snapshotFields(9))
	content, err := ProjectMemberContent(source)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(content)
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	if fields["templateSnapshot"] == nil {
		t.Fatal("member/offline reader lost frozen values")
	}
	for _, private := range []string{"sourceAssetChecksum", "sourcePageCount", "createdBy", "createdAt"} {
		if fields[private] != nil {
			t.Fatal("private field exposed", private)
		}
	}
}
