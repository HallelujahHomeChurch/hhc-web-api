package hhcwebapi

import "testing"

func TestContractSchemaCompilationReusesImmutableDefinitions(t *testing.T) {
	first := compileOpenAPISchema(t, "ReaderMutation")
	if again := compileOpenAPISchema(t, "ReaderMutation"); first != again {
		t.Fatal("unchanged contract schema was compiled again")
	}
	path := "docs/obs-capture/live-control-v1/openapi.snapshot.yaml"
	candidate := compileCandidateSchema(t, path, "BroadcastMarkerAckInput")
	if again := compileCandidateSchema(t, path, "BroadcastMarkerAckInput"); candidate != again {
		t.Fatal("unchanged snapshot schema was compiled again")
	}
	canonical := compileOpenAPISchema(t, "BroadcastMarkerAckInput")
	if canonical == candidate {
		t.Fatal("same schema name in different contracts shares a cache entry")
	}
	for range 8 {
		t.Run("concurrent validation", func(t *testing.T) {
			t.Parallel()
			if compileOpenAPISchema(t, "ReaderMutation") != first {
				t.Fatal("concurrent compilation missed the cached schema")
			}
			if first.Validate(map[string]any{}) == nil {
				t.Fatal("cached schema stopped rejecting missing fields")
			}
		})
	}
}

func TestCanonicalContractLoadsKeepIndependentDocuments(t *testing.T) {
	first, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	delete(first.Components.Schemas, "ReaderMutation")
	second, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	if second.Components.Schemas["ReaderMutation"] == nil {
		t.Fatal("one test mutated another test's contract")
	}
	if second.Paths.Find("/member/recordings") == nil {
		t.Fatal("cached contract lost its resolved paths")
	}
}
