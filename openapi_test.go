package hhcwebapi

import (
	"encoding/json"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/content"
	"github.com/getkin/kin-openapi/openapi3"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
)

func TestOnlineSourcePreviewContract(t *testing.T) {
	doc, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	path := doc.Paths.Find("/admin/bulletins/{issueId}/online/{series}/{contentLocale}/source-pdf")
	if path == nil || path.Get == nil {
		t.Fatal("missing source preview operation")
	}
	op := path.Get
	if op.OperationID != "getOnlineBulletinSourcePDF" || len(op.Parameters) != 3 {
		t.Fatal("invalid preview contract")
	}
	for _, status := range []string{"200", "401", "403", "404", "503"} {
		if op.Responses.Value(status) == nil {
			t.Fatalf("missing %s response", status)
		}
	}
	response := op.Responses.Value("200").Value
	if response.Content.Get("application/pdf") == nil || response.Headers["X-HHC-Source-SHA256"] == nil || response.Headers["Cache-Control"] == nil {
		t.Fatal("unbound or cacheable preview response")
	}
}

func TestReaderRangeMutationContract(t *testing.T) {
	schema := compileOpenAPISchema(t, "ReaderMutation")
	input := map[string]any{"mutationId": "a59c90d3-468f-4014-b59e-21865836ed7b", "createdAt": "2026-10-03T00:00:00Z", "documentRevision": 3, "kind": "setHighlight", "payload": map[string]any{"sentenceIds": []any{"a"}, "color": "yellow", "ranges": []any{map[string]any{"sentenceId": "a", "start": 1, "end": 3}}}}
	if err := schema.Validate(input); err != nil {
		t.Fatal("partial highlight rejected", err)
	}
	payload := input["payload"].(map[string]any)
	payload["ranges"] = []any{}
	if schema.Validate(input) == nil {
		t.Fatal("empty ranges accepted")
	}
	payload["ranges"] = []any{map[string]any{"sentenceId": "a", "start": -1, "end": 3}}
	if schema.Validate(input) == nil {
		t.Fatal("negative range accepted")
	}
	delete(payload, "ranges")
	if err := schema.Validate(input); err != nil {
		t.Fatal("legacy mutation rejected", err)
	}
}

func TestReaderHighlightHistoryContract(t *testing.T) {
	schema := compileOpenAPISchema(t, "ReaderHighlightHistoryEntry")
	input := map[string]any{"id": "a59c90d3-468f-4014-b59e-21865836ed7b", "highlight": map[string]any{"sentenceId": "a", "color": "yellow", "quote": "Previous wording", "active": false, "version": 2, "updatedAt": "2026-10-03T00:00:00Z", "segments": []any{map[string]any{"start": 1, "end": 3, "color": "yellow"}}}}
	if err := schema.Validate(input); err != nil {
		t.Fatal("private history rejected", err)
	}
	delete(input, "highlight")
	if err := schema.Validate(input); err != nil {
		t.Fatal("consumed-entry tombstone rejected", err)
	}
}

func TestReaderHighlightHistoryMutationContract(t *testing.T) {
	schema := compileOpenAPISchema(t, "ReaderMutation")
	payload := map[string]any{"historyId": "a59c90d3-468f-4014-b59e-21865836ed7b", "sentenceIds": []any{"a"}, "ranges": []any{map[string]any{"sentenceId": "a", "start": 0, "end": 2}}, "color": "yellow"}
	input := map[string]any{"mutationId": "a59c90d3-468f-4014-b59e-21865836ed7b", "createdAt": "2026-10-03T00:00:00Z", "documentRevision": 3, "kind": "restoreHighlight", "payload": payload}
	if err := schema.Validate(input); err != nil {
		t.Fatal(err)
	}
	delete(payload, "ranges")
	if schema.Validate(input) == nil {
		t.Fatal("implicit whole-sentence restore accepted")
	}
	input["kind"] = "discardHighlightHistory"
	input["payload"] = map[string]any{"historyId": payload["historyId"]}
	if err := schema.Validate(input); err != nil {
		t.Fatal(err)
	}
	input["payload"].(map[string]any)["color"] = "yellow"
	if schema.Validate(input) == nil {
		t.Fatal("extra discard payload accepted")
	}
}

func TestReaderReanchorNoteContract(t *testing.T) {
	schema := compileOpenAPISchema(t, "ReaderMutation")
	payload := map[string]any{"noteId": "28449e78-9938-4b80-811a-dffb6e44ed9e", "sentenceIds": []any{"a"}, "ranges": []any{map[string]any{"sentenceId": "a", "start": 1, "end": 3}}}
	input := map[string]any{"mutationId": "a59c90d3-468f-4014-b59e-21865836ed7b", "createdAt": "2026-10-03T00:00:00Z", "documentRevision": 3, "baseVersion": 2, "kind": "reanchorNote", "payload": payload}
	if err := schema.Validate(input); err != nil {
		t.Fatal("valid reanchor rejected", err)
	}
	delete(input, "baseVersion")
	if schema.Validate(input) == nil {
		t.Fatal("unversioned reanchor accepted")
	}
	input["baseVersion"] = 2
	delete(payload, "ranges")
	if schema.Validate(input) == nil {
		t.Fatal("implicit whole-sentence reanchor accepted")
	}
}

func TestOnlineSourcePageLimitContract(t *testing.T) {
	doc, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"OnlineBulletinDocument", "OnlineBulletinLayoutManifest"} {
		pages := doc.Components.Schemas[name].Value.Properties["pages"].Value
		if pages.MaxItems == nil || *pages.MaxItems != 40 {
			t.Fatalf("%s must retain source page limit", name)
		}
	}
}

func TestOnlineAdminStateContract(t *testing.T) {
	doc, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	path := doc.Paths.Find("/admin/bulletins/{issueId}/online/{series}/{contentLocale}")
	if path == nil || path.Get == nil || path.Get.OperationID != "getOnlineBulletinState" {
		t.Fatal("missing Online state operation")
	}
	for _, code := range []string{"200", "400", "401", "403", "404", "503"} {
		if path.Get.Responses.Value(code) == nil {
			t.Fatalf("missing %s", code)
		}
	}
	if path.Get.Responses.Value("200").Value.Headers["ETag"] == nil {
		t.Fatal("missing Online CAS version")
	}
}

