package hhcwebapi

import "testing"

func TestOnlineTemplateSnapshotContract(t *testing.T) {
	schema := compileOpenAPISchema(t, "OnlineBulletinLayoutManifest")
	input := map[string]any{"templateVersion": "v1", "rendererVersion": "v9", "rendererArtifactSha256": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "assets": []any{map[string]any{"url": "/assets/weekly/v1/cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc.woff2", "sha256": "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", "kind": "font", "fontRole": "body"}}, "pages": []any{map[string]any{"pageId": "a", "slots": []any{}}, map[string]any{"pageId": "b", "slots": []any{}}, map[string]any{"pageId": "c", "slots": []any{}}, map[string]any{"pageId": "d", "slots": []any{}}}}
	if err := schema.Validate(input); err != nil {
		t.Fatal("V9 manifest absent from canonical schema", err)
	}
	pages := []any{}
	for _, id := range []string{"a", "b", "c", "d"} {
		pages = append(pages, map[string]any{"id": id, "width": 595, "height": 842})
	}
	components := []any{}
	for _, id := range []string{"a", "b", "c", "d", "e", "f"} {
		components = append(components, map[string]any{"id": id, "type": "backSummary", "items": []any{}})
	}
	snapshot := map[string]any{"version": float64(3), "visionMission": "遍地華人", "visionFellowship": "共同生活", "visionCommitment": "宣教主導"}
	for _, name := range []string{"OnlineBulletinDocument", "ReaderContent"} {
		t.Run(name, func(t *testing.T) {
			schema := compileOpenAPISchema(t, name)
			document := map[string]any{"schemaVersion": "1", "templateVersion": "v1", "pages": pages, "layoutManifest": input, "components": components, "templateSnapshot": snapshot}
			if name == "OnlineBulletinDocument" {
				document["issueId"] = "b08a80e8-6388-4166-9a2a-23d336f74b7a"
				document["series"] = "general"
				document["contentLocale"] = "zh-Hant"
				document["sourceAssetChecksum"] = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				document["sourcePageCount"] = 4
			} else {
				document["printedBodyPageCount"] = 2
			}
			input["rendererVersion"] = "v9"
			if err := schema.Validate(document); err != nil {
				t.Fatal("valid V9 projection rejected", err)
			}
			delete(document, "templateSnapshot")
			if schema.Validate(document) == nil {
				t.Fatal("V9 missing snapshot accepted")
			}
			input["rendererVersion"] = "v8"
			if err := schema.Validate(document); err != nil {
				t.Fatal("legacy projection rejected", err)
			}
			document["templateSnapshot"] = snapshot
			if schema.Validate(document) == nil {
				t.Fatal("legacy ignored snapshot")
			}
		})
	}
}
