package hhcwebapi

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/getkin/kin-openapi/openapi3"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestOpenAPIDocumentsPublicBulletinByNumber(t *testing.T) {
	contents, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	document := string(contents)
	start := strings.Index(document, "  /bulletins/by-number/{issueNumber}:")
	end := strings.Index(document[start+1:], "\n  /")
	if start < 0 || end < 0 {
		t.Fatal("missing /bulletins/by-number/{issueNumber} path")
	}
	operation := document[start : start+1+end]
	for _, expected := range []string{
		"operationId: getPublicBulletinByNumber",
		"name: issueNumber",
		"type: integer",
		"minimum: 1",
		"maximum: 2147483647",
		"$ref: '#/components/parameters/BulletinEdition'",
		"'200': { $ref: '#/components/responses/PublicBulletin' }",
		"'400': { $ref: '#/components/responses/Error' }",
		"'404': { $ref: '#/components/responses/Error' }",
	} {
		if !strings.Contains(operation, expected) {
			t.Fatalf("operation missing %q:\n%s", expected, operation)
		}
	}
}

func TestOpenAPIDocumentsCampaignSearchAndClickBehavior(t *testing.T) {
	contents, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	document := string(contents)
	for _, expected := range []string{
		"name: q",
		"maxLength: 120",
		"clickBehavior:",
		"enum: [home, url, dismiss]",
		"actionUrl:",
	} {
		if !strings.Contains(document, expected) {
			t.Fatalf("OpenAPI document missing %q", expected)
		}
	}
}

func TestOpenAPIDocumentsBulletinNotificationIntentAndState(t *testing.T) {
	contents, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	document := string(contents)
	for _, expected := range []string{"notifySubscribers:", "notificationStatus:", "[not_requested, pending, queued, failed]", "NOTIFICATION_QUEUE_FAILED"} {
		if !strings.Contains(document, expected) {
			t.Fatalf("OpenAPI document missing %q", expected)
		}
	}
}

func TestOpenAPIDocumentsExplicitContentLocaleDeletion(t *testing.T) {
	contents, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	document := string(contents)
	for _, expected := range []string{"deleteLocales:", "Locales omitted from translations are preserved", "locale_set_mismatch"} {
		if !strings.Contains(document, expected) {
			t.Fatalf("OpenAPI document missing %q", expected)
		}
	}
}

func TestOpenAPIDocumentsPublicContentLocaleResolution(t *testing.T) {
	contents, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	document := string(contents)
	start := strings.LastIndex(document, "    PublicContentItem:")
	end := strings.Index(document[start+1:], "\n    BulletinIssueEnvelope:")
	if start < 0 || end < 0 {
		t.Fatal("missing PublicContentItem schema")
	}
	schema := document[start : start+1+end]
	for _, expected := range []string{"required: [id, title, resolvedLocale, availableLocales]", "resolvedLocale: { $ref: '#/components/schemas/ContentLocale' }", "availableLocales:", "items: { $ref: '#/components/schemas/ContentLocale' }"} {
		if !strings.Contains(schema, expected) {
			t.Fatalf("PublicContentItem missing %q:\n%s", expected, schema)
		}
	}
}

func TestOpenAPIDocumentsPublicLocationsContract(t *testing.T) {
	document := readOpenAPI(t)
	operation := operationByID(t, document, "listPublicLocations")
	for _, expected := range []string{
		"x-hhc-visibility: public",
		"x-hhc-callers: [api-gateway]",
		"security: []",
		"$ref: '#/components/parameters/ContentLocale'",
		"$ref: '#/components/responses/PublicLocationList'",
		"no locale fallback is applied",
	} {
		if !strings.Contains(operation, expected) {
			t.Errorf("locations operation missing %q:\n%s", expected, operation)
		}
	}
	for _, expected := range []string{
		"enum: [news, history, videos, locations]",
		"locationKey: { type: string, minLength: 1, maxLength: 120, pattern: '^[a-z0-9]+(?:-[a-z0-9]+)*$' }",
		"mapHref: { type: string, format: uri, pattern: '^https://' }",
		"sortOrder: { type: integer, minimum: 0 }",
		"required: [id, name, address, mapHref, sortOrder, resolvedLocale, availableLocales]",
	} {
		if !strings.Contains(document, expected) {
			t.Errorf("OpenAPI document missing %q", expected)
		}
	}
	if schemaBlock(document, "PublicLocation") == "" || schemaBlock(document, "PublicLocationListEnvelope") == "" {
		t.Fatal("missing public locations schemas")
	}
}

func TestOpenAPIDocumentsSiteSettingsContracts(t *testing.T) {
	document := readOpenAPI(t)
	public := operationByID(t, document, "getPublicSiteLayout")
	for _, expected := range []string{"x-hhc-visibility: public", "x-hhc-callers: [api-gateway]", "security: []", "$ref: '#/components/parameters/ContentLocale'", "$ref: '#/components/parameters/IfNoneMatch'", "$ref: '#/components/responses/SiteLayout'"} {
		if !strings.Contains(public, expected) {
			t.Errorf("public Site Layout operation missing %q:\n%s", expected, public)
		}
	}
	loader := openapi3.NewLoader()
	spec, err := loader.LoadFromFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for responseName, wantRef := range map[string]string{"SiteLayout": "#/components/responses/SiteLayout", "SiteLayoutNotModified": "#/components/responses/SiteLayoutNotModified"} {
		response := spec.Components.Responses[responseName]
		if response == nil || response.Value == nil {
			t.Fatalf("missing %s response component", responseName)
		}
		for headerName, headerRef := range map[string]string{"ETag": "#/components/headers/SiteLayoutETag", "Cache-Control": "#/components/headers/SiteLayoutCacheControl"} {
			if response.Value.Headers[headerName] == nil || response.Value.Headers[headerName].Ref != headerRef {
				t.Errorf("%s response %s header ref=%v, want %s", responseName, headerName, response.Value.Headers[headerName], headerRef)
			}
		}
		pathResponse := spec.Paths.Find("/site-layout").Get.Responses.Value(map[string]string{"SiteLayout": "200", "SiteLayoutNotModified": "304"}[responseName])
		if pathResponse == nil || pathResponse.Ref != wantRef {
			t.Errorf("/site-layout response ref=%v, want %s", pathResponse, wantRef)
		}
	}
	for name, want := range map[string]struct {
		pattern string
		value   any
	}{"SiteLayoutETag": {pattern: `^"site-layout-[1-9][0-9]*"$`}, "SiteLayoutCacheControl": {value: "public, max-age=30, must-revalidate"}} {
		header := spec.Components.Headers[name]
		if header == nil || header.Value == nil || header.Value.Schema == nil || header.Value.Schema.Value == nil || header.Value.Schema.Value.Type == nil || !header.Value.Schema.Value.Type.Is("string") {
			t.Fatalf("%s must be a string header", name)
		}
		if header.Value.Schema.Value.Pattern != want.pattern || header.Value.Schema.Value.Const != want.value {
			t.Errorf("%s pattern=%q const=%v", name, header.Value.Schema.Value.Pattern, header.Value.Schema.Value.Const)
		}
	}
	for operationID, scope := range map[string]string{
		"getSiteSettings": "cms:read", "saveSiteSettings": "cms:write", "publishSiteSettings": "cms:publish",
		"unpublishSiteSettings": "cms:publish", "listSiteSettingsRevisions": "cms:read", "restoreSiteSettingsRevision": "cms:write",
	} {
		operation := operationByID(t, document, operationID)
		for _, expected := range []string{"x-hhc-visibility: admin", "x-hhc-callers: [api-gateway]", "servers: [{ url: https://admin.alive.org.tw/api }]", "x-required-scopes: ['" + scope + "']", "daprApiToken", "daprCallerAppId", "trustedUserId", "trustedAuthProvider", "trustedScopes"} {
			if !strings.Contains(operation, expected) {
				t.Errorf("%s missing %q:\n%s", operationID, expected, operation)
			}
		}
	}
	for _, schema := range []string{"SiteLayout", "SiteSettings", "SiteSettingsWriteInput", "SiteSettingsRevision"} {
		block := schemaBlock(document, schema)
		if block == "" {
			t.Errorf("missing %s schema", schema)
		}
		if strings.Contains(block, "canonicalHost") || strings.Contains(block, "apiRoot") || strings.Contains(block, "accountUrl") {
			t.Errorf("%s exposes runtime configuration:\n%s", schema, block)
		}
	}
}

