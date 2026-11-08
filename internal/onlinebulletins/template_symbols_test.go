package onlinebulletins

import "testing"

func TestWingdingsNumberedListsCoverAllTenItems(t *testing.T) {
	for n := rune(0); n < 10; n++ {
		got, role, known := wingdingsRune('\uf08c' + n)
		if !known || role != "body" || got != '➊'+n {
			t.Fatalf("number %d was not converted", n+1)
		}
	}
	if _, _, known := wingdingsRune('\uf096'); known {
		t.Fatal("unknown glyph inferred")
	}
}
