package bulletinwatermark

import (
	"strings"
	"testing"
	"time"
)

func TestTraceCode(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		code, err := NewCode()
		if err != nil {
			t.Fatal(err)
		}
		normalized, err := NormalizeCode(strings.ToLower(FormatCode(code)))
		if err != nil || normalized != code || len(code) != 18 || len(FormatCode(code)) != 22 || seen[code] {
			t.Fatalf("invalid or duplicate code: %v", err)
		}
		seen[code] = true
		replacement := byte('0')
		if code[len(code)-1] == replacement {
			replacement = '1'
		}
		if _, err := NormalizeCode(code[:len(code)-1] + string(replacement)); err == nil {
			t.Fatal("accepted corrupt checksum")
		}
	}
	for _, code := range []string{"", strings.Repeat("0", 29), strings.Repeat("!", 28), strings.Repeat("0", 10000)} {
		if _, err := NormalizeCode(code); err == nil {
			t.Fatal("accepted invalid code")
		}
	}
}
func TestRetentionIs365Days(t *testing.T) {
	issued := time.Date(2028, 2, 29, 12, 0, 0, 0, time.UTC)
	if got := issued.Add(Retention); !got.Equal(time.Date(2029, 2, 28, 12, 0, 0, 0, time.UTC)) {
		t.Fatal(got)
	}
}

func TestNormalizeCodePreservesLegacy128BitCodes(t *testing.T) {
	legacyPayload := encoding.EncodeToString([]byte("0123456789abcdef"))
	legacy := legacyPayload + checksum(legacyPayload)
	got, err := NormalizeCode(strings.ToLower(FormatCode(legacy)))
	if err != nil || got != legacy || len(got) != 28 {
		t.Fatalf("legacy lookup rejected: %v", err)
	}
	// The last legacy base32 symbol contains two padding bits; a checksum alone
	// must not make a noncanonical encoding acceptable.
	invalidPayload := legacyPayload[:25] + "Z"
	if _, err := NormalizeCode(invalidPayload + checksum(invalidPayload)); err == nil {
		t.Fatal("accepted noncanonical legacy payload")
	}
}

func TestNormalizeShortCodeStrictFormatAndChecksum(t *testing.T) {
	payload := encoding.EncodeToString([]byte("0123456789"))
	valid := payload + checksum(payload)
	if got, err := NormalizeCode("  " + strings.ToLower(FormatCode(valid)) + "  "); err != nil || got != valid {
		t.Fatalf("short normalization: %v", err)
	}
	for _, invalid := range []string{valid[:17], valid + "0", "I" + valid[1:], "O" + valid[1:], valid[:4] + " " + valid[4:], strings.Repeat("0", 18)} {
		if _, err := NormalizeCode(invalid); err == nil {
			t.Fatal("accepted invalid short code")
		}
	}
}

func TestFingerprintMetadata(t *testing.T) {
	a, err := NewFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewFingerprint()
	if err != nil || len(a) != 61 || a == b || strings.Trim(a, "01") != "" {
		t.Fatal("invalid independent fingerprint")
	}
	v := Receipt{FingerprintPayload: a, ModelSHA256: strings.Repeat("a", 64), Pages: []PageMetadata{{Page: 1, Width: 100, Height: 200, SourceSHA256: strings.Repeat("b", 64), OutputSHA256: strings.Repeat("c", 64)}}}
	if err := v.ValidateFingerprint(); err != nil {
		t.Fatal(err)
	}
	if err := (Receipt{}).ValidateFingerprint(); err != nil {
		t.Fatal("legacy receipt rejected", err)
	}
	for _, bad := range []Receipt{
		{FingerprintPayload: a},
		{ModelSHA256: v.ModelSHA256},
		{FingerprintPayload: a[:60], ModelSHA256: v.ModelSHA256, Pages: v.Pages},
		{FingerprintPayload: a, ModelSHA256: "invalid", Pages: v.Pages},
		{FingerprintPayload: a, ModelSHA256: v.ModelSHA256, Pages: []PageMetadata{{Page: 2, Width: 100, Height: 200, SourceSHA256: strings.Repeat("b", 64), OutputSHA256: strings.Repeat("c", 64)}}},
		{FingerprintPayload: a, ModelSHA256: v.ModelSHA256, Pages: []PageMetadata{{Page: 1, Width: 5000, Height: 5000, SourceSHA256: strings.Repeat("b", 64), OutputSHA256: strings.Repeat("c", 64)}}},
	} {
		if err := bad.ValidateFingerprint(); err == nil {
			t.Fatal("accepted invalid fingerprint metadata")
		}
	}
}
