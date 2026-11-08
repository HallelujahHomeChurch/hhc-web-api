package onlinebulletins

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func recognitionRasters(t *testing.T, count int) map[int][]byte {
	t.Helper()
	pixels := image.NewRGBA(image.Rect(0, 0, 20, 40))
	for y := range 40 {
		for x := range 20 {
			pixels.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, pixels); err != nil {
		t.Fatal(err)
	}
	result := map[int][]byte{}
	for page := 1; page <= count; page++ {
		result[page] = encoded.Bytes()
	}
	return result
}

func TestRecognitionSourceIncludesUnfamiliarCoverBeforeSemanticParsing(t *testing.T) {
	source := candidateGeometry()
	for i := range source.Pages[0].Texts {
		if source.Pages[0].Texts[i].Text == "本週金句" {
			source.Pages[0].Texts[i].Text = "其他標籤"
		}
	}
	source.Pages[0].Texts = append(source.Pages[0].Texts, GeometryText{Text: "(羅八/33-35、/37)", FontFamily: "MingLiU", FontSize: 10, Box: Box{X: .2, Y: .88, Width: .5, Height: .01}})
	pages, err := PrepareRecognitionPages(source, recognitionRasters(t, 4))
	if err != nil || len(pages) != 4 {
		t.Fatal("legacy semantic parser still blocks input", err)
	}
	var text strings.Builder
	for _, page := range pages {
		for _, part := range page.Native {
			text.WriteString(part.Text)
		}
	}
	if !strings.Contains(text.String(), "(羅八/33-35、/37)") || !strings.Contains(text.String(), "KEEP_body") {
		t.Fatal("full content missing")
	}
	for _, private := range []string{"DROP_roster", "DROP_attendance", "DROP_offerings", "主日敬拜服事表"} {
		if strings.Contains(text.String(), private) {
			t.Fatal("excluded source reached recognition", private)
		}
	}
	image, err := png.Decode(bytes.NewReader(pages[3].PNG))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := image.At(1, 39).RGBA()
	if r != 65535 || g != 65535 || b != 65535 {
		t.Fatal("excluded pixels survived")
	}
	r, g, b, _ = image.At(1, 1).RGBA()
	if r != 65535 || g != 0 || b != 0 {
		t.Fatal("allowed source image changed")
	}
	if bytes.Equal(pages[3].PNG, recognitionRasters(t, 4)[4]) {
		t.Fatal("unredacted image copied")
	}
	if strings.Contains(source.Pages[3].Texts[len(source.Pages[3].Texts)-1].Text, "DROP_") == false {
		t.Fatal("input evidence mutated")
	}
}

func TestRecognitionSourceRejectsUnknownPrivacyBoundaryAndCrossingContent(t *testing.T) {
	for _, name := range []string{"missing_anchor", "crossing_text", "crossing_image", "missing_page_image", "invalid_image", "invalid_text_box"} {
		t.Run(name, func(t *testing.T) {
			source, rasters := candidateGeometry(), recognitionRasters(t, 4)
			switch name {
			case "missing_anchor":
				for i := range source.Pages[3].Texts {
					if source.Pages[3].Texts[i].Text == "主日敬拜服事表" {
						source.Pages[3].Texts[i].Text = "unknown"
					}
				}
			case "crossing_text":
				source.Pages[3].Texts = append(source.Pages[3].Texts, GeometryText{Text: "PRIVATE", FontSize: 12, Box: Box{X: .2, Y: 465 / templateV1Height, Width: .4, Height: .04}})
			case "crossing_image":
				source.Pages[3].Images = append(source.Pages[3].Images, GeometryImage{Box: Box{X: .2, Y: .5, Width: .4, Height: .3}})
			case "missing_page_image":
				delete(rasters, 2)
			case "invalid_image":
				rasters[1] = []byte("invalid")
			case "invalid_text_box":
				source.Pages[0].Texts[0].Box.X = -1
			}
			if _, err := PrepareRecognitionPages(source, rasters); err == nil {
				t.Fatal("unverified source accepted")
			}
		})
	}
}

func TestRecognitionFreezesSourceBeforeParsingAndRasterization(t *testing.T) {
	root := t.TempDir()
	original := filepath.Join(root, "upload.pdf")
	if err := os.WriteFile(original, []byte("%PDF-original-private-source"), 0600); err != nil {
		t.Fatal(err)
	}
	frozen, err := freezeRecognitionSource(original, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("%PDF-replaced-with-different-private-layout"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(frozen)
	if err != nil || string(data) != "%PDF-original-private-source" {
		t.Fatal("source snapshot changed with original path", err)
	}
	info, err := os.Stat(frozen)
	if err != nil || info.Mode().Perm() != 0400 {
		t.Fatal("source snapshot is not private and read-only", err)
	}
	if _, err := freezeRecognitionSource(root, t.TempDir()); err == nil {
		t.Fatal("non-file source accepted")
	}
}

func TestRecognitionNativeOrderUsesVisualBaselineBeforeIDs(t *testing.T) {
	source := candidateGeometry()
	// Smaller inline type starts lower; top-only sorting would reverse the words.
	source.Pages[0].Texts = []GeometryText{
		{Text: "後半", FontFamily: "MingLiU", FontSize: 18, Box: Box{X: .4, Y: .2, Width: .1, Height: .03}},
		{Text: "前半", FontFamily: "MingLiU", FontSize: 5, Box: Box{X: .2, Y: .21, Width: .1, Height: .015}},
	}
	pages, err := PrepareRecognitionPages(source, recognitionRasters(t, 4))
	if err != nil || len(pages[0].Native) != 2 || pages[0].Native[0].Text != "前半" || pages[0].Native[1].Text != "後半" {
		t.Fatal("inline font metrics changed native reading order", err)
	}
	if pages[0].Native[0].ID != "p1-t1" || pages[0].Native[1].ID != "p1-t2" {
		t.Fatal("IDs were assigned before authoritative ordering")
	}
}

func TestPrivateRecognitionRetainsBorderEvidenceWithoutSemanticParsing(t *testing.T) {
	path := os.Getenv("HHC_PRIVATE_PDF_SOURCE")
	if path == "" {
		t.Skip("private source not supplied")
	}
	source, err := ParseRecognitionPDF(context.Background(), path)
	if err != nil || len(source.Pages) < 4 {
		t.Fatal("full-source parser failed", err)
	}
	frames := 0
	for _, page := range source.Pages {
		frames += len(page.Frames)
	}
	if frames == 0 {
		t.Fatal("source borders were discarded before the LLM could reference them")
	}
}
