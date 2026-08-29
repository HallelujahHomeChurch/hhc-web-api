package content

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestValidatePagePayloadAcceptsFixedTemplates(t *testing.T) {
	tests := []struct {
		key     string
		payload json.RawMessage
	}{
		{"home", validHomePagePayload()},
		{"about", validAboutPagePayload()},
		{"privacy-policy", validLegalPagePayload("隱私權", "2026年8月10日")},
		{"terms-of-use", validLegalPagePayload("Terms of Use", "August 4, 2026")},
	}
	for _, test := range tests {
		t.Run(test.key, func(t *testing.T) {
			if err := ValidatePagePayload(test.key, test.payload); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestValidatePagePayloadAcceptsHomeV2ExactLocalizedFields(t *testing.T) {
	if err := ValidatePagePayload("home", validHomeV2PagePayload()); err != nil {
		t.Fatal(err)
	}
	if title, summary, err := PagePayloadMetadata("home", validHomeV2PagePayload()); err != nil || title != "愛從家開始" || summary != "在愛中成長" {
		t.Fatalf("title=%q summary=%q err=%v", title, summary, err)
	}
}

func TestValidatePagePayloadRejectsHomeV2RemovedOrMissingFields(t *testing.T) {
	for _, payload := range []json.RawMessage{
		json.RawMessage(`{"schemaVersion":2,"template":"home.v2","data":{"heroTitle":"Home","heroSubtitle":"Welcome","kingdomJoyDescription":"Joy","aboutDescription":"About","newsTitle":"News"}}`),
		json.RawMessage(`{"schemaVersion":2,"template":"home.v2","data":{"heroTitle":"Home","heroSubtitle":"Welcome","kingdomJoyDescription":"Joy"}}`),
		json.RawMessage(`{"schemaVersion":1,"template":"home.v2","data":{"heroTitle":"Home","heroSubtitle":"Welcome","kingdomJoyDescription":"Joy","aboutDescription":"About"}}`),
	} {
		if err := ValidatePagePayload("home", payload); !errors.Is(err, ErrInvalid) {
			t.Fatalf("payload=%s err=%v", payload, err)
		}
	}
}

func TestBuildPublicHomeV2PayloadLocalizesSettings(t *testing.T) {
	locations := []HomeLocation{{Key: "taipei", MapHref: "https://maps.example.com/taipei", SortOrder: 10, Translations: []HomeLocationTranslation{{Locale: "en", Name: "Taipei", Address: "Address"}, {Locale: "ja", Name: "台北", Address: "住所"}}}}
	payload, err := BuildPublicHomeV2Payload(validHomeV2PagePayload(), HomeLinks{ChurchYouTube: "https://youtube.com/@hhc"}, locations, "ja", "https://www.alive.org.tw/assets/banner-1")
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != `{"schemaVersion":2,"template":"home.v2","data":{"heroTitle":"愛從家開始","heroSubtitle":"在愛中成長","kingdomJoyDescription":"神國大樂介紹","aboutDescription":"教會介紹","bannerImageUrl":"https://www.alive.org.tw/assets/banner-1","links":{"churchYoutube":"https://youtube.com/@hhc","churchFacebook":"","musicYoutube":""},"locations":[{"key":"taipei","name":"台北","address":"住所","mapHref":"https://maps.example.com/taipei","sortOrder":10}]}}` {
		t.Fatalf("payload=%s", payload)
	}
}

func TestValidatePagePayloadRejectsInvalidContent(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		payload string
	}{
		{"unknown key", "custom", string(validHomePagePayload())},
		{"template mismatch", "home", `{"schemaVersion":1,"template":"legal.v1","data":{"heroTitle":"Home"}}`},
		{"unknown field", "home", `{"schemaVersion":1,"template":"home.v1","data":` + string(validHomePageData()) + `,"script":"bad"}`},
		{"missing required field", "home", `{"schemaVersion":1,"template":"home.v1","data":{"heroTitle":"Home"}}`},
		{"raw html", "home", string(replaceJSONField(t, validHomePagePayload(), "heroTitle", "<strong>Home</strong>"))},
		{"script URL", "home", string(replaceJSONField(t, validHomePagePayload(), "mapLink", "javascript:alert(1)"))},
		{"about wrong section count", "about", string(replaceJSONField(t, validAboutPagePayload(), "sections", []any{}))},
		{"legal empty sections", "privacy-policy", string(replaceJSONField(t, validLegalPagePayload("Privacy", "August 10, 2026"), "sections", []any{}))},
		{"legal empty paragraph", "privacy-policy", `{"schemaVersion":1,"template":"legal.v1","data":{"heroTitle":"Privacy","heroSubtitle":"","updatedAtLabel":"Updated","updatedAt":"August 10, 2026","intro":"Intro","sections":[{"title":"Section","body":[""]}]}}`},
		{"invalid updated date", "terms-of-use", string(replaceJSONField(t, validLegalPagePayload("Terms", "August 4, 2026"), "updatedAt", "February 31, 2026"))},
		{"oversized", "home", string(replaceJSONField(t, validHomePagePayload(), "heroTitle", string(make([]rune, 201))))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidatePagePayload(test.key, json.RawMessage(test.payload)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestValidatePagePayloadRejectsNullProperties(t *testing.T) {
	aboutTextCards := mutatePagePayload(t, validAboutPagePayload(), func(data map[string]any) {
		data["vision"].(map[string]any)["sections"].([]any)[0].(map[string]any)["cards"] = nil
	})
	aboutCardBody := mutatePagePayload(t, validAboutPagePayload(), func(data map[string]any) {
		data["vision"].(map[string]any)["sections"].([]any)[2].(map[string]any)["body"] = nil
	})
	legalSubtitle := mutatePagePayload(t, validLegalPagePayload("Privacy", "August 10, 2026"), func(data map[string]any) { data["heroSubtitle"] = nil })
	homeTitle := mutatePagePayload(t, validHomePagePayload(), func(data map[string]any) { data["heroTitle"] = nil })
	for _, test := range []struct {
		name, key string
		payload   json.RawMessage
	}{
		{"home required", "home", homeTitle},
		{"about forbidden cards", "about", aboutTextCards},
		{"about forbidden body", "about", aboutCardBody},
		{"legal optional subtitle", "privacy-policy", legalSubtitle},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := ValidatePagePayload(test.key, test.payload); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err=%v payload=%s", err, test.payload)
			}
		})
	}
}

func TestPageDefinitionIsImmutable(t *testing.T) {
	for _, test := range []struct{ key, template, route string }{
		{"home", "home.v1", "/"},
		{"about", "about.v1", "/about"},
		{"privacy-policy", "legal.v1", "/privacy-policy"},
		{"terms-of-use", "legal.v1", "/terms-of-use"},
	} {
		if err := ValidatePageDefinition(test.key, test.template, test.route); err != nil {
			t.Fatalf("%s: %v", test.key, err)
		}
		if err := ValidatePageDefinition(test.key, test.template, test.route+"-changed"); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%s changed route err=%v", test.key, err)
		}
	}
}

func TestPageDefinitionAllowsOnlyHomeV2AtRoot(t *testing.T) {
	if err := ValidatePageDefinition("home", "home.v2", "/"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ key, template, route string }{
		{"about", "home.v2", "/about"},
		{"home", "home.v2", "/home"},
		{"privacy-policy", "home.v2", "/privacy-policy"},
	} {
		if err := ValidatePageDefinition(test.key, test.template, test.route); !errors.Is(err, ErrInvalid) {
			t.Fatalf("%#v err=%v", test, err)
		}
	}
}

func validHomePagePayload() json.RawMessage {
	return json.RawMessage(`{"schemaVersion":1,"template":"home.v1","data":` + string(validHomePageData()) + `}`)
}

func validHomePageData() json.RawMessage {
	return json.RawMessage(`{"heroTitle":"愛從家開始","heroSubtitle":"在愛中成長","newsTitle":"最新消息","moreNews":"查看更多","weeklyTitle":"週報","downloadWeekly":"下載週報","videosTitle":"神國大樂","videosSubtitle":"用音樂敬拜神","watchMore":"觀看更多","aboutTitle":"關於我們","aboutBody":"教會介紹","aboutCta":"認識我們","locationsTitle":"服務據點","mapLink":"查看地圖"}`)
}

func validHomeV2PagePayload() json.RawMessage {
	return json.RawMessage(`{"schemaVersion":2,"template":"home.v2","data":{"heroTitle":"愛從家開始","heroSubtitle":"在愛中成長","kingdomJoyDescription":"神國大樂介紹","aboutDescription":"教會介紹"}}`)
}

func validAboutPagePayload() json.RawMessage {
	return json.RawMessage(`{"schemaVersion":1,"template":"about.v1","data":{"heroTitle":"關於我們","heroSubtitle":"合一與宣教","vision":{"intro":"異象介紹","imageAlt":"禱告","actionsImageAlt":"事工行動","sections":[{"eyebrow":"一個異象","title":"合一與宣教","body":"內容"},{"eyebrow":"兩個目標","title":"目標","body":"內容"},{"eyebrow":"三個行動","title":"行動","cards":[{"title":"分享生命","body":"內容"}]},{"eyebrow":"四個堅持","title":"堅持","cards":[{"title":"宣教導向","body":"內容"}]}]},"history":{"scripture":[{"lines":["經文"],"cite":"以賽亞書 49:1-3"}],"imageAlt":"插圖","intro":"沿革介紹","title":"教會沿革"}}}`)
}

func validLegalPagePayload(title, updatedAt string) json.RawMessage {
	value, _ := json.Marshal(map[string]any{"schemaVersion": 1, "template": "legal.v1", "data": map[string]any{"heroTitle": title, "heroSubtitle": "", "updatedAtLabel": "更新日期", "updatedAt": updatedAt, "intro": "說明", "sections": []any{map[string]any{"title": "範圍", "body": []string{"第一段"}}}}})
	return value
}

func replaceJSONField(t *testing.T, raw json.RawMessage, field string, value any) json.RawMessage {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	data, _ := payload["data"].(map[string]any)
	data[field] = value
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func mutatePagePayload(t *testing.T, raw json.RawMessage, mutate func(map[string]any)) json.RawMessage {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	mutate(payload["data"].(map[string]any))
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