func TestRecordingProcessingProgressContract(t *testing.T) {
	now := "2026-10-06T00:00:00Z"
	progress := map[string]any{"attempt": 1, "phase": "package_validation", "objectsVerified": 0, "attemptStartedAt": now, "phaseStartedAt": now, "lastProgressAt": now, "heartbeatAt": now}
	schema := compileOpenAPISchema(t, "RecordingProcessingProgress")
	if err := schema.Validate(progress); err != nil {
		t.Fatal(err)
	}
	progress["phase"] = "ready"
	if schema.Validate(progress) == nil {
		t.Fatal("ready accepted as advisory phase")
	}
	progress["phase"] = "package_validation"
	progress["url"] = "https://capability.invalid"
	if schema.Validate(progress) == nil {
		t.Fatal("capability permitted in summary")
	}
	doc, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	if doc.Components.Schemas["MemberRecording"].Value.Properties["processingProgress"] != nil {
		t.Fatal("member projection exposes progress")
	}
	if doc.Components.Schemas["AdminRecording"].Value.Properties["processingProgress"] == nil {
		t.Fatal("admin summary missing")
	}
}

func TestOnlineConversionContract(t *testing.T) {
	doc, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	path := doc.Paths.Find("/admin/bulletins/{issueId}/online/{series}/{contentLocale}/conversions")
	if path == nil || path.Post == nil || path.Post.OperationID != "startOnlineBulletinConversion" {
		t.Fatal("missing conversion operation")
	}
	for _, code := range []string{"200", "202", "400", "401", "403", "404", "409", "428", "503"} {
		if path.Post.Responses.Value(code) == nil {
			t.Fatalf("missing %s", code)
		}
	}
	if len(path.Post.Parameters) != 4 || path.Post.RequestBody == nil {
		t.Fatal("missing exact edition and CAS")
	}
	schema := compileOpenAPISchema(t, "OnlineBulletinConversionInput")
	value := map[string]any{"canonicalVersion": 5, "sourceOnlineVersion": 3, "sourceRevision": 2}
	if err = schema.Validate(value); err != nil {
		t.Fatal(err)
	}
	value["sourceAssetId"] = "forged"
	if schema.Validate(value) == nil {
		t.Fatal("caller can choose source")
	}
}

func TestOnlineExtractionContract(t *testing.T) {
	doc, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	versions := doc.Components.Schemas["OnlineBulletinLayoutManifest"].Value.Properties["rendererVersion"].Value.Enum
	if fmt.Sprint(versions) != "[v1 v2 v3 v4 v5 v6 v7 v8 v9 v10 v11]" {
		t.Fatalf("adaptive renderer contract missing: %v", versions)
	}
	path := doc.Paths.Find("/admin/bulletins/{issueId}/online/{series}/{contentLocale}/extractions")
	if path == nil || path.Post == nil || path.Post.OperationID != "startOnlineBulletinExtraction" {
		t.Fatal("missing extraction operation")
	}
	for _, code := range []string{"200", "202", "400", "401", "403", "404", "409", "428", "503"} {
		if path.Post.Responses.Value(code) == nil {
			t.Fatalf("missing %s", code)
		}
	}
	if path.Post.RequestBody == nil || len(path.Post.Parameters) != 4 {
		t.Fatal("missing source-free input or CAS")
	}
	schema := compileOpenAPISchema(t, "OnlineBulletinExtractionInput")
	value := map[string]any{"canonicalVersion": 5, "retry": false}
	if err = schema.Validate(value); err != nil {
		t.Fatal(err)
	}
	value["sourceAssetId"] = "attacker"
	if schema.Validate(value) == nil {
		t.Fatal("caller can choose extraction source")
	}
}

func TestHistoricalCoverOpenAPIContract(t *testing.T) {
	doc, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	elements := doc.Components.Schemas["OnlineBulletinFixedSlot"].Value.Properties["element"].Value.Enum
	for _, want := range []string{"historicalVision", "historicalGoals", "historicalGospelGoals", "historicalActions", "historicalCommitment", "visionMission", "visionFellowship", "visionCommitment"} {
		found := false
		for _, value := range elements {
			found = found || value == want
		}
		if !found {
			t.Fatal("missing fixed element", want)
		}
	}
}

func TestOnlineComparisonContract(t *testing.T) {
	doc, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"comparison", "compare"} {
		path := doc.Paths.Find("/admin/bulletins/{issueId}/online/{series}/{contentLocale}/" + name)
		if path == nil || (name == "comparison" && path.Get == nil) || (name == "compare" && path.Post == nil) {
			t.Fatal("missing comparison operation", name)
		}
	}
	schema := compileOpenAPISchema(t, "OnlineBulletinCompareInput")
	value := map[string]any{"canonicalVersion": 1, "baseRevision": 1, "localRevision": 2, "incomingRevision": 3, "choices": []any{}}
	if err = schema.Validate(value); err != nil {
		t.Fatal(err)
	}
	value["layoutManifest"] = map[string]any{}
	if schema.Validate(value) == nil {
		t.Fatal("client layout accepted")
	}
}

func TestOnlineDraftSaveContract(t *testing.T) {
	doc, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	path := doc.Paths.Find("/admin/bulletins/{issueId}/online/{series}/{contentLocale}/draft")
	if path == nil || path.Put == nil || path.Put.OperationID != "saveOnlineBulletinDraft" {
		t.Fatal("missing draft save")
	}
	for _, code := range []string{"200", "400", "401", "403", "404", "409", "428", "503"} {
		if path.Put.Responses.Value(code) == nil {
			t.Fatalf("missing %s", code)
		}
	}
	schema := compileOpenAPISchema(t, "OnlineBulletinDraftInput")
	if schema.Validate(map[string]any{"canonicalVersion": 1, "components": []any{}, "layoutManifest": map[string]any{}}) == nil {
		t.Fatal("client layout proof accepted")
	}
}

