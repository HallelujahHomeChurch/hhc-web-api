package hhcwebapi

import (
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sort"
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
	want := []string{"Admin", "Operations", "Public"}
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

func TestOpenAPIComposedSchemasRemainSatisfiable(t *testing.T) {
	document := readOpenAPI(t)
	tests := []struct {
		name     string
		contains []string
	}{
		{"ContentWriteInput", []string{"allOf:", "$ref: '#/components/schemas/ContentWriteFields'", "unevaluatedProperties: false"}},
		{"ContentItem", []string{"$ref: '#/components/schemas/ContentWriteFields'", "required: [id, module, status, version, isPublished, createdBy, updatedBy, createdAt, updatedAt]", "unevaluatedProperties: false"}},
		{"CreateCampaignSchedule", []string{"$ref: '#/components/schemas/CampaignScheduleFields'", "unevaluatedProperties: false"}},
		{"UpdateCampaignSchedule", []string{"$ref: '#/components/schemas/CampaignScheduleFields'", "required: [enabled]", "enabled: { type: boolean }", "unevaluatedProperties: false"}},
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
	for _, fields := range []string{"ContentWriteFields", "CampaignScheduleFields"} {
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

	visibilityTags := map[string]string{"public": "Public", "admin": "Admin", "private": "Private", "operations": "Operations"}
	adminSecurity := "[{ daprApiToken: [], daprCallerAppId: [], trustedUserId: [], trustedAuthProvider: [], trustedScopes: [] }]"
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
		case "operations":
			if serverCount != 1 || servers != "[{ url: / }]" {
				return fmt.Errorf("%s must use a contract-relative direct-service server", name)
			}
			if callers != "[]" {
				return fmt.Errorf("%s must declare an empty application caller list", name)
			}
		}
		security, count := operationValue(operation.block, "security")
		if visibility == "admin" && (count != 1 || security != adminSecurity) {
			return fmt.Errorf("%s must document trusted gateway security", name)
		}
		if visibility != "admin" && (count != 1 || security != "[]") {
			return fmt.Errorf("%s must document its unauthenticated service boundary", name)
		}
		if visibility == "admin" {
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
		GET /home
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
