package content

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseStatementDocumentAndPlainText(t *testing.T) {
	raw := json.RawMessage(`{"schemaVersion":1,"blocks":[{"id":"p1","type":"paragraph","alignment":"center","content":[{"type":"text","text":"重要","marks":["strong"]},{"type":"text","text":"聲明"}]},{"id":"l1","type":"list","ordered":true,"items":[{"content":[{"type":"text","text":"第一點"}]}]},{"id":"i1","type":"image","assetId":"asset_1","size":"medium","alignment":"center","alt":{"mode":"text","text":"聚會現場"},"caption":[{"type":"text","text":"圖片說明"}]}]}`)
	document, err := ParseStatementDocument(raw, true)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := StatementDocumentText(document), "重要聲明\n\n1. 第一點\n\n圖片說明"; got != want {
		t.Fatalf("text=%q want=%q", got, want)
	}
}

func TestParseStatementDocumentRejectsUnsafeOrUnsupportedInput(t *testing.T) {
	valid := `{"schemaVersion":1,"blocks":[{"id":"p1","type":"paragraph","content":[{"type":"text","text":"Body"}]}]}`
	tests := map[string]string{
		"unknown field":     strings.Replace(valid, `"blocks":`, `"html":"<b>x</b>","blocks":`, 1),
		"unknown block":     strings.Replace(valid, `"paragraph"`, `"table"`, 1),
		"wrong block field": strings.Replace(valid, `"content":`, `"ordered":false,"content":`, 1),
		"duplicate id":      strings.Replace(valid, `]}`, `,{"id":"p1","type":"paragraph","content":[]}]}`, 1),
		"unsafe link":       `{"schemaVersion":1,"blocks":[{"id":"p1","type":"paragraph","content":[{"type":"link","href":"javascript:alert(1)","content":[{"type":"text","text":"bad"}]}]}]}`,
		"admin link":        `{"schemaVersion":1,"blocks":[{"id":"p1","type":"paragraph","content":[{"type":"link","href":"/admin/users","content":[{"type":"text","text":"bad"}]}]}]}`,
		"unknown mark":      strings.Replace(valid, `"text":"Body"`, `"text":"Body","marks":["underline"]`, 1),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseStatementDocument(json.RawMessage(raw), false); err == nil {
				t.Fatal("expected invalid document")
			}
		})
	}
}

func TestParseStatementDocumentAllowsLinksInHeadings(t *testing.T) {
	raw := json.RawMessage(`{"schemaVersion":1,"blocks":[{"id":"h1","type":"heading","level":2,"content":[{"type":"link","href":"/zh-Hant/about","content":[{"type":"text","text":"About"}]}]}]}`)
	if _, err := ParseStatementDocument(raw, true); err != nil {
		t.Fatal(err)
	}
}

func TestStatementTextColorsAreOptionalAndRestricted(t *testing.T) {
	valid := `{"schemaVersion":1,"blocks":[{"id":"p1","type":"paragraph","content":[{"type":"text","text":"Old"},{"type":"link","href":"/zh-Hant/about","content":[{"type":"text","text":"Color","color":"#A1B2C3","highlight":"#FFF000"}]}]}]}`
	if _, err := ParseStatementDocument(json.RawMessage(valid), true); err != nil {
		t.Fatalf("valid colors: %v", err)
	}
	for _, replacement := range []string{`"color":"red"`, `"color":"#a1b2c3"`, `"color":"#A1B2C3DD"`, `"color":""`, `"highlight":"var(--x)"`} {
		raw := strings.Replace(valid, `"color":"#A1B2C3"`, replacement, 1)
		if strings.HasPrefix(replacement, `"highlight"`) {
			raw = strings.Replace(valid, `"highlight":"#FFF000"`, replacement, 1)
		}
		if _, err := ParseStatementDocument(json.RawMessage(raw), false); err == nil {
			t.Fatalf("accepted %s", replacement)
		}
	}
	for _, node := range []string{`{"type":"lineBreak","color":"#A1B2C3"}`, `{"type":"link","href":"/zh-Hant/about","color":"#A1B2C3","content":[{"type":"text","text":"Link"}]}`} {
		raw := `{"schemaVersion":1,"blocks":[{"id":"p1","type":"paragraph","content":[` + node + `]}]}`
		if _, err := ParseStatementDocument(json.RawMessage(raw), false); err == nil {
			t.Fatalf("accepted color on %s", node)
		}
	}
}

func TestParseStatementDocumentPublishAllowsEmptyParagraphAndImageAlt(t *testing.T) {
	raw := json.RawMessage(`{"schemaVersion":1,"blocks":[{"id":"p1","type":"paragraph","content":[{"type":"text","text":"Body"}]},{"id":"p2","type":"paragraph"},{"id":"i1","type":"image","assetId":"asset_1","alt":{"mode":"text"}},{"id":"p3","type":"paragraph"}]}`)
	if _, err := ParseStatementDocument(raw, false); err != nil {
		t.Fatalf("draft: %v", err)
	}
	if _, err := ParseStatementDocument(raw, true); err != nil {
		t.Fatalf("publish: %v", err)
	}
}