func TestOpenAPIDocumentsFixedEditorialPageContracts(t *testing.T) {
	document := readOpenAPI(t)
	operation := operationByID(t, document, "getPublicPage")
	for _, expected := range []string{
		"x-hhc-visibility: public",
		"x-hhc-callers: [api-gateway]",
		"security: []",
		"$ref: '#/components/parameters/PageKey'",
		"$ref: '#/components/parameters/ContentLocale'",
		"$ref: '#/components/parameters/IfNoneMatch'",
		"$ref: '#/components/responses/PublicEditorialPage'",
		"$ref: '#/components/responses/PublicEditorialPageNotModified'",
	} {
		if !strings.Contains(operation, expected) {
			t.Errorf("public page operation missing %q:\n%s", expected, operation)
		}
	}
	for _, schema := range []string{"HomePageContentV1", "AboutPageContentV1", "LegalPageContentV1", "PageContent", "PublicEditorialPage"} {
		if schemaBlock(document, schema) == "" {
			t.Errorf("missing %s schema", schema)
		}
	}
	pageContent := schemaBlock(document, "PageContent")
	for _, expected := range []string{"oneOf:", "propertyName: template", "home.v1", "about.v1", "legal.v1"} {
		if !strings.Contains(pageContent, expected) {
			t.Errorf("PageContent missing %q:\n%s", expected, pageContent)
		}
	}
	if !strings.Contains(schemaBlock(document, "ContentModule"), "enum: [news, history, videos, locations, pages]") {
		t.Error("ContentModule must include pages")
	}
	if strings.Contains(schemaBlock(document, "CreatableContentModule"), "pages") || strings.Contains(schemaBlock(document, "TranslatableContentModule"), "pages") {
		t.Error("generic create and AI translation modules must exclude pages")
	}
	if !strings.Contains(operationByID(t, document, "deleteContent"), "'405':") {
		t.Error("deleteContent must document fixed-page 405")
	}
	for _, operationID := range []string{"createContent", "deleteContent"} {
		if !strings.Contains(operationByID(t, document, operationID), "'405': { $ref: '#/components/responses/Error' }") {
			t.Errorf("%s must use the standard 405 error envelope", operationID)
		}
	}
	notModified := responseBlock(document, "PublicEditorialPageNotModified")
	for _, expected := range []string{"ETag:", "Cache-Control:", "public, max-age=30, must-revalidate"} {
		if !strings.Contains(notModified, expected) {
			t.Errorf("page 304 response missing %q:\n%s", expected, notModified)
		}
	}
}

func TestOpenAPIDocumentsPageGroupPublicationContracts(t *testing.T) {
	document := readOpenAPI(t)
	if !strings.Contains(schemaBlock(document, "ContentStatus"), "pending_removal") {
		t.Error("ContentStatus must include pending_removal")
	}
	if !strings.Contains(schemaBlock(document, "PublicationContentModule"), "enum: [news, locations, pages]") {
		t.Error("publication module must exclude history and videos")
	}
	for _, operationID := range []string{"publishContent", "unpublishContent", "restoreContentRevision"} {
		operation := operationByID(t, document, operationID)
		if !strings.Contains(operation, "$ref: '#/components/parameters/PublicationContentModule'") || !strings.Contains(operation, "'405': { $ref: '#/components/responses/Error' }") {
			t.Errorf("%s must restrict child publication and document 405:\n%s", operationID, operation)
		}
	}
	for _, schema := range []string{"PageGroupManifest", "PageGroupManifestItem"} {
		if schemaBlock(document, schema) == "" {
			t.Errorf("missing %s", schema)
		}
	}
	if !strings.Contains(schemaBlock(document, "ContentRevision"), "groupManifest: { $ref: '#/components/schemas/PageGroupManifest' }") {
		t.Error("ContentRevision must expose optional groupManifest")
	}
	deleteOperation := operationByID(t, document, "deleteContent")
	for _, expected := range []string{"never-published and never-manifested draft", "manifest-backed", "pending removal", "'204':"} {
		if !strings.Contains(deleteOperation, expected) {
			t.Errorf("deleteContent missing %q:\n%s", expected, deleteOperation)
		}
	}
	for _, operationID := range []string{"publishContent", "unpublishContent", "restoreContentRevision"} {
		operation := operationByID(t, document, operationID)
		if !strings.Contains(operation, "Home") || !strings.Contains(operation, "About") || !strings.Contains(operation, "group") {
			t.Errorf("%s must document Home/About group semantics", operationID)
		}
	}
	if restore := operationByID(t, document, "restoreContentRevision"); !strings.Contains(restore, "marks removed or absent children pending removal") || !strings.Contains(restore, "leaves live projections unchanged") {
		t.Errorf("restoreContentRevision must distinguish included drafts and pending removals:\n%s", restore)
	}
}

func TestOpenAPIDocumentsExactHomeV2AndBannerContracts(t *testing.T) {
	document := readOpenAPI(t)
	for _, schema := range []string{"HomePageWriteInputV2", "HomePageContentV2Draft", "HomePageContentV2", "HomeLocation", "HomeLocationTranslation", "PublicHomeLocation", "HomeBannerUploadInput", "HomeBannerCompleteInput"} {
		if schemaBlock(document, schema) == "" {
			t.Errorf("missing %s schema", schema)
		}
	}
	for _, operationID := range []string{"createHomeBannerUpload", "getHomeBannerStatus", "retryHomeBannerScan", "completeHomeBannerUpload"} {
		operation := operationByID(t, document, operationID)
		for _, expected := range []string{"x-hhc-visibility: admin", "x-hhc-callers: [api-gateway]", "daprApiToken", "daprCallerAppId", "trustedUserId", "trustedAuthProvider", "trustedScopes"} {
			if !strings.Contains(operation, expected) {
				t.Errorf("%s missing %q:\n%s", operationID, expected, operation)
			}
		}
	}
	for _, operationID := range []string{"createHomeBannerUpload", "retryHomeBannerScan", "completeHomeBannerUpload"} {
		if !strings.Contains(operationByID(t, document, operationID), "x-required-scopes: ['cms:write', 'assets:write']") {
			t.Errorf("%s missing write scopes", operationID)
		}
	}
	if !strings.Contains(operationByID(t, document, "getHomeBannerStatus"), "x-required-scopes: ['cms:read']") {
		t.Error("getHomeBannerStatus missing read scope")
	}
	pageContent := schemaBlock(document, "PageContent")
	pageWriteContent := schemaBlock(document, "PageWriteContent")
	for _, block := range []string{pageContent, pageWriteContent} {
		if !strings.Contains(block, "home.v2") || !strings.Contains(block, "propertyName: template") {
			t.Errorf("Home v2 discriminator missing:\n%s", block)
		}
	}

	schema := compileOpenAPISchema(t, "HomePageWriteInputV2")
	var valid map[string]any
	if err := json.Unmarshal(openAPIValidHomeV2WriteInput(), &valid); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(valid); err != nil {
		t.Fatalf("valid Home v2 rejected: %v", err)
	}
	valid["translations"].([]any)[0].(map[string]any)["bodyJson"].(map[string]any)["data"].(map[string]any)["newsTitle"] = "removed"
	if err := schema.Validate(valid); err == nil {
		t.Fatal("Home v2 accepted a removed localized field")
	}
}

