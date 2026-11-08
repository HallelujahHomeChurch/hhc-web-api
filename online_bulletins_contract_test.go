package hhcwebapi

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/getkin/kin-openapi/openapi3"
)

func TestBulletinTemplateSettingsContract(t *testing.T) {
	doc, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	base := "/admin/bulletins/template-settings"
	path := doc.Paths.Find(base)
	if path == nil || path.Get == nil || path.Put == nil || doc.Paths.Find(base+"/revisions") == nil {
		t.Fatal("missing template settings paths")
	}
	for _, entry := range []struct {
		op    *openapi3.Operation
		scope string
	}{{path.Get, "cms:bulletins:read"}, {path.Put, "cms:bulletins:write"}, {doc.Paths.Find(base + "/revisions").Get, "cms:bulletins:read"}} {
		if entry.op == nil || !reflect.DeepEqual(entry.op.Extensions["x-required-scopes"], []any{entry.scope}) || entry.op.Extensions["x-hhc-visibility"] != "admin" || entry.op.Extensions["x-hhc-cache"] != "private, no-store" || entry.op.Security == nil || len(*entry.op.Security) == 0 {
			t.Fatal("missing private scope/security contract", entry.scope)
		}
	}
	if !path.Put.Deprecated || path.Put.Responses.Value("410") == nil || len(path.Put.Parameters) != 1 || path.Put.Parameters[0].Ref != "#/components/parameters/IfMatch" {
		t.Fatal("missing retirement and legacy client compatibility contract")
	}
	settings := onlinebulletins.TemplateSettings{Version: 1, TemplateText: onlinebulletins.TemplateText{VisionMission: "遍地華人興起、福音傳到地極", VisionFellowship: "共同生活、愛與成全、恩膏傳承", VisionCommitment: "宣教主導、靈恩神學、團隊事奉、門徒訓練"}}
	for name, value := range map[string]any{
		"BulletinTemplateText":             settings.TemplateText,
		"BulletinTemplateSettings":         settings,
		"BulletinTemplateSettingsRevision": onlinebulletins.TemplateSettingsRevision{TemplateSettings: settings, CreatedBy: "migration", CreatedAt: "2026-10-10T00:00:00Z"},
	} {
		schema := compileOpenAPISchema(t, name)
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(object); err != nil {
			t.Fatal(name, err)
		}
		object["sourceAssetId"] = "private"
		if schema.Validate(object) == nil {
			t.Fatal(name, "accepted unknown/private field")
		}
		delete(object, "sourceAssetId")
		object["visionMission"] = ""
		if schema.Validate(object) == nil {
			t.Fatal(name, "accepted empty settings")
		}
	}
}

func TestBulletinUploadAutomaticExtractionContract(t *testing.T) {
	doc, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	op := doc.Paths.Find("/admin/bulletins/{issueId}/assets/{assetId}/complete").Post
	for _, promise := range []string{"BULLETIN_AUTO_EXTRACTION_ENABLED", "issue 1739", "general zh-Hant", "clean scan", "first and replacement uploads", "does not publish"} {
		if !strings.Contains(op.Description, promise) {
			t.Fatalf("upload contract missing %q", promise)
		}
	}
	if op.Responses.Value("200").Ref != "#/components/responses/BulletinIssue" {
		t.Fatal("upload response changed")
	}
}

func TestSimplifiedReadOnlyMutationContract(t *testing.T) {
	doc, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	base := "/admin/bulletins/{issueId}/online/{series}/{contentLocale}"
	for _, suffix := range []string{"/draft", "/compare", "/revisions/{revision}/restore"} {
		path := doc.Paths.Find(base + suffix)
		operation := path.Post
		if suffix == "/draft" {
			operation = path.Put
		}
		if !strings.Contains(operation.Description, "Simplified manual mutations return 409") || operation.Responses.Value("409") == nil {
			t.Fatalf("missing derived-content contract: %s", suffix)
		}
	}
	if !strings.Contains(doc.Paths.Find(base+"/conversions").Post.Description, "replaces the Simplified Local and Base") {
		t.Fatal("conversion contract still requires manual merging")
	}
}

