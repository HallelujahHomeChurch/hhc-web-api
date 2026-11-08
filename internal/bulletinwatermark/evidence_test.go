package bulletinwatermark

import (
	"bytes"
	"strings"
	"testing"
)

func TestEvidenceBundleRoundTripDeduplicatesAndDropsNames(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), []byte("pixels")...)
	pdf := []byte("%PDF-1.7\nbody\n%%EOF\n")
	bundle, err := PackEvidence([]EvidenceFile{{Name: "member-name.png", Data: png}, {Name: "secret.pdf", Data: pdf}, {Name: "duplicate.png", Data: png}})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bundle, []byte("member-name")) || bytes.Contains(bundle, []byte("secret.pdf")) {
		t.Fatal("client filenames retained")
	}
	files, err := UnpackEvidence(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Kind != EvidencePNG || files[1].Kind != EvidencePDF || !bytes.Equal(files[1].Data, pdf) {
		t.Fatalf("unexpected files: %#v", files)
	}
}

func TestEvidenceBundleAcceptsLegacyImageAndRejectsBounds(t *testing.T) {
	jpeg := append([]byte("\xff\xd8\xff"), bytes.Repeat([]byte("x"), 16)...)
	files, err := UnpackEvidence(jpeg)
	if err != nil || len(files) != 1 || files[0].Kind != EvidenceJPEG {
		t.Fatalf("legacy image rejected: %#v %v", files, err)
	}
	if _, err = PackEvidence(nil); err == nil {
		t.Fatal("empty bundle accepted")
	}
	tooMany := make([]EvidenceFile, MaxEvidenceFiles+1)
	for i := range tooMany {
		tooMany[i] = EvidenceFile{Data: append([]byte("%PDF-"), byte(i), '%', '%', 'E', 'O', 'F')}
	}
	if _, err = PackEvidence(tooMany); err == nil {
		t.Fatal("too many files accepted")
	}
	if _, err = PackEvidence([]EvidenceFile{{Data: []byte("plain text")}}); err == nil {
		t.Fatal("unsupported evidence accepted")
	}
	if _, err = PackEvidence([]EvidenceFile{{Data: []byte("%PDF-" + strings.Repeat("x", MaxEvidenceBytes) + "%%EOF")}}); err == nil {
		t.Fatal("oversize evidence accepted")
	}
}

func TestEvidenceBundleAcceptsFiveUniqueFilesAndRejectsSix(t *testing.T) {
	files := make([]EvidenceFile, MaxEvidenceFiles)
	for i := range files {
		files[i] = EvidenceFile{Data: append([]byte("%PDF-1.7\n"), byte('0'+i), '\n', '%', '%', 'E', 'O', 'F')}
	}
	bundle, err := PackEvidence(files)
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnpackEvidence(bundle)
	if err != nil || len(got) != MaxEvidenceFiles {
		t.Fatalf("five-file boundary: count=%d err=%v", len(got), err)
	}
	if _, err = PackEvidence(append(files, EvidenceFile{Data: []byte("%PDF-1.7\n6\n%%EOF")})); err == nil {
		t.Fatal("sixth file accepted")
	}
}

func TestEvidenceBundleCountsDuplicateBytesOnce(t *testing.T) {
	chunk := append([]byte("\x89PNG\r\n\x1a\n"), bytes.Repeat([]byte("x"), MaxEvidenceBytes-8)...)
	bundle, err := PackEvidence([]EvidenceFile{{Data: chunk}, {Data: append([]byte(nil), chunk...)}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := UnpackEvidence(bundle)
	if err != nil || len(got) != 1 {
		t.Fatalf("duplicate normalization: count=%d err=%v", len(got), err)
	}
}