func TestOnlineConfirmationContract(t *testing.T) {
	doc, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	path := doc.Paths.Find("/admin/bulletins/{issueId}/online/{series}/{contentLocale}/confirm")
	if path == nil || path.Post == nil || path.Post.OperationID != "confirmOnlineBulletin" {
		t.Fatal("missing confirmation")
	}
	for _, code := range []string{"200", "400", "401", "403", "404", "409", "428", "503"} {
		if path.Post.Responses.Value(code) == nil {
			t.Fatalf("missing %s", code)
		}
	}
	schema := compileOpenAPISchema(t, "OnlineBulletinConfirmInput")
	value := map[string]any{"canonicalVersion": 1, "revision": 2, "pageCount": 14, "layoutValidationHash": strings.Repeat("a", 64), "acceptedIssues": []any{}}
	if err = schema.Validate(value); err != nil {
		t.Fatal(err)
	}
	value["validated"] = true
	if schema.Validate(value) == nil {
		t.Fatal("client pass accepted")
	}
}

func TestOnlineRestoreContract(t *testing.T) {
	doc, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	path := doc.Paths.Find("/admin/bulletins/{issueId}/online/{series}/{contentLocale}/revisions/{revision}/restore")
	if path == nil || path.Post == nil || path.Post.OperationID != "restoreOnlineBulletinRevision" {
		t.Fatal("missing Online restore")
	}
	scopes, _ := json.Marshal(path.Post.Extensions["x-required-scopes"])
	if string(scopes) != `["cms:bulletins:publish"]` {
		t.Fatal("restore requires publish scope", string(scopes))
	}
	for _, code := range []string{"200", "400", "401", "403", "404", "409", "428", "503"} {
		if path.Post.Responses.Value(code) == nil {
			t.Fatalf("missing %s", code)
		}
	}
	schema := compileOpenAPISchema(t, "OnlineBulletinRestoreInput")
	value := map[string]any{"canonicalVersion": 1}
	if err = schema.Validate(value); err != nil {
		t.Fatal(err)
	}
	value["document"] = map[string]any{}
	if schema.Validate(value) == nil {
		t.Fatal("caller can replace history")
	}
}

func TestOnlinePublicationContract(t *testing.T) {
	doc, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"publish", "unpublish"} {
		path := doc.Paths.Find("/admin/bulletins/{issueId}/online/{series}/{contentLocale}/" + action)
		if path == nil || path.Post == nil {
			t.Fatal("missing Online", action)
		}
		for _, code := range []string{"200", "400", "401", "403", "404", "409", "428", "503"} {
			if path.Post.Responses.Value(code) == nil {
				t.Fatal("missing response", code)
			}
		}
		scopes, _ := json.Marshal(path.Post.Extensions["x-required-scopes"])
		if string(scopes) != `["cms:bulletins:publish"]` {
			t.Fatal("publication scope", string(scopes))
		}
	}
	schema := compileOpenAPISchema(t, "OnlineBulletinPublishInput")
	value := map[string]any{"canonicalVersion": 1, "revision": 2, "mappings": []any{map[string]any{"fromSentenceId": "old", "status": "split", "toSentenceIds": []any{"a", "b"}}}}
	if err = schema.Validate(value); err != nil {
		t.Fatal(err)
	}
	value["passed"] = true
	if schema.Validate(value) == nil {
		t.Fatal("client proof accepted")
	}
}

func TestBulletinUnpublishSelectionContract(t *testing.T) {
	schema := compileOpenAPISchema(t, "BulletinUnpublishInput")
	for _, tc := range []struct {
		value map[string]any
		valid bool
	}{
		{map[string]any{"locale": "en"}, true},
		{map[string]any{"locale": "zh-Hant", "unpublishOnline": true, "onlineVersion": 3}, true},
		{map[string]any{"locale": "zh-Hant", "unpublishOnline": true}, false},
		{map[string]any{"locale": "zh-Hant", "onlineVersion": 3}, false},
		{map[string]any{"locale": "en", "unpublishOnline": true, "onlineVersion": 3}, false},
		{map[string]any{"series": "children", "locale": "zh-Hant", "unpublishOnline": true, "onlineVersion": 3}, false},
	} {
		if (schema.Validate(tc.value) == nil) != tc.valid {
			t.Fatalf("schema decision %+v", tc)
		}
	}
}

func TestOnlineHistoryContract(t *testing.T) {
	doc, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	for suffix, operation := range map[string]string{"/revisions": "listOnlineBulletinRevisions", "/revisions/{revision}": "getOnlineBulletinRevision"} {
		path := doc.Paths.Find("/admin/bulletins/{issueId}/online/{series}/{contentLocale}" + suffix)
		if path == nil || path.Get == nil || path.Get.OperationID != operation {
			t.Fatal("missing history operation", operation)
		}
		for _, code := range []string{"200", "400", "401", "403", "404", "503"} {
			if path.Get.Responses.Value(code) == nil {
				t.Fatalf("missing %s", code)
			}
		}
	}
	schema := compileOpenAPISchema(t, "OnlineBulletinRevisionPage")
	value := map[string]any{"documentId": "018f47d2-e5d1-4f3f-8f18-6c8e32621f74", "items": []any{}, "nextBefore": nil}
	if err = schema.Validate(value); err != nil {
		t.Fatal(err)
	}
	value["sourceAssetId"] = "private"
	if schema.Validate(value) == nil {
		t.Fatal("history permits source assets")
	}
}

func TestOpenAPIRecordingLadderSupportsThreeRenditions(t *testing.T) {
	document, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"RecordingPackageInventory", "RecordingPackageStatus"} {
		rungs := document.Components.Schemas[name].Value.Properties["renditions"].Value
		if rungs.MaxItems == nil || *rungs.MaxItems != 3 {
			t.Fatalf("%s must allow three renditions", name)
		}
	}
	values := document.Components.Schemas["RecordingRendition"].Value.Properties["name"].Value.Enum
	if !reflect.DeepEqual(values, []any{"480p", "720p", "1080p"}) {
		t.Fatalf("unexpected ladder: %v", values)
	}
}