func TestOpenAPIPagePayloadSchemasMatchRuntimeValidation(t *testing.T) {
	about := compileOpenAPISchema(t, "AboutPageContentV1")
	valid := map[string]any{}
	if err := json.Unmarshal(openAPIValidAboutPagePayload(), &valid); err != nil {
		t.Fatal(err)
	}
	if err := about.Validate(valid); err != nil {
		t.Fatalf("About schema rejected runtime-valid payload: %v", err)
	}
	sections := valid["data"].(map[string]any)["vision"].(map[string]any)["sections"].([]any)
	sections[0].(map[string]any)["cards"] = []any{map[string]any{"title": "wrong", "body": "shape"}}
	if err := about.Validate(valid); err == nil {
		t.Fatal("About schema accepted cards in positional text section")
	}

	legal := compileOpenAPISchema(t, "LegalPageContentV1")
	var legalValue map[string]any
	if err := json.Unmarshal(openAPIValidLegalPagePayload(), &legalValue); err != nil {
		t.Fatal(err)
	}
	delete(legalValue["data"].(map[string]any), "heroSubtitle")
	encoded, _ := json.Marshal(legalValue)
	if err := content.ValidatePagePayload("privacy-policy", encoded); err != nil {
		t.Fatalf("runtime rejected omitted legal heroSubtitle: %v", err)
	}
	if err := legal.Validate(legalValue); err != nil {
		t.Fatalf("OpenAPI rejected runtime-valid omitted legal heroSubtitle: %v", err)
	}
}

func TestOpenAPIAndRuntimeRejectNullPageProperties(t *testing.T) {
	for _, test := range []struct {
		name, key, schema string
		payload           []byte
		mutate            func(map[string]any)
	}{
		{"home required", "home", "HomePageContentV1", openAPIValidHomePagePayload(), func(data map[string]any) { data["heroTitle"] = nil }},
		{"about forbidden cards", "about", "AboutPageContentV1", openAPIValidAboutPagePayload(), func(data map[string]any) {
			data["vision"].(map[string]any)["sections"].([]any)[0].(map[string]any)["cards"] = nil
		}},
		{"about forbidden body", "about", "AboutPageContentV1", openAPIValidAboutPagePayload(), func(data map[string]any) {
			data["vision"].(map[string]any)["sections"].([]any)[2].(map[string]any)["body"] = nil
		}},
		{"legal optional subtitle", "privacy-policy", "LegalPageContentV1", openAPIValidLegalPagePayload(), func(data map[string]any) { data["heroSubtitle"] = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			var value map[string]any
			if err := json.Unmarshal(test.payload, &value); err != nil {
				t.Fatal(err)
			}
			test.mutate(value["data"].(map[string]any))
			encoded, _ := json.Marshal(value)
			if err := content.ValidatePagePayload(test.key, encoded); err == nil {
				t.Fatal("runtime accepted JSON null")
			}
			if err := compileOpenAPISchema(t, test.schema).Validate(value); err == nil {
				t.Fatal("OpenAPI accepted JSON null")
			}
		})
	}
}

func TestOpenAPISiteSettingsSchemasAcceptValidWireShapes(t *testing.T) {
	if err := compileOpenAPISchema(t, "SiteLayout").Validate(validPublicSiteLayout()); err != nil {
		t.Fatalf("SiteLayout rejected valid payload: %v", err)
	}
	adminSchema := compileOpenAPISchema(t, "SiteSettingsWriteInput")
	if err := adminSchema.Validate(validAdminSiteSettingsInput()); err != nil {
		t.Fatalf("SiteSettingsWriteInput rejected valid payload: %v", err)
	}
	if err := compileOpenAPISchema(t, "SiteSettings").Validate(validAdminSiteSettings()); err != nil {
		t.Fatalf("SiteSettings rejected valid payload: %v", err)
	}
	for _, safeURL := range []string{"https://media.example.com/channel", "https://example.xn--fiqs8s/channel", "https://example.com/channel?%66oo=bar"} {
		value := validAdminSiteSettingsInput()
		externalLinks(value)["churchYoutube"] = safeURL
		if err := adminSchema.Validate(value); err != nil {
			t.Fatalf("SiteSettingsWriteInput rejected runtime-valid URL %q: %v", safeURL, err)
		}
	}
}

func TestOpenAPISiteSettingsRejectsRuntimeInvalidWireShapes(t *testing.T) {
	adminSchema := compileOpenAPISchema(t, "SiteSettingsWriteInput")
	publicSchema := compileOpenAPISchema(t, "SiteLayout")

	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"admin concrete href", func(value map[string]any) { firstHeader(value)["href"] = "/zh-Hant/about" }},
		{"admin cross-key href", func(value map[string]any) { firstHeader(value)["href"] = "/{locale}/news" }},
		{"missing locale", func(value map[string]any) { value["locales"] = value["locales"].([]any)[:4] }},
		{"duplicate locale key", func(value map[string]any) {
			locales := value["locales"].([]any)
			locales[1].(map[string]any)["locale"] = locales[0].(map[string]any)["locale"]
		}},
		{"missing header key", func(value map[string]any) {
			locale := value["locales"].([]any)[0].(map[string]any)
			locale["header"] = locale["header"].([]any)[:2]
		}},
		{"duplicate header key", func(value map[string]any) {
			header := value["locales"].([]any)[0].(map[string]any)["header"].([]any)
			header[1] = map[string]any{"key": "about", "label": "Other", "href": "/{locale}/about", "visible": true}
		}},
		{"missing legal key", func(value map[string]any) {
			locale := value["locales"].([]any)[0].(map[string]any)
			locale["legal"] = locale["legal"].([]any)[:1]
		}},
		{"duplicate legal key", func(value map[string]any) {
			legal := value["locales"].([]any)[0].(map[string]any)["legal"].([]any)
			legal[1] = map[string]any{"key": "privacy-policy", "label": "Other", "href": "/{locale}/privacy-policy", "visible": true}
		}},
		{"empty visible label", func(value map[string]any) { firstHeader(value)["label"] = "" }},
		{"whitespace visible label", func(value map[string]any) { firstHeader(value)["label"] = " \t" }},
		{"userinfo URL", func(value map[string]any) { externalLinks(value)["churchYoutube"] = "https://user@example.com/channel" }},
		{"fragment URL", func(value map[string]any) {
			externalLinks(value)["churchYoutube"] = "https://example.com/channel#private"
		}},
		{"private IP URL", func(value map[string]any) { externalLinks(value)["churchYoutube"] = "https://10.0.0.1/channel" }},
		{"loopback URL", func(value map[string]any) { externalLinks(value)["churchYoutube"] = "https://127.0.0.1/channel" }},
		{"public IPv4 URL", func(value map[string]any) { externalLinks(value)["churchYoutube"] = "https://8.8.8.8/channel" }},
		{"trailing-dot IPv4 URL", func(value map[string]any) { externalLinks(value)["churchYoutube"] = "https://8.8.8.8./channel" }},
		{"integer numeric host URL", func(value map[string]any) { externalLinks(value)["churchYoutube"] = "https://2130706433/channel" }},
		{"short numeric host URL", func(value map[string]any) { externalLinks(value)["churchYoutube"] = "https://127.1/channel" }},
		{"hex numeric host URL", func(value map[string]any) { externalLinks(value)["churchYoutube"] = "https://0x7f000001/channel" }},
		{"mixed hex numeric host URL", func(value map[string]any) { externalLinks(value)["churchYoutube"] = "https://0x7f.0.0.1/channel" }},
		{"canonical public IPv6 URL", func(value map[string]any) {
			externalLinks(value)["churchYoutube"] = "https://[2606:4700:4700::1111]/channel"
		}},
		{"expanded public IPv6 URL", func(value map[string]any) {
			externalLinks(value)["churchYoutube"] = "https://[2606:4700:4700:0000:0000:0000:0000:1111]/channel"
		}},
		{"hex-mapped public IPv6 URL", func(value map[string]any) {
			externalLinks(value)["churchYoutube"] = "https://[::ffff:808:808]/channel"
		}},
		{"IPv4-mapped private IPv6 URL", func(value map[string]any) {
			externalLinks(value)["churchYoutube"] = "https://[::ffff:10.0.0.1]/channel"
		}},
		{"IPv4-mapped loopback IPv6 URL", func(value map[string]any) {
			externalLinks(value)["churchYoutube"] = "https://[::ffff:127.0.0.1]/channel"
		}},
		{"internal URL", func(value map[string]any) { externalLinks(value)["churchYoutube"] = "https://service.internal/channel" }},
		{"trailing-dot internal URL", func(value map[string]any) {
			externalLinks(value)["churchYoutube"] = "https://service.internal./channel"
		}},
		{"storage URL", func(value map[string]any) {
			externalLinks(value)["churchYoutube"] = "https://account.blob.core.windows.net/container"
		}},
		{"SAS URL", func(value map[string]any) {
			externalLinks(value)["churchYoutube"] = "https://example.com/channel?sv=2024-11-04&sig=secret"
		}},
		{"percent-encoded SAS key URL", func(value map[string]any) {
			externalLinks(value)["churchYoutube"] = "https://example.com/channel?%73v=2024-11-04&%73ig=secret"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := validAdminSiteSettingsInput()
			test.mutate(value)
			if err := adminSchema.Validate(value); err == nil {
				t.Fatalf("SiteSettingsWriteInput accepted invalid payload: %#v", value)
			}
		})
	}

	for _, field := range []string{"siteName", "englishName", "copyrightHolder", "allRightsReserved", "seoTitleSuffix", "seoDescriptionFallback"} {
		t.Run("whitespace-only "+field, func(t *testing.T) {
			value := validAdminSiteSettingsInput()
			value["locales"].([]any)[0].(map[string]any)[field] = " \t"
			if err := adminSchema.Validate(value); err == nil {
				t.Fatalf("SiteSettingsWriteInput accepted whitespace-only %s", field)
			}
		})
	}

	for _, test := range []struct {
		name string
		href string
	}{
		{"public template href", "/{locale}/about"},
		{"public cross-key href", "/ja/news"},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := validPublicSiteLayout()
			firstHeader(value)["href"] = test.href
			if err := publicSchema.Validate(value); err == nil {
				t.Fatalf("SiteLayout accepted invalid href %q", test.href)
			}
		})
	}
}

