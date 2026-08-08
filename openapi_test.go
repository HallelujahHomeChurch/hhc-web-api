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
		"$ref: '#/components/parameters/Locale'",
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
