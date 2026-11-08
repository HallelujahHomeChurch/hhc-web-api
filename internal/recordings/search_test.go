package recordings

import (
	"strings"
	"testing"
)

func TestNormalizeRecordingSearch(t *testing.T) {
	for _, tc := range []struct {
		input, want string
		invalid     bool
	}{
		{"  Faith\t主日\u3000 Hope  ", "faith 主日 hope", false}, {" \n ", "", false},
		{strings.Repeat("😀", 100), strings.Repeat("😀", 100), false}, {strings.Repeat("😀", 101), "", true},
		{"a\x00b", "", true}, {string([]byte{0xff}), "", true},
	} {
		got, err := NormalizeSearch(tc.input)
		if (err != nil) != tc.invalid || got != tc.want {
			t.Fatalf("normalize: got %q err=%v", got, err)
		}
	}
}
func TestRecordingSearchMatchesAllLiteralTermsAcrossFields(t *testing.T) {
	for _, tc := range []struct {
		q, title, description string
		want                  bool
	}{
		{"faith 主日", "主日聚會", "Faith and hope", true},
		{"faith missing", "Faith", "Hope", false}, {"100% a_b", "100%", "literal a_b", true},
		{"100% a_b", "1000", "axb", false}, {"", "anything", "", true},
	} {
		if searchMatches(tc.q, tc.title, tc.description) != tc.want {
			t.Fatalf("matching %q", tc.q)
		}
	}
}