func loadOpenAPI(t *testing.T) *openapi3.T {
	t.Helper()
	document, err := openapi3.NewLoader().LoadFromFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func compileOpenAPISchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	document := loadOpenAPI(t)
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var resource any
	if err := json.Unmarshal(encoded, &resource); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	if err := compiler.AddResource("openapi.json", resource); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("openapi.json#/components/schemas/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func validPublicSiteLayout() map[string]any {
	value := validSiteLocale("ja", "/ja")
	value["links"] = validExternalLinks()
	value["version"], value["publishedAt"] = float64(4), "2026-08-29T00:00:00Z"
	return value
}

func validAdminSiteSettingsInput() map[string]any {
	locales := make([]any, 0, 5)
	for _, locale := range []string{"zh-Hant", "zh-Hans", "en", "ja", "ko"} {
		locales = append(locales, validSiteLocale(locale, "/{locale}"))
	}
	return map[string]any{"locales": locales, "links": validExternalLinks()}
}

func validAdminSiteSettings() map[string]any {
	value := validAdminSiteSettingsInput()
	value["id"], value["status"], value["version"] = "default", "draft", float64(3)
	value["createdBy"], value["updatedBy"] = "admin", "admin"
	value["createdAt"], value["updatedAt"] = "2026-08-29T00:00:00Z", "2026-08-29T00:00:00Z"
	return value
}

func validSiteLocale(locale, prefix string) map[string]any {
	return map[string]any{
		"locale": locale, "siteName": "教会", "englishName": "HHC", "copyrightHolder": "HHC", "allRightsReserved": "All rights reserved",
		"seoTitleSuffix": "HHC", "seoDescriptionFallback": "description",
		"header": []any{
			map[string]any{"key": "about", "label": "概要", "href": prefix + "/about", "visible": true},
			map[string]any{"key": "news", "label": "ニュース", "href": prefix + "/news", "visible": true},
			map[string]any{"key": "literature-ministry", "label": "文書", "href": prefix + "/literature-ministry", "visible": true},
		},
		"legal": []any{
			map[string]any{"key": "privacy-policy", "label": "Privacy", "href": prefix + "/privacy-policy", "visible": true},
			map[string]any{"key": "terms-of-use", "label": "Terms", "href": prefix + "/terms-of-use", "visible": true},
		},
	}
}

func validExternalLinks() map[string]any {
	return map[string]any{
		"churchYoutube":  "https://youtube.com/@hhc33?si=public",
		"churchFacebook": "https://facebook.com/hhc?locale=zh_TW",
		"musicYoutube":   "https://youtube.com/@music?si=public",
	}
}

func firstHeader(value map[string]any) map[string]any {
	locales, ok := value["locales"].([]any)
	if ok {
		value = locales[0].(map[string]any)
	}
	return value["header"].([]any)[0].(map[string]any)
}

func externalLinks(value map[string]any) map[string]any {
	return value["links"].(map[string]any)
}

func TestOpenAPIContentWriteInputKeepsExistingModulesCompatible(t *testing.T) {
	schema := contentWriteInputSchema(t)
	for _, test := range []struct {
		name, body string
	}{
		{"news", `{"authorName":"Pastor","slug":"announcement","displayDate":"2026-08-28","detailLayout":"top","translations":[{"locale":"zh-Hant","title":"消息","summary":"摘要","body":"內容","imageAlt":"圖片"}]}`},
		{"history", `{"eventDate":"1988-03","translations":[{"locale":"zh-Hant","title":"開始家庭聚會","body":"內容","dateLabel":"1988年3月"}]}`},
		{"videos", `{"youtubeVideoId":"K3ckFWeSQ-k","homeEligible":true,"translations":[{"locale":"zh-Hant","title":"影片"}]}`},
		{"pages", `{"pageKey":"home","pageTemplate":"home.v1","routePath":"/","indexable":true,"translations":[{"locale":"zh-Hant","bodyJson":{"schemaVersion":1,"template":"home.v1","data":{"heroTitle":"Home","heroSubtitle":"Welcome","newsTitle":"News","moreNews":"More","weeklyTitle":"Weekly","downloadWeekly":"Download","videosTitle":"Videos","videosSubtitle":"Music","watchMore":"Watch","aboutTitle":"About","aboutBody":"About us","aboutCta":"Meet us","locationsTitle":"Locations","mapLink":"Map"}}}]}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateOpenAPIContentWriteInput(schema, []byte(test.body)); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOpenAPIContentTranslationKeepsResponseTitleRequired(t *testing.T) {
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}

	writeTranslation := document.Components.Schemas["ContentWriteTranslation"]
	responseTranslation := document.Components.Schemas["ContentTranslation"]
	if writeTranslation == nil || writeTranslation.Value == nil {
		t.Fatal("missing ContentWriteTranslation schema")
	}
	if responseTranslation == nil || responseTranslation.Value == nil {
		t.Fatal("missing ContentTranslation schema")
	}
	if slices.Contains(writeTranslation.Value.Required, "title") {
		t.Fatal("page writes must be able to omit their derived title")
	}
	if !slices.Contains(responseTranslation.Value.Required, "title") {
		t.Fatal("ContentItem translations must keep title required")
	}

	writeItems := document.Components.Schemas["ContentWriteFields"].Value.Properties["translations"].Value.Items
	if writeItems.Ref != "#/components/schemas/ContentWriteTranslation" {
		t.Fatalf("ContentWriteFields translations ref = %q", writeItems.Ref)
	}
	item := document.Components.Schemas["ContentItem"].Value
	if len(item.AllOf) < 2 || item.AllOf[1].Value == nil {
		t.Fatal("ContentItem response fields missing")
	}
	responseItems := item.AllOf[1].Value.Properties["translations"].Value.Items
	if responseItems.Ref != "#/components/schemas/ContentTranslation" {
		t.Fatalf("ContentItem translations ref = %q", responseItems.Ref)
	}

	response := map[string]any{
		"id": "00000000-0000-0000-0000-000000000001", "module": "news", "status": "draft", "version": 1,
		"translations": []any{map[string]any{"locale": "zh-Hant", "title": "消息"}},
		"isPublished":  false, "createdBy": "actor", "updatedBy": "actor",
		"createdAt": "2026-08-29T00:00:00Z", "updatedAt": "2026-08-29T00:00:00Z",
	}
	if err := item.VisitJSON(response, openapi3.EnableJSONSchema2020()); err != nil {
		t.Fatalf("valid ContentItem response: %v", err)
	}
	delete(response["translations"].([]any)[0].(map[string]any), "title")
	if err := item.VisitJSON(response, openapi3.EnableJSONSchema2020()); err == nil {
		t.Fatal("ContentItem response without translation title passed validation")
	}
}

func TestOpenAPIDocumentsNewsSEOFields(t *testing.T) {
	contents, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	document := string(contents)
	for _, expected := range []string{
		"authorName: { type: string, maxLength: 200 }",
		"firstPublishedAt: { type: string, format: date-time }",
		"lastPublishedAt: { type: string, format: date-time }",
	} {
		if !strings.Contains(document, expected) {
			t.Fatalf("OpenAPI missing %q", expected)
		}
	}
}

func TestOpenAPISplitsContentLocalesFromWeeklyEditions(t *testing.T) {
	contents, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	document := string(contents)
	for _, expected := range []string{
		"ContentLocale:",
		"enum: [zh-Hant, zh-Hans, en, ja, ko]",
		"BulletinEdition:",
		"enum: [zh-Hant, zh-Hans, en]",
		"ContentTranslationTargetLocale:",
		"enum: [zh-Hans, en, ja, ko]",
		"BulletinTranslationTargetEdition:",
		"enum: [zh-Hans, en]",
		"$ref: '#/components/parameters/BulletinTranslationTargetEdition'",
		"$ref: '#/components/parameters/ContentTranslationTargetLocale'",
	} {
		if !strings.Contains(document, expected) {
			t.Fatalf("OpenAPI document missing %q", expected)
		}
	}
	if strings.Contains(document, "#/components/schemas/Locale") || strings.Contains(document, "#/components/parameters/Locale") {
		t.Fatal("OpenAPI still exposes an ambiguous Locale contract")
	}
}

func TestOpenAPIDocumentsTranslationPreviewContracts(t *testing.T) {
	contents, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	document := string(contents)
	for _, expected := range []string{
		"/admin/content/{module}/{contentId}/translation-previews/{targetLocale}:",
		"/admin/bulletins/{issueId}/translation-previews/{targetLocale}:",
		"operationId: previewContentTranslation",
		"operationId: previewBulletinTranslation",
		"x-required-scopes: ['cms:write']",
		"TranslationPreviewInput:",
		"additionalProperties: false",
		"required: [sourceLocale, replaceExisting]",
		"enum: [zh-Hans, en, ja, ko]",
		"ContentTranslationPreviewEnvelope:",
		"BulletinTranslationPreviewEnvelope:",
		"required: [sourceLocale, targetLocale, sourceVersion, translation, retryAfterSeconds]",
		"retryAfterSeconds:",
		"invalid_translation_request",
		"translation_exists",
		"version_mismatch",
		"translation_rate_limited",
		"translation_content_filtered",
		"Retry-After:",
		"Integer seconds until the caller may retry this translation target.",
		"translation_provider_error",
		"translation_timeout",
		"translation_disabled",
	} {
		if !strings.Contains(document, expected) {
			t.Fatalf("OpenAPI document missing %q", expected)
		}
	}
}

func TestOpenAPICatalogContract(t *testing.T) {
	document := readOpenAPI(t)
	if err := validateCatalogContract(document); err != nil {
		t.Fatal(err)
	}
}

func TestOpenAPITagDefinitionsMatchOperations(t *testing.T) {
	document := readOpenAPI(t)
	want := []string{"Admin", "Authenticated", "Operations", "Private", "Public"}
	if got := topLevelTagNames(document); !reflect.DeepEqual(got, want) {
		t.Fatalf("top-level tags = %v, want %v", got, want)
	}
	tags := map[string]bool{}
	for _, operation := range parseCatalogOperations(document) {
		tag, count := operationValue(operation.block, "tags")
		if count == 1 {
			tags[strings.TrimSpace(strings.Trim(tag, "[]"))] = true
		}
	}
	got := make([]string, 0, len(tags))
	for tag := range tags {
		got = append(got, tag)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("operation tags = %v, want %v", got, want)
	}
}

func topLevelTagNames(document string) []string {
	head, _, _ := strings.Cut(document, "paths:\n")
	var tags []string
	for _, line := range strings.Split(head, "\n") {
		if strings.HasPrefix(line, "  - name: ") {
			tags = append(tags, strings.TrimPrefix(line, "  - name: "))
		}
	}
	sort.Strings(tags)
	return tags
}

func TestOpenAPICatalogContractRejectsInvalidMetadataAndRoutes(t *testing.T) {
	document := readOpenAPI(t)
	tests := []struct {
		name, old, replacement, want string
	}{
		{"non-3.1 document", "openapi: 3.1.0", "openapi: 3.0.3", "OpenAPI 3.1"},
		{"missing service", "x-hhc-service: hhc-web-api\n", "", "x-hhc-service"},
		{"missing owner", "x-hhc-owner: HHC Platform\n", "", "x-hhc-owner"},
		{"missing repository", "x-hhc-repository: HallelujahHomeChurch/hhc-web-api\n", "", "x-hhc-repository"},
		{"missing visibility", "      x-hhc-visibility: operations\n", "", "missing x-hhc-visibility"},
		{"unknown visibility", "      x-hhc-visibility: operations", "      x-hhc-visibility: partner", "unknown visibility"},
		{"tag mismatch", "      tags: [Operations]", "      tags: [Public]", "does not match visibility"},
		{"missing callers", "      x-hhc-callers: [api-gateway]\n", "", "missing x-hhc-callers"},
		{"missing registered route", "  /health:\n", "  /missing-health:\n", "missing documented route GET /health"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := strings.Replace(document, test.old, test.replacement, 1)
			if mutated == document {
				t.Fatalf("mutation marker %q not found", test.old)
			}
			if err := validateCatalogContract(mutated); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateCatalogContract() error = %v, want containing %q", err, test.want)
			}
		})
	}

	t.Run("unexpected documented route", func(t *testing.T) {
		mutated := strings.Replace(document, "paths:\n", "paths:\n  /unexpected:\n    get:\n      summary: Unexpected\n      operationId: unexpected\n      tags: [Public]\n      x-hhc-visibility: public\n      x-hhc-callers: [api-gateway]\n      security: []\n      responses: { '200': { description: Unexpected } }\n", 1)
		if err := validateCatalogContract(mutated); err == nil || !strings.Contains(err.Error(), "unexpected documented route GET /unexpected") {
			t.Fatalf("validateCatalogContract() error = %v, want unexpected route", err)
		}
	})
}

func TestOpenAPIDocumentsGatewayTrustAndServiceBoundaries(t *testing.T) {
	document := readOpenAPI(t)
	for _, expected := range []string{
		"Dapr-Caller-App-Id",
		"dapr-api-token",
		"X-HHC-User-ID",
		"X-HHC-Auth-Provider",
		"X-HHC-Scopes",
		"Campaign routes are authorization-preserving proxies to engagement-api, which owns campaign and consent state.",
		"Website API owns CMS records and publication state; asset-api owns file bytes, scanning, derivatives, and grants.",
	} {
		if !strings.Contains(document, expected) {
			t.Errorf("OpenAPI document missing %q", expected)
		}
	}
}

func TestOpenAPIDocumentsMeetingOperationsVisibilityAndDataBoundaries(t *testing.T) {
	document := readOpenAPI(t)
	wantVisibility := map[string]string{
		"listPublicMeetings": "public", "listMeetingSyncWindows": "authenticated",
		"listMeetings": "admin", "listPrivateMeetingOccurrences": "private",
	}
	for operationID, visibility := range wantVisibility {
		operation := operationByID(t, document, operationID)
		for _, expected := range []string{
			"x-hhc-visibility: " + visibility,
			"x-hhc-callers:",
			"x-hhc-required-headers:",
			"x-hhc-cache:",
			"security:",
		} {
			if !strings.Contains(operation, expected) {
				t.Errorf("%s missing %q:\n%s", operationID, expected, operation)
			}
		}
	}
	for _, operationID := range []string{
		"listPublicMeetings", "getPublicMeeting", "listPublicMeetingOccurrences", "listMeetingSyncWindows",
		"listChurchUnits", "createChurchUnit", "getChurchUnit", "updateChurchUnit", "setChurchUnitStatus",
		"listOperationsResources", "createOperationsResource", "getOperationsResource", "updateOperationsResource", "setOperationsResourceStatus",
		"listMeetings", "createMeeting", "getMeeting", "updateMeeting", "setMeetingStatus",
		"putMeetingOccurrenceOverride", "deleteMeetingOccurrenceOverride", "replaceMeetingCollectionBindings",
		"listPrivateMeetingOccurrences", "listPrivateMeetingSyncWindows",
	} {
		operation := operationByID(t, document, operationID)
		for _, expected := range []string{"x-hhc-visibility:", "x-hhc-callers:", "x-hhc-required-headers:", "x-hhc-cache:", "security:", "responses:"} {
			if !strings.Contains(operation, expected) {
				t.Errorf("%s missing %q:\n%s", operationID, expected, operation)
			}
		}
	}
	for _, schema := range []string{"PublicMeeting", "PublicMeetingOccurrence", "MediaSyncWindow"} {
		block := schemaBlock(document, schema)
		if block == "" {
			t.Fatalf("missing %s schema", schema)
		}
		for _, forbidden := range []string{"collectionId", "createdBy", "updatedBy", "createdAt", "updatedAt"} {
			if strings.Contains(block, forbidden) {
				t.Errorf("%s leaks %q:\n%s", schema, forbidden, block)
			}
		}
	}
	for _, forbidden := range []string{"meetingId", "meetingKey", "collectionId", "name", "version"} {
		if block := schemaBlock(document, "MediaSyncWindow"); strings.Contains(block, forbidden) {
			t.Errorf("MediaSyncWindow leaks %q:\n%s", forbidden, block)
		}
	}
}

func TestOpenAPIComposedSchemasRemainSatisfiable(t *testing.T) {
	document := readOpenAPI(t)
	tests := []struct {
		name     string
		contains []string
	}{
		{"ContentWriteInput", []string{"allOf:", "$ref: '#/components/schemas/ContentWriteFields'", "unevaluatedProperties: false"}},
		{"ContentItem", []string{"$ref: '#/components/schemas/ContentWriteFields'", "required: [id, module, status, version, translations, isPublished, createdBy, updatedBy, createdAt, updatedAt]", "$ref: '#/components/schemas/ContentTranslation'", "unevaluatedProperties: false"}},
		{"CreateCampaignSchedule", []string{"$ref: '#/components/schemas/CampaignScheduleFields'", "unevaluatedProperties: false"}},
		{"UpdateCampaignSchedule", []string{"$ref: '#/components/schemas/CampaignScheduleFields'", "required: [enabled]", "enabled: { type: boolean }", "unevaluatedProperties: false"}},
		{"ChurchUnit", []string{"$ref: '#/components/schemas/ChurchUnitInput'", "unevaluatedProperties: false"}},
		{"OperationsResource", []string{"$ref: '#/components/schemas/OperationsResourceInput'", "unevaluatedProperties: false"}},
		{"MeetingDetail", []string{"$ref: '#/components/schemas/Meeting'", "required: [overrides, collectionIds]", "unevaluatedProperties: false"}},
		{"MeetingMutation", []string{"$ref: '#/components/schemas/Meeting'", "nextOccurrence:", "unevaluatedProperties: false"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			block := schemaBlock(document, test.name)
			for _, expected := range test.contains {
				if !strings.Contains(block, expected) {
					t.Errorf("schema %s missing %q:\n%s", test.name, expected, block)
				}
			}
		})
	}
	for _, fields := range []string{"ContentWriteFields", "CampaignScheduleFields", "ChurchUnitInput", "OperationsResourceInput", "MeetingInput", "MeetingOccurrenceOverrideInput", "Meeting"} {
		if block := schemaBlock(document, fields); strings.Contains(block, "additionalProperties: false") || strings.Contains(block, "unevaluatedProperties: false") {
			t.Errorf("shared schema %s closes properties before composition:\n%s", fields, block)
		}
	}
}

func TestOpenAPIDocumentsEngagementProxyParametersAndResponses(t *testing.T) {
	document := readOpenAPI(t)
	for _, operationID := range []string{"listCampaigns", "listCampaignDeliveries"} {
		block := operationByID(t, document, operationID)
		if !strings.Contains(block, "$ref: '#/components/parameters/PerPage'") || strings.Contains(block, "$ref: '#/components/parameters/PageSize'") {
			t.Errorf("%s must forward Engagement perPage, not Website pageSize:\n%s", operationID, block)
		}
	}

	tests := []struct {
		operationID, success string
		errors               []string
	}{
		{"listCampaigns", "'200': { $ref: '#/components/responses/CampaignPage' }", []string{"400"}},
		{"createCampaign", "'201': { $ref: '#/components/responses/Campaign' }", []string{"400", "409"}},
		{"getCampaign", "'200': { $ref: '#/components/responses/Campaign' }", []string{"400", "404"}},
		{"updateCampaign", "'200': { $ref: '#/components/responses/Campaign' }", []string{"400", "409"}},
		{"deleteCampaign", "'204': { description: Campaign draft deleted }", []string{"400", "409"}},
		{"sendCampaign", "'200': { $ref: '#/components/responses/Campaign' }", []string{"400", "404", "409"}},
		{"listCampaignDeliveries", "'200': { $ref: '#/components/responses/CampaignDeliveryPage' }", []string{"400", "404"}},
		{"retryFailedCampaignDeliveries", "'200': { $ref: '#/components/responses/Campaign' }", []string{"400", "404", "409"}},
		{"listCampaignSchedules", "'200': { $ref: '#/components/responses/CampaignScheduleList' }", nil},
		{"createCampaignSchedule", "'201': { $ref: '#/components/responses/CampaignSchedule' }", []string{"400"}},
		{"getCampaignSchedule", "'200': { $ref: '#/components/responses/CampaignSchedule' }", []string{"400", "404"}},
		{"updateCampaignSchedule", "'200': { $ref: '#/components/responses/CampaignSchedule' }", []string{"400", "404"}},
		{"deleteCampaignSchedule", "'204': { description: Campaign schedule deleted }", []string{"400", "404"}},
	}
	for _, test := range tests {
		t.Run(test.operationID, func(t *testing.T) {
			block := operationByID(t, document, test.operationID)
			for _, expected := range []string{
				test.success,
				"'401': { $ref: '#/components/responses/AdminUnauthorized' }",
				"'403': { $ref: '#/components/responses/AdminForbidden' }",
				"'500': { $ref: '#/components/responses/EngagementError' }",
				"'503': { $ref: '#/components/responses/EngagementUnavailable' }",
			} {
				if !strings.Contains(block, expected) {
					t.Errorf("%s missing %q:\n%s", test.operationID, expected, block)
				}
			}
			for _, status := range test.errors {
				expected := "'" + status + "': { $ref: '#/components/responses/EngagementError' }"
				if !strings.Contains(block, expected) {
					t.Errorf("%s missing %q:\n%s", test.operationID, expected, block)
				}
			}
		})
	}

	for _, schema := range []string{"CampaignEnvelope", "CampaignPageEnvelope", "CampaignDeliveryPageEnvelope", "CampaignScheduleEnvelope", "CampaignScheduleListEnvelope", "EngagementErrorEnvelope", "EngagementUnavailableEnvelope"} {
		if schemaBlock(document, schema) == "" {
			t.Errorf("missing proxy schema %s", schema)
		}
	}
}

func TestCIValidatesOpenAPIWithPinnedToolchain(t *testing.T) {
	contents, err := os.ReadFile(".github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(contents)
	for _, expected := range []string{
		"actions/setup-node@395ad3262231945c25e8478fd5baf05154b1d79f # v6.1.0",
		"node-version: 24",
		"npx --yes @redocly/cli@2.47.0 lint openapi.yaml",
	} {
		if !strings.Contains(workflow, expected) {
			t.Errorf("CI workflow missing %q", expected)
		}
	}
}

type catalogOperation struct {
	method, path, block string
}

var pathParameter = regexp.MustCompile(`\{[^}]+\}`)

func readOpenAPI(t *testing.T) string {
	t.Helper()
	contents, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

func contentWriteInputSchema(t *testing.T) *openapi3.Schema {
	t.Helper()
	loader := openapi3.NewLoader()
	document, err := loader.LoadFromFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	schema := document.Components.Schemas["ContentWriteInput"]
	if schema == nil || schema.Value == nil {
		t.Fatal("missing ContentWriteInput schema")
	}
	return schema.Value
}

func validateOpenAPIContentWriteInput(schema *openapi3.Schema, body []byte) error {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return err
	}
	return schema.VisitJSON(value, openapi3.EnableJSONSchema2020())
}

func validateCatalogContract(document string) error {
	if !strings.HasPrefix(document, "openapi: 3.1.") {
		return fmt.Errorf("document must use OpenAPI 3.1")
	}
	head, _, ok := strings.Cut(document, "paths:\n")
	if !ok {
		return fmt.Errorf("missing paths")
	}
	for _, metadata := range []string{
		"x-hhc-service: hhc-web-api\n",
		"x-hhc-owner: HHC Platform\n",
		"x-hhc-repository: HallelujahHomeChurch/hhc-web-api\n",
	} {
		if !strings.Contains(head, metadata) {
			return fmt.Errorf("missing root %s", strings.TrimSpace(strings.SplitN(metadata, ":", 2)[0]))
		}
	}
	if !strings.Contains(head, "servers:\n  - url: https://www.alive.org.tw/api\n") {
		return fmt.Errorf("root server must resolve Public operations through www.alive.org.tw/api")
	}

	operations := parseCatalogOperations(document)
	expected := expectedCatalogRoutes()
	documented := make(map[string]bool, len(operations))
	for _, operation := range operations {
		key := operation.method + " " + canonicalPath(operation.path)
		if documented[key] {
			return fmt.Errorf("duplicate documented route %s", key)
		}
		documented[key] = true
	}
	for _, route := range expected {
		if !documented[route] {
			return fmt.Errorf("missing documented route %s", route)
		}
	}
	for route := range documented {
		if !contains(expected, route) {
			return fmt.Errorf("unexpected documented route %s", route)
		}
	}

	visibilityTags := map[string]string{"public": "Public", "authenticated": "Authenticated", "admin": "Admin", "private": "Private", "operations": "Operations"}
	adminSecurity := "[{ daprApiToken: [], daprCallerAppId: [], trustedUserId: [], trustedAuthProvider: [], trustedScopes: [] }]"
	privateSecurity := "[{ daprApiToken: [], daprCallerAppId: [] }]"
	for _, operation := range operations {
		name := operation.method + " " + operation.path
		visibility, count := operationValue(operation.block, "x-hhc-visibility")
		if count != 1 || visibility == "" {
			return fmt.Errorf("%s missing x-hhc-visibility", name)
		}
		tag, count := operationValue(operation.block, "tags")
		if count != 1 || !strings.HasPrefix(tag, "[") || !strings.HasSuffix(tag, "]") || strings.Contains(tag, ",") {
			return fmt.Errorf("%s must have exactly one visibility tag", name)
		}
		tag = strings.TrimSpace(strings.Trim(tag, "[]"))
		expectedTag, ok := visibilityTags[visibility]
		if !ok {
			return fmt.Errorf("%s has unknown visibility %q", name, visibility)
		}
		if tag != expectedTag {
			return fmt.Errorf("%s tag %q does not match visibility %q", name, tag, visibility)
		}
		callers, count := operationValue(operation.block, "x-hhc-callers")
		if count != 1 || !strings.HasPrefix(callers, "[") || !strings.HasSuffix(callers, "]") {
			return fmt.Errorf("%s missing x-hhc-callers", name)
		}
		servers, serverCount := operationValue(operation.block, "servers")
		switch visibility {
		case "public":
			if serverCount != 0 {
				return fmt.Errorf("%s must inherit the Public server", name)
			}
		case "admin":
			if serverCount != 1 || servers != "[{ url: https://admin.alive.org.tw/api }]" {
				return fmt.Errorf("%s must resolve through admin.alive.org.tw/api", name)
			}
		case "authenticated":
			if serverCount != 0 {
				return fmt.Errorf("%s must inherit the authenticated gateway server", name)
			}
		case "private":
			if serverCount != 1 || servers != "[{ url: / }]" {
				return fmt.Errorf("%s must use a contract-relative direct-service server", name)
			}
		case "operations":
			if serverCount != 1 || servers != "[{ url: / }]" {
				return fmt.Errorf("%s must use a contract-relative direct-service server", name)
			}
			if callers != "[]" {
				return fmt.Errorf("%s must declare an empty application caller list", name)
			}
		}
		security, count := operationValue(operation.block, "security")
		if (visibility == "admin" || visibility == "authenticated") && (count != 1 || security != adminSecurity) {
			return fmt.Errorf("%s must document trusted gateway security", name)
		}
		if visibility == "private" && (count != 1 || security != privateSecurity) {
			return fmt.Errorf("%s must document service caller security", name)
		}
		if visibility != "admin" && visibility != "authenticated" && visibility != "private" && (count != 1 || security != "[]") {
			return fmt.Errorf("%s must document its unauthenticated service boundary", name)
		}
		if visibility == "admin" || visibility == "authenticated" {
			for status, response := range map[string]string{"401": "AdminUnauthorized", "403": "AdminForbidden"} {
				if !strings.Contains(operation.block, "'"+status+"': { $ref: '#/components/responses/"+response+"' }") {
					return fmt.Errorf("%s missing trusted Admin %s response", name, status)
				}
			}
		}
	}
	return nil
}

func parseCatalogOperations(document string) []catalogOperation {
	lines := strings.Split(document, "\n")
	operations := make([]catalogOperation, 0, 64)
	path := ""
	methods := map[string]bool{"get": true, "post": true, "put": true, "delete": true, "patch": true}
	for index, line := range lines {
		if strings.HasPrefix(line, "  /") && strings.HasSuffix(line, ":") {
			path = strings.TrimSuffix(strings.TrimSpace(line), ":")
			continue
		}
		trimmed := strings.TrimSpace(line)
		method := strings.TrimSuffix(trimmed, ":")
		if path == "" || !methods[method] || !strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "     ") {
			continue
		}
		end := index + 1
		for end < len(lines) {
			next := lines[end]
			if next != "" && len(next)-len(strings.TrimLeft(next, " ")) <= 4 {
				break
			}
			end++
		}
		operations = append(operations, catalogOperation{strings.ToUpper(method), path, strings.Join(lines[index+1:end], "\n")})
	}
	return operations
}

func operationValue(block, key string) (string, int) {
	prefix := "      " + key + ":"
	value, count := "", 0
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, prefix) {
			value = strings.TrimSpace(strings.TrimPrefix(line, prefix))
			count++
		}
	}
	return value, count
}

func operationByID(t *testing.T, document, operationID string) string {
	t.Helper()
	for _, operation := range parseCatalogOperations(document) {
		if value, count := operationValue(operation.block, "operationId"); count == 1 && value == operationID {
			return operation.block
		}
	}
	t.Fatalf("missing operationId %s", operationID)
	return ""
}

func schemaBlock(document, name string) string {
	_, schemas, ok := strings.Cut(document, "\n  schemas:\n")
	if !ok {
		return ""
	}
	lines := strings.Split(schemas, "\n")
	marker := "    " + name + ":"
	for start, line := range lines {
		if line != marker {
			continue
		}
		end := start + 1
		for end < len(lines) && (!strings.HasPrefix(lines[end], "    ") || strings.HasPrefix(lines[end], "     ") || lines[end] == "") {
			end++
		}
		return strings.Join(lines[start:end], "\n")
	}
	return ""
}

func responseBlock(document, name string) string {
	_, responses, ok := strings.Cut(document, "\n  responses:\n")
	if !ok {
		return ""
	}
	responses, _, _ = strings.Cut(responses, "\n  headers:\n")
	lines := strings.Split(responses, "\n")
	marker := "    " + name + ":"
	for start, line := range lines {
		if line != marker {
			continue
		}
		end := start + 1
		for end < len(lines) && (!strings.HasPrefix(lines[end], "    ") || strings.HasPrefix(lines[end], "     ") || lines[end] == "") {
			end++
		}
		return strings.Join(lines[start:end], "\n")
	}
	return ""
}

func openAPIValidAboutPagePayload() []byte {
	return []byte(`{"schemaVersion":1,"template":"about.v1","data":{"heroTitle":"About","heroSubtitle":"Mission","vision":{"intro":"Intro","imageAlt":"Image","actionsImageAlt":"Actions","sections":[{"eyebrow":"One","title":"Vision","body":"Body"},{"eyebrow":"Two","title":"Goals","body":"Body"},{"eyebrow":"Three","title":"Actions","cards":[{"title":"Share","body":"Body"}]},{"eyebrow":"Four","title":"Convictions","cards":[{"title":"Mission","body":"Body"}]}]},"history":{"scripture":[{"lines":["Verse"],"cite":"Isaiah"}],"imageAlt":"Image","intro":"History","title":"Church History"}}}`)
}

func openAPIValidHomePagePayload() []byte {
	return []byte(`{"schemaVersion":1,"template":"home.v1","data":{"heroTitle":"Home","heroSubtitle":"Welcome","newsTitle":"News","moreNews":"More","weeklyTitle":"Weekly","downloadWeekly":"Download","videosTitle":"Videos","videosSubtitle":"Music","watchMore":"Watch","aboutTitle":"About","aboutBody":"About us","aboutCta":"Meet us","locationsTitle":"Locations","mapLink":"Map"}}`)
}

func openAPIValidLegalPagePayload() []byte {
	return []byte(`{"schemaVersion":1,"template":"legal.v1","data":{"heroTitle":"Privacy","heroSubtitle":"","updatedAtLabel":"Updated","updatedAt":"August 10, 2026","intro":"Intro","sections":[{"title":"Section","body":["Paragraph"]}]}}`)
}

func openAPIValidHomeV2WriteInput() []byte {
	return []byte(`{"pageKey":"home","pageTemplate":"home.v2","routePath":"/","indexable":true,"bannerAssetId":"banner-1","links":{"churchYoutube":"https://youtube.com/@hhc","churchFacebook":"https://facebook.com/hhc","musicYoutube":"https://youtube.com/@music"},"locations":[{"key":"taipei","mapHref":"https://maps.example.com/taipei","sortOrder":10,"translations":[{"locale":"zh-Hant","name":"台北","address":"地址"},{"locale":"zh-Hans","name":"台北","address":"地址"},{"locale":"en","name":"Taipei","address":"Address"},{"locale":"ja","name":"台北","address":"住所"},{"locale":"ko","name":"타이베이","address":"주소"}]}],"translations":[{"locale":"zh-Hant","bodyJson":{"schemaVersion":2,"template":"home.v2","data":{"heroTitle":"Home","heroSubtitle":"Welcome","kingdomJoyDescription":"Kingdom joy","aboutDescription":"About us"}}},{"locale":"zh-Hans","bodyJson":{"schemaVersion":2,"template":"home.v2","data":{"heroTitle":"Home","heroSubtitle":"Welcome","kingdomJoyDescription":"Kingdom joy","aboutDescription":"About us"}}},{"locale":"en","bodyJson":{"schemaVersion":2,"template":"home.v2","data":{"heroTitle":"Home","heroSubtitle":"Welcome","kingdomJoyDescription":"Kingdom joy","aboutDescription":"About us"}}},{"locale":"ja","bodyJson":{"schemaVersion":2,"template":"home.v2","data":{"heroTitle":"Home","heroSubtitle":"Welcome","kingdomJoyDescription":"Kingdom joy","aboutDescription":"About us"}}},{"locale":"ko","bodyJson":{"schemaVersion":2,"template":"home.v2","data":{"heroTitle":"Home","heroSubtitle":"Welcome","kingdomJoyDescription":"Kingdom joy","aboutDescription":"About us"}}}]}`)
}

func canonicalPath(path string) string {
	return pathParameter.ReplaceAllString(path, "{}")
}

func expectedCatalogRoutes() []string {
	fields := strings.Fields(`
		GET /health
		GET /health/live
		GET /ready
		GET /health/ready
		GET /bulletins
		GET /bulletins/latest
		GET /bulletins/by-number/{}
		GET /bulletins/{}
		GET /news
		GET /news/{}
		GET /history
		GET /videos
		GET /locations
		GET /pages/{}
		GET /site-layout
		GET /home
		GET /meetings
		GET /meetings/{}
		GET /meeting-occurrences
		GET /meeting-sync-windows
		GET /admin/operations/church-units
		POST /admin/operations/church-units
		GET /admin/operations/church-units/{}
		PUT /admin/operations/church-units/{}
		POST /admin/operations/church-units/{}/{}
		GET /admin/operations/resources
		POST /admin/operations/resources
		GET /admin/operations/resources/{}
		PUT /admin/operations/resources/{}
		POST /admin/operations/resources/{}/{}
		GET /admin/operations/meetings
		POST /admin/operations/meetings
		GET /admin/operations/meetings/{}
		PUT /admin/operations/meetings/{}
		POST /admin/operations/meetings/{}/{}
		PUT /admin/operations/meetings/{}/overrides/{}
		DELETE /admin/operations/meetings/{}/overrides/{}
		PUT /admin/operations/meetings/{}/collections
		GET /priv/meeting-occurrences
		GET /priv/meeting-sync-windows
		GET /admin/site-settings
		PUT /admin/site-settings
		POST /admin/site-settings/publish
		POST /admin/site-settings/unpublish
		GET /admin/site-settings/revisions
		POST /admin/site-settings/revisions/{}/restore
		GET /admin/bulletins
		POST /admin/bulletins
		GET /admin/bulletins/{}
		PUT /admin/bulletins/{}
		DELETE /admin/bulletins/{}
		POST /admin/bulletins/{}/upload-sessions
		PUT /admin/bulletins/{}/versions/{}
		DELETE /admin/bulletins/{}/versions/{}
		GET /admin/bulletins/{}/assets/{}
		POST /admin/bulletins/{}/assets/{}/scan/retry
		POST /admin/bulletins/{}/assets/{}/complete
		POST /admin/bulletins/{}/publish
		POST /admin/bulletins/{}/unpublish
		GET /admin/bulletins/{}/revisions
		POST /admin/bulletins/{}/revisions/{}/restore
		POST /admin/bulletins/{}/translation-previews/{}
		GET /admin/content/{}
		POST /admin/content/{}
		GET /admin/content/{}/{}
		PUT /admin/content/{}/{}
		DELETE /admin/content/{}/{}
		POST /admin/content/{}/{}/translation-previews/{}
		POST /admin/content/{}/{}/publish
		POST /admin/content/{}/{}/unpublish
		GET /admin/content/{}/{}/revisions
		POST /admin/content/{}/{}/revisions/{}/restore
		POST /admin/content/news/{}/upload-sessions
		GET /admin/content/news/{}/assets/{}
		POST /admin/content/news/{}/assets/{}/scan/retry
		POST /admin/content/news/{}/assets/{}/complete
		POST /admin/content/pages/{}/upload-sessions
		GET /admin/content/pages/{}/assets/{}
		POST /admin/content/pages/{}/assets/{}/scan/retry
		POST /admin/content/pages/{}/assets/{}/complete
		GET /admin/campaigns
		POST /admin/campaigns
		GET /admin/campaigns/{}
		PUT /admin/campaigns/{}
		DELETE /admin/campaigns/{}
		POST /admin/campaigns/{}/send
		POST /admin/campaigns/{}/translation-previews/{}
		GET /admin/campaigns/{}/deliveries
		POST /admin/campaigns/{}/retry-failed
		GET /admin/campaign-schedules
		POST /admin/campaign-schedules
		GET /admin/campaign-schedules/{}
		PUT /admin/campaign-schedules/{}
		DELETE /admin/campaign-schedules/{}
		POST /admin/campaign-schedules/{}/translation-previews/{}
	`)
	routes := make([]string, 0, len(fields)/2)
	for index := 0; index < len(fields); index += 2 {
		routes = append(routes, fields[index]+" "+fields[index+1])
	}
	return routes
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
