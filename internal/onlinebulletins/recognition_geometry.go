package onlinebulletins

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"os/exec"
	"sort"
	"time"
	"unicode"
	"unicode/utf8"
)

func loadRecognitionGlyphs(ctx context.Context, path string, source *GeometryDocument) error {
	command := exec.CommandContext(ctx, "/opt/bulletin-fonts/glyphs", path)
	command.WaitDelay = time.Second
	command.Env = []string{"PATH=/usr/bin:/bin", "LC_ALL=C"}
	output, diagnostic := &limitedProcessOutput{limit: 32 << 20}, &limitedProcessOutput{}
	command.Stdout, command.Stderr = output, diagnostic
	if command.Run() != nil || output.overflow || diagnostic.overflow {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrInvalidGeometry
	}
	return decodeRecognitionGlyphs(output.Bytes(), source)
}

func decodeRecognitionGlyphs(raw []byte, source *GeometryDocument) error {
	var evidence struct {
		Pages []struct {
			Page          int
			Width, Height float64
			Glyphs        []GeometryGlyph
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if len(raw) > 32<<20 || decoder.Decode(&evidence) != nil || decoder.Decode(new(any)) != io.EOF || len(evidence.Pages) != len(source.Pages) {
		return ErrInvalidGeometry
	}
	count := 0
	for index, page := range evidence.Pages {
		original := source.Pages[index]
		if page.Page != original.Number || !finitePositive(page.Width) || !finitePositive(page.Height) || math.Abs(page.Width-original.Width) > .02 || math.Abs(page.Height-original.Height) > .02 || len(page.Glyphs) > 65536 {
			return ErrInvalidGeometry
		}
		count += len(page.Glyphs)
		if count > 250000 {
			return ErrGeometryLimits
		}
		for _, glyph := range page.Glyphs {
			if !utf8.ValidString(glyph.Text) || utf8.RuneCountInString(glyph.Text) != 1 || !validBox(glyph.Box) {
				return ErrInvalidGeometry
			}
		}
	}
	for index, page := range evidence.Pages {
		source.Pages[index].Glyphs = page.Glyphs
	}
	return nil
}

func recognitionCharacterBoxes(part GeometryText, glyphs []GeometryGlyph) []Box {
	var selected []GeometryGlyph
	for _, glyph := range glyphs {
		centerX, centerY := glyph.Box.X+glyph.Box.Width/2, glyph.Box.Y+glyph.Box.Height/2
		if centerX >= part.Box.X-.001 && centerX <= part.Box.X+part.Box.Width+.001 && centerY >= part.Box.Y-.003 && centerY <= part.Box.Y+part.Box.Height+.003 {
			selected = append(selected, glyph)
		}
	}
	sort.SliceStable(selected, func(i, j int) bool { return selected[i].Box.X < selected[j].Box.X })
	points := []rune(part.Text)
	boxes := make([]Box, len(points))
	n := 0
	for index, point := range points {
		if unicode.IsSpace(point) {
			continue
		}
		if n >= len(selected) || selected[n].Text != string(point) {
			return nil
		}
		box := selected[n].Box
		if box.X < part.Box.X-.005 || box.X+box.Width > part.Box.X+part.Box.Width+.005 {
			return nil
		}
		// Poppler's XML line box supplies vertical metrics; glyphs supply advances.
		boxes[index] = Box{X: box.X, Y: part.Box.Y, Width: box.Width, Height: part.Box.Height}
		n++
	}
	if n != len(selected) || n == 0 {
		return nil
	}
	return boxes
}