func TestParseStatementDocumentCapsImages(t *testing.T) {
	blocks := make([]string, 11)
	for index := range blocks {
		blocks[index] = `{"id":"i` + string(rune('a'+index)) + `","type":"image","assetId":"asset_` + string(rune('a'+index)) + `","alt":{"mode":"decorative"}}`
	}
	raw := json.RawMessage(`{"schemaVersion":1,"blocks":[` + strings.Join(blocks, ",") + `]}`)
	if _, err := ParseStatementDocument(raw, false); err == nil {
		t.Fatal("accepted more than ten images")
	}
}

func TestNormalizeStatementDerivesBodyFromDocument(t *testing.T) {
	input := WriteInput{
		Kind:        KindStatement,
		Slug:        "statement",
		DisplayDate: "2026-09-20",
		Translations: []Translation{{
			Locale:   "zh-Hant",
			Title:    "聲明",
			Body:     "client supplied text is ignored",
			BodyJSON: json.RawMessage(`{"schemaVersion":1,"blocks":[{"id":"p1","type":"paragraph","content":[{"type":"text","text":"後端產生內文"}]}]}`),
		}},
	}
	normalized := normalize(ModuleNews, input)
	if got := normalized.Translations[0].Body; got != "後端產生內文" {
		t.Fatalf("body=%q", got)
	}
	if !valid(ModuleNews, normalized) {
		t.Fatal("normalized Statement was rejected")
	}
}

func TestBuildPublicStatementDocumentReplacesAssetIDWithURL(t *testing.T) {
	raw := json.RawMessage(`{"schemaVersion":1,"blocks":[{"id":"p1","type":"paragraph","content":[{"type":"text","text":"Color","color":"#123ABC","highlight":"#FFF000"}]},{"id":"i1","type":"image","assetId":"asset_1","alt":{"mode":"decorative"}}]}`)
	public, err := BuildPublicStatementDocument(raw, map[string]string{"asset_1": "/api/assets/public/asset_1"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(public), "assetId") || !strings.Contains(string(public), `"url":"/api/assets/public/asset_1"`) || !strings.Contains(string(public), `"color":"#123ABC"`) || !strings.Contains(string(public), `"highlight":"#FFF000"`) {
		t.Fatalf("public document=%s", public)
	}
}

func TestStatementTranslationChangesTextOnly(t *testing.T) {
	raw := json.RawMessage(`{"schemaVersion":1,"blocks":[{"id":"p1","type":"paragraph","alignment":"center","content":[{"type":"text","text":"來源","marks":["strong"],"color":"#123ABC"},{"type":"link","href":"/zh-Hant/about","content":[{"type":"text","text":"連結","highlight":"#FFF000"}]}]},{"id":"i1","type":"image","assetId":"asset_1","size":"small","alignment":"end","alt":{"mode":"text","text":"圖片"}}]}`)
	fields, err := StatementTranslationFields("標題", raw)
	if err != nil {
		t.Fatal(err)
	}
	for key := range fields {
		fields[key] = "translated-" + key
	}
	translated, err := ApplyStatementTranslation(raw, fields)
	if err != nil {
		t.Fatal(err)
	}
	text := string(translated)
	for _, preserved := range []string{`"id":"p1"`, `"alignment":"center"`, `"marks":["strong"]`, `"color":"#123ABC"`, `"highlight":"#FFF000"`, `"href":"/zh-Hant/about"`, `"assetId":"asset_1"`, `"size":"small"`, `"alignment":"end"`} {
		if !strings.Contains(text, preserved) {
			t.Fatalf("missing %s in %s", preserved, text)
		}
	}
}

func TestStatementSourceHashTracksTextNotLayout(t *testing.T) {
	left := json.RawMessage(`{"schemaVersion":1,"blocks":[{"id":"copy","type":"paragraph","alignment":"start","content":[{"type":"text","text":"原文"}]}]}`)
	center := json.RawMessage(`{"schemaVersion":1,"blocks":[{"id":"copy","type":"paragraph","alignment":"center","content":[{"type":"text","text":"原文"}]}]}`)
	changed := json.RawMessage(`{"schemaVersion":1,"blocks":[{"id":"copy","type":"paragraph","alignment":"start","content":[{"type":"text","text":"更新"}]}]}`)
	first, err := StatementSourceHash("聲明", left)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := StatementSourceHash("聲明", center)
	third, _ := StatementSourceHash("聲明", changed)
	if first != second || first == third || len(first) != 64 {
		t.Fatalf("first=%q second=%q third=%q", first, second, third)
	}
	item := prepareStatementTranslations(Item{Kind: KindStatement, Translations: []Translation{
		{Locale: "zh-Hant", Title: "聲明", Body: "原文", BodyJSON: left},
		{Locale: "en", Title: "Statement", Body: "Text", BodyJSON: left, TranslatedFromSourceHash: first},
		{Locale: "ja", Title: "声明", Body: "本文", BodyJSON: left, TranslatedFromSourceHash: strings.Repeat("0", 64)},
	}})
	if item.Translations[1].TranslationSourceStatus != "current" || item.Translations[2].TranslationSourceStatus != "stale" {
		t.Fatalf("translations=%#v", item.Translations)
	}
}

func TestLegacyStatementDocumentIsCanonical(t *testing.T) {
	document, err := ParseStatementDocument(LegacyStatementDocument("第一行\n第二行"), true)
	if err != nil || StatementDocumentText(document) != "第一行\n第二行" || document.Blocks[0].ID != "legacy-body" {
		t.Fatalf("document=%#v err=%v", document, err)
	}
}
