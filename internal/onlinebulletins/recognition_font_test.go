package onlinebulletins

import (
	"reflect"
	"testing"
)

func TestRecognitionGlyphFallbackPreservesTextAndSize(t *testing.T) {
	coverage := FontCoverage{"body": {{'A', 'Z'}}, "scripture": {{'稣', '稣'}}}
	size := 14.0
	span := Span{Text: "A稣稣B", FontRole: "body", FontSize: &size}
	runs, ok := coverage.glyphRuns(span)
	want := []Span{{Text: "A", FontRole: "body", FontSize: &size}, {Text: "稣稣", FontRole: "scripture", FontSize: &size}, {Text: "B", FontRole: "body", FontSize: &size}}
	if !ok || !reflect.DeepEqual(runs, want) {
		t.Fatalf("unexpected fallback: %+v", runs)
	}
	for _, role := range []string{"body", "symbol"} {
		original := Span{Text: "字", FontRole: role}
		runs, ok = coverage.glyphRuns(original)
		if ok || !reflect.DeepEqual(runs, []Span{original}) {
			t.Fatal("unsupported glyph must remain available for review")
		}
	}
}
