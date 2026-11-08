package recordings

import "testing"

func TestRecordingWatermarkFormat(t *testing.T) {
	for _, in := range []string{"01234ABCDE", " 01234-abcde "} {
		if got, err := NormalizeWatermarkCode(in); err != nil || got != "01234ABCDE" {
			t.Fatalf("%q %v", got, err)
		}
	}
	for _, in := range []string{"12345678", "01234ABCDI", "01234ABCDO", "01234ABCDU", "01234ABCDEextra", "01234 ABCDE", "--01234ABCDE"} {
		if _, err := NormalizeWatermarkCode(in); err == nil {
			t.Fatalf("accepted %q", in)
		}
	}
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		code, err := NewWatermarkCode()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = NormalizeWatermarkCode(code); err != nil || seen[code] {
			t.Fatal("invalid generated code", err)
		}
		seen[code] = true
	}
}
