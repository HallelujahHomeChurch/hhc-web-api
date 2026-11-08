package hhcwebapi

import (
	"strings"
	"testing"
)

func TestRecordingDescriptionContract(t *testing.T) {
	schema := compileOpenAPISchema(t, "RecordingDescription")
	for _, tc := range []struct {
		value any
		valid bool
	}{{"", true}, {strings.Repeat("😀", 5000), true}, {strings.Repeat("😀", 5001), false}, {nil, false}, {"\x00", false}, {42, false}} {
		if err := schema.Validate(tc.value); (err == nil) != tc.valid {
			t.Fatalf("description valid=%v: %v", tc.valid, err)
		}
	}
}
