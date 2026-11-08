package onlinebulletins

import (
	"strings"
	"unicode"
)

// Offsets count Unicode scalars in the immutable native text, not UTF-8 bytes.
type RecognitionSourceRange struct {
	ID    string `json:"id"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

func recognitionReferences(input RecognitionPage, block RecognitionBlock) ([]RecognitionSourceRange, bool) {
	if len(block.SourceIDs) > 0 && len(block.SourceRanges) > 0 {
		return nil, false
	}
	native := make(map[string]int, len(input.Native))
	for i, part := range input.Native {
		native[part.ID] = i
	}
	refs := append([]RecognitionSourceRange(nil), block.SourceRanges...)
	for _, id := range block.SourceIDs {
		index, exists := native[id]
		if !exists {
			return nil, false
		}
		refs = append(refs, RecognitionSourceRange{ID: id, End: len([]rune(input.Native[index].Text))})
	}
	last, end := -1, 0
	for _, ref := range refs {
		index, exists := native[ref.ID]
		if !exists || ref.Start < 0 || ref.End <= ref.Start || ref.End > len([]rune(input.Native[index].Text)) || index < last || index == last && ref.Start < end {
			return nil, false
		}
		last, end = index, ref.End
	}
	return refs, true
}

func recognitionBlockParts(input RecognitionPage, block RecognitionBlock) ([]RecognitionText, bool) {
	refs, ok := recognitionReferences(input, block)
	if !ok {
		return nil, false
	}
	native := make(map[string]RecognitionText, len(input.Native))
	for _, part := range input.Native {
		native[part.ID] = part
	}
	parts := make([]RecognitionText, 0, len(refs))
	for _, ref := range refs {
		part := native[ref.ID]
		points := []rune(part.Text)
		if ref.Start != 0 || ref.End != len(points) {
			if len(part.Characters) != len(points) {
				return nil, false
			}
			var glyphs []GeometryText
			for i := ref.Start; i < ref.End; i++ {
				if unicode.IsSpace(points[i]) {
					continue
				}
				if !validBox(part.Characters[i]) {
					return nil, false
				}
				glyphs = append(glyphs, GeometryText{Box: part.Characters[i]})
			}
			if len(glyphs) == 0 {
				return nil, false
			}
			part.Box = textBounds(glyphs)
			part.Text = string(points[ref.Start:ref.End])
			part.Characters = part.Characters[ref.Start:ref.End]
		}
		parts = append(parts, part)
	}
	return parts, true
}

func recognitionNativeValue(input RecognitionPage, block RecognitionBlock) (string, Box, bool) {
	parts, ok := recognitionBlockParts(input, block)
	if !ok || len(parts) == 0 {
		return "", Box{}, false
	}
	var value strings.Builder
	var geometry []GeometryText
	for _, part := range parts {
		value.WriteString(part.Text)
		geometry = append(geometry, GeometryText{Box: part.Box})
	}
	return value.String(), textBounds(geometry), true
}

func recognitionVisualReferences(input RecognitionPage, block RecognitionBlock, refs []RecognitionSourceRange) bool {
	for _, part := range input.Native {
		points := []rune(part.Text)
		claimed := make([]bool, len(points))
		for _, ref := range refs {
			if ref.ID == part.ID {
				for i := ref.Start; i < ref.End; i++ {
					claimed[i] = true
				}
			}
		}
		if len(part.Characters) == 0 {
			for _, used := range claimed {
				if used != recognitionBoxesOverlap(block.Box, part.Box) {
					return false
				}
			}
			continue
		}
		for i, point := range points {
			if !unicode.IsSpace(point) && claimed[i] != recognitionBoxesOverlap(block.Box, part.Characters[i]) {
				return false
			}
		}
	}
	return true
}