func TestOpenAPIServicePrincipalSecurityIsRecordingOnly(t *testing.T) {
	document := readOpenAPI(t)
	allowed := map[string]bool{
		"listAdminRecordings": true, "createAdminRecording": true,
		"getAdminRecording": true, "updateAdminRecordingTitle": true,
		"setAdminRecordingExposure": true, "publishAdminRecording": true,
		"unpublishAdminRecording": true,
		"createRecordingPackage":  true, "getRecordingPackage": true,
		"signRecordingPackage": true, "completeRecordingPackage": true,
		"listRecordingCovers": true, "uploadRecordingCover": true, "setRecordingCover": true, "getRecordingCoverContent": true,
	}
	seen := 0
	for _, operation := range parseCatalogOperations(document) {
		id, _ := operationValue(operation.block, "operationId")
		security, _ := operationValue(operation.block, "security")
		service := strings.Contains(security, "trustedPrincipalId:")
		if service != allowed[id] {
			t.Fatalf("%s service-principal security = %v", id, service)
		}
		if service {
			seen++
			for _, scheme := range []string{"trustedPrincipalType", "trustedClientId", "trustedCredentialId"} {
				if !strings.Contains(security, scheme+": []") {
					t.Fatalf("%s omits %s", id, scheme)
				}
			}
		}
	}
	if seen != len(allowed) {
		t.Fatalf("documented %d service operations, want %d", seen, len(allowed))
	}
}

func TestOpenAPIBrowserSourceSeparatesSourceBudgetAndBlockPaging(t *testing.T) {
	input := compileOpenAPISchema(t, "RecordingSourceInput")
	value := map[string]any{"fileName": "source.mp4", "sizeBytes": float64(50_000_000_000), "checksumSHA256": strings.Repeat("a", 64)}
	if err := input.Validate(value); err != nil {
		t.Fatal(err)
	}
	value["sizeBytes"] = float64(50_000_000_001)
	if err := input.Validate(value); err == nil {
		t.Fatal("source cap not enforced")
	}
	sign := compileOpenAPISchema(t, "RecordingSourceSignInput")
	if err := sign.Validate(map[string]any{"numbers": []any{float64(2981)}}); err != nil {
		t.Fatal(err)
	}
	if err := sign.Validate(map[string]any{"numbers": []any{float64(2982)}}); err == nil {
		t.Fatal("out of range block")
	}
	if err := sign.Validate(map[string]any{"numbers": []any{float64(1), float64(1)}}); err == nil {
		t.Fatal("duplicate block")
	}
	status := compileOpenAPISchema(t, "RecordingSourceStatus")
	value = map[string]any{"sourceId": strings.Repeat("a", 32), "recordingId": "0199a000-0000-4000-8000-000000000003", "fileName": "source.mp4", "sizeBytes": float64(50_000_000_000), "checksumSHA256": strings.Repeat("a", 64), "blockCount": float64(2981), "state": "finalizing", "createdAt": "2026-10-01T00:00:00Z", "expiresAt": "2026-10-02T00:00:00Z", "confirmedBlocks": []any{float64(1001)}, "nextCursor": float64(1001)}
	if err := status.Validate(value); err != nil {
		t.Fatal(err)
	}
	value["state"] = "published"
	if err := status.Validate(value); err == nil {
		t.Fatal("source status implies publication")
	}
}

func TestOpenAPIDocumentsProtectedBulletinRoutes(t *testing.T) {
	contents, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	document := string(contents)
	start := strings.Index(document, "  /member/bulletins:")
	end := strings.Index(document[start+1:], "\n  /")
	if start < 0 || end < 0 {
		t.Fatal("missing /member/bulletins path")
	}
	operation := document[start : start+1+end]
	for _, expected := range []string{
		"operationId: listProtectedBulletins",
		"x-hhc-callers: [api-gateway, hhc-line-function-bot]",
		"name: issueNumber",
		"type: integer",
		"minimum: 1",
		"maximum: 2147483647",
		"$ref: '#/components/parameters/BulletinSeries'",
		"$ref: '#/components/parameters/BulletinLocale'",
		"'404': { $ref: '#/components/responses/ProtectedBulletinNotFound' }",
	} {
		if !strings.Contains(operation, expected) {
			t.Fatalf("operation missing %q:\n%s", expected, operation)
		}
	}
	for _, forbidden := range []string{"bulletin:read", "x-required-scopes", "PublicBulletin"} {
		if strings.Contains(operation, forbidden) {
			t.Fatalf("protected operation retains %q:\n%s", forbidden, operation)
		}
	}
	for _, expected := range []string{
		"/member/bulletins/latest:",
		"/member/bulletins/{issueID}/versions/{locale}:",
		"/member/bulletins/{issueID}/versions/{locale}/download:",
		"operationId: downloadProtectedBulletinVersion",
		"x-hhc-cache: 'private, no-store'",
	} {
		if !strings.Contains(document, expected) {
			t.Fatalf("OpenAPI document missing %q", expected)
		}
	}
}

func TestOpenAPIDocumentsHybridBulletinInvestigationContract(t *testing.T) {
	document := readOpenAPI(t)
	create := operationByID(t, document, "createBulletinWatermarkInvestigation")
	for _, expected := range []string{"minItems: 1", "maxItems: 5", "format: binary"} {
		if !strings.Contains(create, expected) {
			t.Fatalf("create investigation missing %q:\n%s", expected, create)
		}
	}
	input := schemaBlock(document, "BulletinWatermarkInvestigationInput")
	if !strings.Contains(input, "required: [issueId]") || strings.Contains(input, "locale:") {
		t.Fatalf("new investigation input must require only issueId:\n%s", input)
	}
	global := operationByID(t, document, "listGlobalBulletinWatermarkInvestigations")
	for _, expected := range []string{"name: cursor", "name: limit", "BulletinWatermarkInvestigationGlobalPageEnvelope"} {
		if !strings.Contains(global, expected) {
			t.Fatalf("global investigation history missing %q:\n%s", expected, global)
		}
	}
	row := schemaBlock(document, "BulletinWatermarkInvestigationGlobalRow")
	for _, expected := range []string{"requestedIssue:", "actualIssue:", "resolvedLocale:", "resolvedRevision:", "submittedBy:", "matchedAccount:", "issuedAt:", "reasonCode:"} {
		if !strings.Contains(row, expected) {
			t.Fatalf("investigation row missing %q:\n%s", expected, row)
		}
	}
	for _, operationID := range []string{"listBulletinWatermarkInvestigations", "getBulletinWatermarkInvestigation"} {
		if !strings.Contains(document, "operationId: "+operationID) {
			t.Fatalf("compatibility operation %s removed", operationID)
		}
	}
}

