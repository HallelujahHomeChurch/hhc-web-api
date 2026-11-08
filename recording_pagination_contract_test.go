package hhcwebapi

import "testing"

func TestRecordingListCursorContract(t *testing.T) {
	schema := compileOpenAPISchema(t, "RecordingListEnvelope")
	for _, meta := range []map[string]any{{}, {"nextCursor": nil}, {"nextCursor": "opaque"}} {
		if err := schema.Validate(map[string]any{"data": []any{}, "meta": meta, "error": nil}); err != nil {
			t.Fatal(err)
		}
	}
	if err := schema.Validate(map[string]any{"data": []any{}, "meta": map[string]any{"nextCursor": 42}}); err == nil {
		t.Fatal("numeric cursor accepted")
	}
}