func TestOnlineAdminStateSchemaRejectsPrivateWorkerFields(t *testing.T) {
	schema := compileOpenAPISchema(t, "OnlineBulletinAdminState")
	state := onlinebulletins.AdminState{IssueID: "018f47d2-e5d1-4f3f-8f18-6c8e32621f73", Series: "general", ContentLocale: "zh-Hant", CanonicalVersion: 1, CanonicalMetadata: onlinebulletins.CanonicalMetadata{Title: "標題", IssueNumber: 1740, Date: "2026-09-27"}, PDFStatus: "draft", DraftStatus: "notStarted"}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if err = schema.Validate(value); err != nil {
		t.Fatal(err)
	}
	value["extraction"] = map[string]any{"id": state.IssueID, "status": "failed", "attempts": 3, "errorCode": "attempt_limit"}
	if err = schema.Validate(value); err != nil {
		t.Fatal(err)
	}
	value["extraction"].(map[string]any)["leaseToken"] = "private"
	if schema.Validate(value) == nil {
		t.Fatal("private worker field accepted")
	}
	value["extraction"] = nil
	issue := map[string]any{"code": "layout_validation_failed", "componentId": "body", "blockId": "heading", "sentenceId": "first", "sourcePage": float64(2), "blocking": true}
	value["layout"] = map[string]any{"id": state.IssueID, "status": "failed", "attempts": float64(3), "errorCode": "layout_validation_failed", "reviewIssues": []any{issue}}
	if err = schema.Validate(value); err != nil {
		t.Fatal("located layout diagnostics rejected", err)
	}
	issue["rawError"] = "private"
	if schema.Validate(value) == nil {
		t.Fatal("private diagnostic text accepted")
	}
}

func TestOnlineBulletinComponentContract(t *testing.T) {
	schema := compileOpenAPISchema(t, "OnlineBulletinComponent")
	block := onlinebulletins.Block{ID: "b", Style: onlinebulletins.ParagraphStyle{FontSize: 16, LineHeight: 24}, Sentences: []onlinebulletins.Sentence{{ID: "s", Spans: []onlinebulletins.Span{{Text: "測試。", FontRole: "body"}}}}}
	items := []onlinebulletins.Item{{ID: "item", Title: &block, Blocks: []onlinebulletins.Block{block}}}
	cases := []onlinebulletins.Component{
		{ID: "cover", Type: "cover", Cover: &onlinebulletins.Cover{Welcome: []onlinebulletins.Block{block}, Worship: items, Work: items, WordQuestions: items, WeeklyVerses: []onlinebulletins.Block{block}}},
		{ID: "body", Type: "bodySection", BodySection: &onlinebulletins.BodySection{Kind: "sermon", Title: block, Blocks: []onlinebulletins.Block{block}}},
		{ID: "hymns", Type: "hymnLyrics", HymnLyrics: &onlinebulletins.HymnLyrics{Hymns: []onlinebulletins.Hymn{{ID: "h", Title: block, Sections: []onlinebulletins.HymnSection{{ID: "v", Kind: "verse", Lines: []onlinebulletins.Block{block}}}}}}},
		{ID: "summary", Type: "backSummary", Items: items},
		{ID: "announcements", Type: "announcements", Items: items},
		{ID: "prayers", Type: "victoriesAndPrayers", Items: items},
		{ID: "empty-announcements", Type: "announcements", Items: []onlinebulletins.Item{}},
		{ID: "empty-prayers", Type: "victoriesAndPrayers", Items: []onlinebulletins.Item{}},
	}
	for _, c := range cases {
		t.Run(c.Type, func(t *testing.T) {
			data, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if err = json.Unmarshal(data, &value); err != nil {
				t.Fatal(err)
			}
			if err = schema.Validate(value); err != nil {
				t.Fatal(err)
			}
			value["sourceAssetId"] = "private"
			if err = schema.Validate(value); err == nil {
				t.Fatal("unknown union field accepted")
			}
		})
	}
	for _, bad := range []any{
		map[string]any{"id": "roster", "type": "serviceRoster", "items": []any{}},
		map[string]any{"id": "body", "type": "bodySection", "bodySection": map[string]any{"kind": "invented", "title": "標題", "blocks": []any{}}},
		map[string]any{"id": "body", "type": "bodySection", "items": []any{}},
	} {
		if err := schema.Validate(bad); err == nil {
			t.Fatal("invalid component accepted")
		}
	}
}

func TestOnlineBulletinInlineFontSizeContract(t *testing.T) {
	schema := compileOpenAPISchema(t, "OnlineBulletinSpan")
	for _, size := range []any{nil, float64(6), float64(12), float64(96)} {
		if err := schema.Validate(map[string]any{"text": "文字", "fontRole": "body", "fontSize": size}); err != nil {
			t.Fatal(err)
		}
	}
	for _, size := range []any{float64(0), float64(5), float64(97), "12pt"} {
		if err := schema.Validate(map[string]any{"text": "文字", "fontRole": "body", "fontSize": size}); err == nil {
			t.Fatalf("invalid size accepted: %v", size)
		}
	}
}