func TestOpenAPIRemovesLegacyBulletinAccessContract(t *testing.T) {
	document := readOpenAPI(t)
	for _, forbidden := range []string{
		"  /bulletins:\n", "  /bulletins/latest:\n", "  /bulletins/by-number/", "  /bulletin-access", "bulletin:read", "bulletin:trace",
		"PublicBulletin", "BulletinAccess", "MemberBulletinAccess", "publicGrantId",
	} {
		if strings.Contains(document, forbidden) {
			t.Fatalf("OpenAPI retains legacy bulletin contract %q", forbidden)
		}
	}
}

func TestOpenAPIUsesBusinessPermissionsWithoutAssetOrCMSFallbacks(t *testing.T) {
	document := readOpenAPI(t)
	for operationID, scope := range map[string]string{
		"createBulletinUploadSession": "cms:bulletins:write",
		"retryBulletinAssetScan":      "cms:bulletins:write",
		"createNewsCoverUpload":       "cms:news:write",
		"createHomeBannerUpload":      "cms:pages:write",
		"listCampaigns":               "campaigns:read",
		"listNotificationSummaries":   "campaigns:read",
		"createCampaign":              "campaigns:write",
		"sendCampaign":                "campaigns:send",
	} {
		operation := operationByID(t, document, operationID)
		if !strings.Contains(operation, "x-required-scopes: ["+scope+"]") {
			t.Errorf("%s missing exact scope %s:\n%s", operationID, scope, operation)
		}
		for _, forbidden := range []string{"assets:read", "assets:write", "cms:read", "cms:write", "cms:publish", "x-required-any-scopes", "x-required-scope-sets"} {
			if strings.Contains(operation, forbidden) {
				t.Errorf("%s retains %q:\n%s", operationID, forbidden, operation)
			}
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

func TestOpenAPIDocumentsNotificationIntentWithoutSourceDeliveryState(t *testing.T) {
	contents, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	document := string(contents)
	for _, expected := range []string{"notifySubscribers:", "statementNotification:"} {
		if !strings.Contains(document, expected) {
			t.Fatalf("OpenAPI document missing %q", expected)
		}
	}
	for _, forbidden := range []string{"notificationStatus:", "notificationQueuedAt:", "notificationErrorCode:", "/statement-notifications:", "/statement-notifications/preview:", "StatementNotificationBatch:"} {
		if strings.Contains(document, forbidden) {
			t.Fatalf("OpenAPI document retains source notification state %q", forbidden)
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
	spec, err := loadCanonicalOpenAPI()
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
		"getSiteSettings": "cms:pages:read", "saveSiteSettings": "cms:pages:write", "publishSiteSettings": "cms:pages:publish",
		"unpublishSiteSettings": "cms:pages:publish", "listSiteSettingsRevisions": "cms:pages:read", "restoreSiteSettingsRevision": "cms:pages:write",
	} {
		operation := operationByID(t, document, operationID)
		for _, expected := range []string{"x-hhc-visibility: admin", "x-hhc-callers: [api-gateway]", "servers: [{ url: https://admin.alive.org.tw/api }]", "x-required-scopes: [" + scope + "]", "daprApiToken", "daprCallerAppId", "trustedUserId", "trustedAuthProvider", "trustedScopes"} {
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
		if !strings.Contains(operationByID(t, document, operationID), "x-required-scopes: [cms:pages:write]") {
			t.Errorf("%s missing write scopes", operationID)
		}
	}
	if !strings.Contains(operationByID(t, document, "getHomeBannerStatus"), "x-required-scopes: [cms:pages:read]") {
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

// Normalize the fixed YAML once, then resolve a fresh document for each caller.
var canonicalOpenAPIJSON = sync.OnceValues(func() ([]byte, error) {
	document, err := openapi3.NewLoader().LoadFromFile("openapi.yaml")
	if err != nil {
		return nil, err
	}
	return json.Marshal(document)
})

func loadCanonicalOpenAPI() (*openapi3.T, error) {
	data, err := canonicalOpenAPIJSON()
	if err != nil {
		return nil, err
	}
	return openapi3.NewLoader().LoadFromDataWithPath(data, &url.URL{Path: "openapi.yaml"})
}

func loadOpenAPI(t *testing.T) *openapi3.T {
	t.Helper()
	document, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	return document
}

func compileOpenAPISchema(t *testing.T, name string) *jsonschema.Schema {
	t.Helper()
	return compileCandidateSchema(t, "openapi.yaml", name)
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
	document, err := loadCanonicalOpenAPI()
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
		"BulletinSeries:",
		"enum: [general, children]",
		"BulletinLocale:",
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
		"x-required-scopes: [cms:bulletins:write]",
		"x-required-scopes-by-module:",
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
		{"deleteCampaign", "'204': { description: Campaign deleted }", []string{"400", "409"}},
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
		"node-version: '24.22.1'",
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
	document, err := loadCanonicalOpenAPI()
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
		expectedSecurity := adminSecurity
		operationID, _ := operationValue(operation.block, "operationId")
		switch operationID {
		case "listAdminRecordings", "createAdminRecording", "getAdminRecording", "updateAdminRecordingTitle", "setAdminRecordingExposure", "publishAdminRecording", "unpublishAdminRecording", "createRecordingPackage", "getRecordingPackage", "signRecordingPackage", "completeRecordingPackage", "listRecordingCovers", "uploadRecordingCover", "setRecordingCover", "getRecordingCoverContent":
			expectedSecurity = strings.TrimSuffix(adminSecurity, "]") + ", { daprApiToken: [], daprCallerAppId: [], trustedPrincipalId: [], trustedPrincipalType: [], trustedClientId: [], trustedCredentialId: [], trustedAuthProvider: [], trustedScopes: [] }]"
		}
		if strings.HasPrefix(operation.path, "/ingest/") {
			expectedSecurity = privateSecurity
			if !strings.HasPrefix(operation.path, "/ingest/broadcast-source/") && operation.path != "/ingest/streams/open" && operation.path != "/ingest/streams/replay" {
				expectedSecurity = "[{ daprApiToken: [], daprCallerAppId: [], broadcastIngestSession: [] }]"
			}
		}
		if operation.path == "/member/bulletins/online" {
			expectedSecurity = strings.TrimSuffix(adminSecurity, "]") + ", { cmsServiceCaller: [], cmsServiceToken: [], trustedUserId: [], trustedAuthProvider: [] }]"
		}
		if (visibility == "admin" || visibility == "authenticated") && (count != 1 || security != expectedSecurity) {
			return fmt.Errorf("%s must document trusted gateway security", name)
		}
		expectedPrivateSecurity := privateSecurity
		switch operation.path {
		case "/priv/legal/current", "/priv/legal/snapshots/resolve", "/priv/account-cleanup", "/priv/recordings/{recordingID}/notification-policy":
			expectedPrivateSecurity = "[{ daprApiToken: [], daprCallerAppId: [] }, { cmsServiceCaller: [], cmsServiceToken: [] }]"
		}
		if operation.path == "/priv/line/member-access" {
			expectedPrivateSecurity = "[{ daprApiToken: [], daprCallerAppId: [], trustedUserId: [], trustedAuthProvider: [] }, { cmsServiceCaller: [], cmsServiceToken: [], trustedUserId: [], trustedAuthProvider: [] }]"
			if callers != "[hhc-line-function-bot]" {
				return fmt.Errorf("%s must restrict callers to the LINE Bot", name)
			}
		}
		if visibility == "private" && (count != 1 || security != expectedPrivateSecurity) {
			return fmt.Errorf("%s must document service caller security", name)
		}
		if visibility != "admin" && visibility != "authenticated" && visibility != "private" && (count != 1 || security != "[]") {
			return fmt.Errorf("%s must document its unauthenticated service boundary", name)
		}
		if (visibility == "admin" || visibility == "authenticated") && !strings.HasPrefix(operation.path, "/ingest/") {
			responses := map[string]string{"401": "AdminUnauthorized", "403": "AdminForbidden"}
			if strings.HasPrefix(operation.path, "/member/bulletins") || strings.HasPrefix(operation.path, "/member/bulletin-download-jobs") {
				delete(responses, "403")
			}
			for status, response := range responses {
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
	methods := map[string]bool{"head": true, "get": true, "post": true, "put": true, "delete": true, "patch": true}
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
        GET /video-previews/{}
        GET /video-previews/{}/cover
        GET /member/videos/{}/watch
        GET /recording-previews/{}
        GET /recording-previews/{}/cover
        GET /member/recordings/{}/interactions
        PUT /member/recordings/{}/like
        GET /member/recordings/{}/comments
        POST /member/recordings/{}/comments
        PATCH /member/recordings/{}/comments/{}
        DELETE /member/recordings/{}/comments/{}
        PUT /member/recordings/{}/comments/{}/report
        PUT /member/recordings/{}/comments/{}/reaction
        GET /member/recordings/continue-watching
        GET /member/recordings/{}/progress
        PUT /member/recordings/{}/progress
        POST /member/recordings/{}/progress/claim
        POST /member/recordings/{}/heartbeat
        GET /member/recordings/{}/metrics
        GET /admin/recordings/{}/metrics
        GET /admin/recordings/{}/comments
        PUT /admin/recordings/{}/comments/moderation
        POST /ingest/streams/open
        POST /ingest/streams/replay
        GET /admin/broadcast-sources
        POST /admin/broadcast-sources
        PATCH /admin/broadcast-sources/{}
        DELETE /admin/broadcast-sources/{}
        POST /admin/broadcast-sources/{}/stream-key/reveal
        PUT /admin/broadcast-sources/{}/stream-key
        PUT /admin/broadcast-sources/{}/target
        POST /admin/broadcast-sources/{}/direct/ensure
        PUT /admin/studio/broadcasts/{}/source
        POST /admin/studio/broadcasts
        GET /admin/broadcasts/{}/studio
        POST /ingest/broadcast-source/resolve
        POST /ingest/broadcast-source/bindings
        POST /ingest/broadcast-source/session
        GET /ingest/broadcasts/{}/captures/{}
        POST /ingest/broadcasts/{}/captures/{}/objects
        POST /ingest/broadcasts/{}/captures/{}/sign
        POST /ingest/broadcasts/{}/captures/{}/confirm
        POST /ingest/broadcasts/{}/captures/{}/stop
        POST /ingest/broadcasts/{}/captures/{}/seal
        POST /ingest/broadcasts/{}/captures/{}/abort
        GET /ingest/broadcasts/{}/control
        POST /ingest/broadcasts/{}/commands/{}/ack
        GET /admin/broadcast-source
        PUT /admin/broadcast-source/stream-key
        PUT /admin/broadcast-source/target
        POST /admin/broadcast-source/resolve
        POST /admin/broadcast-source/bindings
        GET /admin/broadcast-capabilities
        POST /admin/broadcasts
        GET /admin/broadcasts
        GET /admin/broadcasts/{}
        PATCH /admin/broadcasts/{}
        POST /admin/broadcasts/{}/announce
        POST /admin/broadcasts/{}/cancel
        GET /admin/broadcasts/{}/stream-key
        PUT /admin/broadcasts/{}/stream-key
        POST /admin/broadcasts/{}/stream-key-bindings
        POST /admin/broadcasts/{}/bindings
        POST /admin/broadcasts/{}/start
        POST /admin/broadcasts/{}/end
        POST /admin/broadcasts/{}/commands/{}/cancel
        GET /admin/broadcasts/{}/control
        POST /admin/broadcasts/{}/commands/{}/ack
        POST /admin/broadcasts/{}/emergency-close
        GET /member/recordings/{}/watch
        POST /admin/broadcasts/{}/preview-access
        GET /member/broadcasts
        POST /admin/broadcasts/{}/cover-uploads
        GET /admin/broadcasts/{}/cover-uploads/{}
        GET /admin/broadcasts/{}/cover-uploads/{}/content
        GET /admin/broadcasts/{}/cover/content
        GET /member/recordings/{}/broadcast-cover
        POST /admin/recordings/{}/captures
        GET /admin/recordings/{}/captures/{}
        POST /admin/recordings/{}/captures/{}/objects
        POST /admin/recordings/{}/captures/{}/sign
        POST /admin/recordings/{}/captures/{}/confirm
        POST /admin/recordings/{}/captures/{}/stop
        POST /admin/recordings/{}/captures/{}/seal
        POST /admin/recordings/{}/captures/{}/abort
        DELETE /admin/recordings/{}/captures/{}/auto-publish
        DELETE /admin/recordings/{}/captures/{}/live
        GET /member/recordings/live
        POST /member/recordings/{}/live/playback
        GET /legal/common
        GET /member/legal/current
        GET /admin/legal/{}
        PUT /admin/legal/{}
        POST /admin/legal/{}/publish
        POST /priv/legal/current
        POST /priv/legal/snapshots/resolve
		GET /health
		GET /health/live
		GET /ready
		GET /health/ready
		POST /member/bulletin-download-jobs
		GET /member/bulletin-download-jobs/{}
		GET /member/bulletin-download-jobs/{}/file
		GET /member/bulletins
		GET /member/bulletins/online
		POST /member/bulletins/{}/versions/{}/online/access
		GET /member/bulletins/{}/versions/{}/online/reader/state
		POST /member/bulletins/{}/versions/{}/online/reader/mutations
		GET /member/bulletins/latest
		GET /member/bulletins/{}/versions/{}
		GET /member/bulletins/{}/versions/{}/download
		GET /member/recordings
		GET /member/recordings/{}/cover
		GET /admin/recordings/{}/covers
		GET /admin/recordings/live-cover-settings
		PUT /admin/recordings/live-cover-settings
		POST /admin/recordings/live-cover-settings/uploads
		GET /admin/recordings/live-cover-settings/uploads/{}
		GET /admin/recordings/live-cover-settings/uploads/{}/content
		GET /admin/recordings/{}/captures/{}/cover-settings
		PUT /admin/recordings/{}/captures/{}/cover-settings
		POST /admin/recordings/{}/captures/{}/cover-settings/uploads
		GET /admin/recordings/{}/captures/{}/cover-settings/uploads/{}
		GET /admin/recordings/{}/captures/{}/cover-settings/uploads/{}/content
		GET /admin/recordings/{}/captures/{}/cover-settings/content
		GET /member/recordings/{}/live/{}/cover
		GET /admin/recordings/retention-policy
		PUT /admin/recordings/retention-policy
		POST /admin/recordings/retention-policy/preview
		POST /admin/recordings/{}/cover-uploads
		PUT /admin/recordings/{}/cover
		GET /admin/recordings/{}/covers/{}/content
		POST /member/recordings/{}/playback
		POST /admin/bulletins/watermark-lookups
		POST /admin/recordings/watermark-lookups
        GET /admin/bulletins/{}/watermark-versions
		GET /admin/bulletins/{}/watermark-investigations
        GET /admin/bulletin-watermark-investigations
        POST /admin/bulletin-watermark-investigations
        GET /admin/bulletin-watermark-investigations/{}
		POST /priv/account-cleanup
		GET /statements/active
        GET /me/statements/{}/dismissal
        PUT /me/statements/{}/dismissal
		GET /news
		GET /news/{}
		GET /history
		GET /videos
		GET /locations
		GET /pages/{}
		GET /site-layout
		GET /home
		GET /priv/line/member-access
        GET /priv/bulletins/{}/notification-policy
		GET /priv/recordings/{}/notification-policy
		GET /admin/site-settings
		PUT /admin/site-settings
		POST /admin/site-settings/publish
		POST /admin/site-settings/unpublish
		GET /admin/site-settings/revisions
		POST /admin/site-settings/revisions/{}/restore
		GET /admin/bulletins
		GET /admin/bulletins/template-settings
		PUT /admin/bulletins/template-settings
		GET /admin/bulletins/template-settings/revisions
		GET /admin/bulletin-learning-rules
		GET /admin/bulletin-learning-rules/{}
		PUT /admin/bulletin-learning-rules/{}
		GET /admin/bulletins/{}/online/{}/{}/source-pdf
        GET /admin/bulletins/{}/online/{}/{}/source-parts
        POST /admin/bulletins/{}/online/{}/{}/source-repairs
		GET /admin/bulletins/{}/online/{}/{}
		POST /admin/bulletins/{}/online/{}/{}/extractions
		POST /admin/bulletins/{}/online/{}/{}/conversions
		PUT /admin/bulletins/{}/online/{}/{}/draft
		GET /admin/bulletins/{}/online/{}/{}/comparison
		POST /admin/bulletins/{}/online/{}/{}/compare
		POST /admin/bulletins/{}/online/{}/{}/confirm
		POST /admin/bulletins/{}/online/{}/{}/publish
		POST /admin/bulletins/{}/online/{}/{}/unpublish
		POST /admin/bulletins/{}/online/{}/{}/revisions/{}/restore
		GET /admin/bulletins/{}/online/{}/{}/revisions
		GET /admin/bulletins/{}/online/{}/{}/revisions/{}
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
		GET /admin/recordings
		POST /admin/recordings
		GET /admin/recordings/{}
		PATCH /admin/recordings/{}
		DELETE /admin/recordings/{}
		PATCH /admin/recordings/{}/exposure
		POST /admin/recordings/{}/publish
		POST /admin/recordings/{}/packages
		POST /admin/recordings/{}/source-uploads
		GET /admin/recordings/{}/source-uploads/{}
		POST /admin/recordings/{}/source-uploads/{}/sign
		POST /admin/recordings/{}/source-uploads/{}/complete
		POST /admin/recordings/{}/source-uploads/{}/retry-processing
		GET /admin/recordings/{}/packages/{}
		POST /admin/recordings/{}/packages/{}/sign
		POST /admin/recordings/{}/packages/{}/complete
		POST /admin/recordings/{}/unpublish
		POST /admin/bulletins/{}/translation-previews/{}
		GET /admin/bulletins/{}/notification-preview
		POST /admin/bulletins/{}/notifications
		POST /admin/campaign-submissions
		GET /admin/content/{}
		POST /admin/content/{}
		GET /admin/content/{}/{}
		PUT /admin/content/{}/{}
		DELETE /admin/content/{}/{}
		POST /admin/content/{}/{}/translation-previews/{}
		POST /admin/content/news/{}/statement-popup/end
		POST /admin/content/{}/{}/publish
		POST /admin/content/{}/{}/unpublish
		GET /admin/content/{}/{}/revisions
		POST /admin/content/{}/{}/revisions/{}/restore
		POST /admin/content/news/{}/upload-sessions
		GET /admin/content/news/{}/assets/{}
		GET /admin/content/news/{}/assets/{}/preview
		POST /admin/content/news/{}/assets/{}/scan/retry
		POST /admin/content/news/{}/assets/{}/complete
		POST /admin/content/pages/{}/upload-sessions
		GET /admin/content/pages/{}/assets/{}
		POST /admin/content/pages/{}/assets/{}/scan/retry
		POST /admin/content/pages/{}/assets/{}/complete
		GET /admin/campaigns
		POST /admin/campaigns
		GET /admin/notification-summaries
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

func TestRecordingCaptureRuntimeWireShapeMatchesC1(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	status := recordings.CaptureStatus{RecordingCaptureStatus: assetclient.RecordingCaptureStatus{CaptureID: strings.Repeat("a", 32), RecordingID: "11111111-1111-4111-8111-111111111111", State: "uploading", CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour), Objects: []assetclient.RecordingCaptureObject{}, Progress: assetclient.RecordingLiveProgress{LastSequence: -1}}, RecordingVersion: 1, LiveState: "starting", LiveEnabled: true, AutoPublish: "pending", ServerNow: now}
	result := recordings.CaptureResult{Capture: status, Receipt: assetclient.RecordingCaptureReceipt{OperationKey: "create", Operation: "create", CaptureID: status.CaptureID, AcceptedAt: now}}
	for _, tc := range []struct {
		name  string
		value any
	}{{"RecordingCaptureStatus", status}, {"RecordingCaptureResult", result}} {
		raw, err := json.Marshal(tc.value)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err = json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		if err = compileOpenAPISchema(t, tc.name).Validate(value); err != nil {
			t.Fatalf("%s runtime wire drift: %v", tc.name, err)
		}
	}
}

func TestRecordingRenditionUsesActualBoundedSegmentCount(t *testing.T) {
	schema := compileOpenAPISchema(t, "RecordingRendition")
	r := assetclient.RecordingRendition{Name: "720p", Width: 1280, Height: 720, FrameRate: 30000.0 / 1001, VideoBitrate: 1500000, AudioBitrate: 128000, DurationSeconds: 240.206633, SegmentCount: 8}
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	if err := schema.Validate(value); err != nil {
		t.Fatalf("actual eight-segment recording: %v", err)
	}
	for _, count := range []float64{0, 4321} {
		value["segmentCount"] = count
		if err := schema.Validate(value); err == nil {
			t.Fatalf("out-of-bounds segment count accepted: %v", count)
		}
	}
	description := loadOpenAPI(t).Components.Schemas["RecordingRendition"].Value.Properties["segmentCount"].Value.Description
	if strings.Contains(description, "ceil(") || !strings.Contains(description, "Actual number of contiguous media segments") {
		t.Fatalf("segment count description contradicts actual media: %s", description)
	}
}

func TestOnlineReviewLocatorContract(t *testing.T) {
	schema := compileOpenAPISchema(t, "OnlineBulletinReviewIssue")
	value := map[string]any{"code": "unknown_font_role", "blocking": true, "componentId": "body", "blockId": "block", "sentenceId": "sentence", "sourcePage": float64(7)}
	if err := schema.Validate(value); err != nil {
		t.Fatal(err)
	}
	value["sourcePage"] = float64(0)
	if err := schema.Validate(value); err == nil {
		t.Fatal("zero source page accepted")
	}
	delete(value, "sourcePage")
	delete(value, "blockId")
	delete(value, "sentenceId")
	if err := schema.Validate(value); err != nil {
		t.Fatal("legacy finding rejected", err)
	}
}

func TestLiveRuntimeWireAndAdminSummaryMatchCanonicalSchema(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	id := "11111111-1111-4111-8111-111111111111"
	capture := strings.Repeat("a", 32)
	scope := "22222222-2222-4222-8222-222222222222"
	progress := assetclient.RecordingLiveProgress{Revision: 3, LastSequence: 2, MediaEndSeconds: 90, LastAdvancedAt: &now}
	for _, tc := range []struct {
		name  string
		value any
	}{
		{"MemberLiveRecording", recordings.LiveRecording{ID: id, CaptureID: capture, Title: "Live", Description: "Plain text", LiveState: "live", CreatedAt: now, Progress: progress}},
		{"MemberLivePlayback", recordings.LivePlayback{RecordingID: id, CaptureID: capture, PlaybackScopeID: scope, MediaURL: "https://media.alive.org.tw/videos/" + id + "/captures/" + capture + "/sessions/" + scope + "/master.m3u8", ExchangeCredential: "credential", IssuedAt: now, ExpiresAt: now.Add(5 * time.Minute), ServerNow: now, CaptureExpiresAt: now.Add(24 * time.Hour), LiveState: "live", Progress: progress, WatermarkCode: "01234ABCDE"}},
		{"AdminRecordingCaptureSummary", recordings.AdminCaptureSummary{CaptureID: capture, LiveEnabled: true, LiveState: "live", AutoPublish: "blocked"}},
	} {
		raw, err := json.Marshal(tc.value)
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err = json.Unmarshal(raw, &value); err != nil {
			t.Fatal(err)
		}
		if err = compileOpenAPISchema(t, tc.name).Validate(value); err != nil {
			t.Fatalf("%s %v", tc.name, err)
		}
	}
}

func TestRecordingSearchParametersKeepPrivateIncrementalContracts(t *testing.T) {
	doc, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"/member/recordings", "/member/recordings/live"} {
		op := doc.Paths.Find(route).Get
		found := false
		for _, param := range op.Parameters {
			p := param.Value
			if p.Name == "q" {
				found = true
				if p.In != "query" || p.Required || p.Schema.Value.MaxLength == nil || *p.Schema.Value.MaxLength != 100 {
					t.Fatalf("wrong q contract %s", route)
				}
			}
		}
		if !found || op.Responses.Value("400") == nil || op.Extensions["x-hhc-cache"] != "private, no-store" {
			t.Fatalf("missing search/private error contract %s", route)
		}
	}
}
