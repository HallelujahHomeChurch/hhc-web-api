package hhcwebapi

import (
	"os"
	"strings"
	"testing"
)

func TestPrivateReaderOpenAPIContract(t *testing.T) {
	raw, err := os.ReadFile("openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	contract := string(raw)
	for _, required := range []string{
		"/member/bulletins/{issueID}/versions/{locale}/online/reader/state:",
		"/member/bulletins/{issueID}/versions/{locale}/online/reader/mutations:",
		"operationId: getBulletinReaderState", "operationId: mutateBulletinReader",
		"ReaderBookmark:", "createBookmark", "renameBookmark", "deleteBookmark", "reanchorBookmark", "bookmark_conflict", "bookmark_limit",
		"ReaderPrivateState:", "ReaderMutation:", "ReaderNote:",
		"maxItems: 100", "maxItems: 500", "maxLength: 10000",
		"mapping_history_unavailable", "mutation_id_conflict", "reanchorRequired:",
	} {
		if !strings.Contains(contract, required) {
			t.Errorf("missing private reader contract %s", required)
		}
	}
}

func TestBookmarkMutationOpenAPIRejectsMissingCASAndTarget(t *testing.T) {
	schema := compileOpenAPISchema(t, "ReaderMutation")
	id := "49f10a14-688e-4726-b46f-51b68f950128"
	for _, kind := range []string{"createBookmark", "renameBookmark", "deleteBookmark", "reanchorBookmark"} {
		payload := map[string]any{"bookmarkId": id}
		if kind == "createBookmark" || kind == "renameBookmark" {
			payload["name"] = "bookmark"
		}
		if kind == "createBookmark" || kind == "reanchorBookmark" {
			payload["sentenceId"] = "a"
			payload["offset"] = 0
			payload["pageId"] = "page"
		}
		input := map[string]any{"mutationId": id, "createdAt": "2026-10-19T00:00:00Z", "documentRevision": 3, "kind": kind, "payload": payload}
		if kind != "createBookmark" {
			input["baseVersion"] = 1
		}
		if err := schema.Validate(input); err != nil {
			t.Fatal(kind, err)
		}
		if kind != "createBookmark" {
			delete(input, "baseVersion")
			if schema.Validate(input) == nil {
				t.Fatal("missing CAS accepted", kind)
			}
			input["baseVersion"] = 1
		} else {
			input["baseVersion"] = 1
			if schema.Validate(input) == nil {
				t.Fatal("create CAS accepted")
			}
			delete(input, "baseVersion")
		}
		payload["owner"] = "forged"
		if schema.Validate(input) == nil {
			t.Fatal("unknown private input accepted", kind)
		}
		delete(payload, "owner")
		if kind == "createBookmark" || kind == "reanchorBookmark" {
			delete(payload, "offset")
			if schema.Validate(input) == nil {
				t.Fatal("missing offset accepted", kind)
			}
		}
	}
}
