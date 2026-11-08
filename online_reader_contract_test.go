package hhcwebapi

import (
	"strings"
	"testing"
)

func TestMemberReaderContract(t *testing.T) {
	document := readOpenAPI(t)
	for _, id := range []string{"discoverOnlineBulletins", "openOnlineBulletin"} {
		operation := operationByID(t, document, id)
		for _, required := range []string{"private, no-store", "ReaderUnavailable", "'503'"} {
			if !strings.Contains(operation, required) {
				t.Errorf("%s missing %s", id, required)
			}
		}
	}
	spec, err := loadCanonicalOpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ReaderContent", "ReaderSentence"} {
		ref := spec.Components.Schemas[name]
		if ref == nil {
			t.Fatalf("missing %s", name)
		}
		if ref.Value.AdditionalProperties.Has == nil || *ref.Value.AdditionalProperties.Has {
			t.Fatalf("%s is not closed", name)
		}
		for _, key := range []string{"source", "sourceAssetChecksum", "sourcePageCount", "createdBy", "reviewIssues"} {
			if ref.Value.Properties[key] != nil {
				t.Errorf("%s permits %s", name, key)
			}
		}
	}
	if !strings.Contains(operationByID(t, document, "openOnlineBulletin"), "X-HHC-Content-SHA256") {
		t.Fatal("missing byte digest")
	}
}
