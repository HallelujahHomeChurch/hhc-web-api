package recordings

import (
	"strings"
	"testing"
)

func TestCommentInputBoundaries(t *testing.T) {
	for _, test := range []struct {
		text, id, parent string
		valid            bool
	}{{"Hello", "11111111-1111-4111-8111-111111111111", "", true}, {strings.Repeat("字", 2000), "11111111-1111-4111-8111-111111111111", "", true}, {strings.Repeat("字", 2001), "11111111-1111-4111-8111-111111111111", "", false}, {"  ", "11111111-1111-4111-8111-111111111111", "", false}, {"Hello", "invalid", "", false}, {"Hello", "11111111-1111-4111-8111-111111111111", "invalid", false}, {"Hello\x00", "11111111-1111-4111-8111-111111111111", "", false}} {
		if got := (CommentInput{ClientMutationID: test.id, ParentID: test.parent, Text: test.text}).Validate() == nil; got != test.valid {
			t.Errorf("valid=%v want %v", got, test.valid)
		}
	}
}
