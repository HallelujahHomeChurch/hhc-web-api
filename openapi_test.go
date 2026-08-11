package hhcwebapi

import (
	"os"
	"strings"
	"testing"
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
		"required: [sourceLocale, targetLocale, sourceVersion, translation]",
		"invalid_translation_request",
		"translation_exists",
		"version_mismatch",
		"translation_rate_limited",
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
