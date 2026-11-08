package onlinebulletins

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// PrepareRecognitionPages applies the source exclusion boundary, not the legacy
// cover/body/hymn parsers. Re-encoding the pixels removes original PNG metadata.
func PrepareRecognitionPages(source GeometryDocument, rasters map[int][]byte) ([]RecognitionPage, error) {
	if !hashPattern.MatchString(source.SourceSHA256) || source.PopplerVersion != PopplerVersion {
		return nil, ErrRecognition
	}
	allowed, issues, err := filterTemplateV1Geometry(source)
	if err != nil || len(issues) != 0 {
		return nil, ErrRecognition
	}
	parts, err := CatalogParts(allowed)
	if err != nil {
		return nil, ErrRecognition
	}
	pages := make([]RecognitionPage, 0, len(allowed.Pages))
	total := 0
	for index, page := range allowed.Pages {
		raw := rasters[page.Number]
		if len(raw) == 0 || len(raw) > 4<<20 {
			return nil, ErrRecognition
		}
		config, err := png.DecodeConfig(bytes.NewReader(raw))
		if err != nil || config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > 16_000_000 {
			return nil, ErrRecognition
		}
		pixels, err := png.Decode(bytes.NewReader(raw))
		if err != nil {
			return nil, ErrRecognition
		}
		clean := image.NewRGBA(pixels.Bounds())
		draw.Draw(clean, clean.Bounds(), pixels, pixels.Bounds().Min, draw.Src)
		if index == len(allowed.Pages)-1 {
			if page.backContentBottom <= 0 {
				return nil, ErrRecognition
			}
			boundary := int(math.Floor(page.backContentBottom / page.Height * float64(config.Height)))
			draw.Draw(clean, image.Rect(0, boundary, config.Width, config.Height), image.NewUniform(color.White), image.Point{}, draw.Src)
		}
		var encoded bytes.Buffer
		if err := png.Encode(&encoded, clean); err != nil || encoded.Len() > 4<<20 {
			return nil, ErrRecognition
		}
		input := RecognitionPage{SourceSHA256: source.SourceSHA256, Page: page.Number, TotalPages: len(allowed.Pages), Width: page.Width, Height: page.Height, PNG: encoded.Bytes(), Native: []RecognitionText{}}
		input.Frames = recognitionFrames(page)
		lines := baselineSourceLines(page, append([]GeometryText(nil), page.Texts...), index == len(allowed.Pages)-2)
		for _, line := range lines {
			for _, part := range line.parts {
				input.Native = append(input.Native, RecognitionText{ID: fmt.Sprintf("p%d-t%d", page.Number, len(input.Native)+1), Text: part.Text, Box: part.Box, Characters: recognitionCharacterBoxes(part, page.Glyphs)})
			}
		}
		for _, part := range parts {
			if part.Kind == "image" && part.Page == page.Number {
				input.Images = append(input.Images, RecognitionImage{ID: part.ID, Box: part.Box})
			}
		}
		if !validRecognitionPage(input) {
			return nil, ErrRecognition
		}
		total += len(input.PNG)
		if total > 64<<20 {
			return nil, ErrRecognition
		}
		pages = append(pages, input)
	}
	return pages, nil
}

func recognitionFrames(page GeometryPage) []RecognitionFrame {
	var result []RecognitionFrame
	for _, frame := range page.Frames {
		if frame.Element == "bodyHeadingFrame" {
			result = append(result, RecognitionFrame{ID: fmt.Sprintf("p%d-f%d", page.Number, len(result)+1), Box: frame.Box})
		}
	}
	return result
}

// PrepareRecognitionSource runs only in the pinned extractor runtime. Source
// bytes and raster files stay in a private temporary directory until removed.
func PrepareRecognitionSource(ctx context.Context, path string) ([]RecognitionPage, error) {
	dir, err := os.MkdirTemp("", "hhc-recognition-")
	if err != nil {
		return nil, ErrRecognition
	}
	defer os.RemoveAll(dir)
	path, err = freezeRecognitionSource(path, dir)
	if err != nil {
		return nil, ErrRecognition
	}
	source, err := ParseRecognitionPDF(ctx, path)
	if err != nil {
		return nil, ErrRecognition
	}
	// Prove the private final-page boundary before any raster can reach a model.
	if _, issues, err := filterTemplateV1Geometry(source); err != nil || len(issues) != 0 {
		return nil, ErrRecognition
	}
	parser, err := exec.LookPath("pdftohtml")
	if err != nil {
		return nil, ErrRecognition
	}
	renderer := filepath.Join(filepath.Dir(parser), "pdftoppm")
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, ErrRecognition
	}
	rasters := map[int][]byte{}
	for _, page := range source.Pages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		prefix := filepath.Join(dir, fmt.Sprintf("page-%d", page.Number))
		number := strconv.Itoa(page.Number)
		command := exec.CommandContext(ctx, renderer, "-f", number, "-l", number, "-r", "120", "-singlefile", "-png", absolute, prefix)
		command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
		command.Stdout, command.Stderr = &limitedProcessOutput{}, &limitedProcessOutput{}
		if command.Run() != nil {
			return nil, ErrRecognition
		}
		file, err := os.Open(prefix + ".png")
		if err != nil {
			return nil, ErrRecognition
		}
		data, readErr := io.ReadAll(io.LimitReader(file, (4<<20)+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || len(data) > 4<<20 {
			return nil, ErrRecognition
		}
		rasters[page.Number] = data
	}
	return PrepareRecognitionPages(source, rasters)
}

// Parse, hash and rasterize the same private bytes even if the upload path changes.
func freezeRecognitionSource(path, directory string) (string, error) {
	source, err := os.Open(path)
	if err != nil {
		return "", ErrRecognition
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 5 || info.Size() > 20<<20 {
		return "", ErrRecognition
	}
	frozen := filepath.Join(directory, "source.pdf")
	target, err := os.OpenFile(frozen, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", ErrRecognition
	}
	n, copyErr := io.Copy(target, io.LimitReader(source, (20<<20)+1))
	closeErr := target.Close()
	if copyErr != nil || closeErr != nil || n < 5 || n > 20<<20 || os.Chmod(frozen, 0400) != nil {
		return "", ErrRecognition
	}
	return frozen, nil
}
