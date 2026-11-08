package bulletinreader

import (
	"bytes"
	"encoding/json"
	"slices"
	"unicode/utf8"
)

const MaxSegments = 256

// TextRange offsets are half-open Unicode scalar positions, never UTF-16.
type TextRange struct {
	SentenceID string `json:"sentenceId"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
}
type HighlightSegment struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Color string `json:"color"`
}
type NoteRange struct {
	TextRange
	// The complete trusted source sentence proves offsets on later revisions.
	Quote string `json:"quote"`
}

func requestedRanges(snapshot Snapshot, anchors []Anchor, raw json.RawMessage) ([]TextRange, error) {
	if len(raw) == 0 {
		return nil, nil
	} // Legacy whole-sentence request.
	var inputs []struct {
		SentenceID string `json:"sentenceId"`
		Start      *int   `json:"start"`
		End        *int   `json:"end"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&inputs) != nil || len(inputs) == 0 || len(inputs) != len(anchors) {
		return nil, ErrInvalid
	}
	positions := map[string]int{}
	for i, anchor := range snapshot.Sentences {
		positions[anchor.ID] = i
	}
	ranges := make([]TextRange, len(inputs))
	for i, input := range inputs {
		length := utf8.RuneCountInString(anchors[i].Text)
		if input.SentenceID != anchors[i].ID || input.Start == nil || input.End == nil || *input.Start < 0 || *input.Start >= *input.End || *input.End > length || *input.End > 1000000 {
			return nil, ErrInvalid
		}
		if i > 0 && (positions[input.SentenceID] != positions[inputs[i-1].SentenceID]+1 || *input.Start != 0 || ranges[i-1].End != utf8.RuneCountInString(anchors[i-1].Text)) {
			return nil, ErrInvalid
		}
		ranges[i] = TextRange{SentenceID: input.SentenceID, Start: *input.Start, End: *input.End}
	}
	return ranges, nil
}

// Replace only the intersecting interval; retain both sides and merge neighbors.
func replaceSegments(previous Highlight, selected TextRange, color string) ([]HighlightSegment, error) {
	segments := previous.Segments
	if previous.Active && len(segments) == 0 {
		segments = []HighlightSegment{{Start: 0, End: utf8.RuneCountInString(previous.Quote), Color: previous.Color}}
	}
	if !previous.Active {
		segments = nil
	}
	next := []HighlightSegment{}
	for _, part := range segments {
		if part.End <= selected.Start || part.Start >= selected.End {
			next = append(next, part)
			continue
		}
		if part.Start < selected.Start {
			next = append(next, HighlightSegment{part.Start, selected.Start, part.Color})
		}
		if part.End > selected.End {
			next = append(next, HighlightSegment{selected.End, part.End, part.Color})
		}
	}
	if color != "" {
		next = append(next, HighlightSegment{selected.Start, selected.End, color})
	}
	slices.SortFunc(next, func(a, b HighlightSegment) int { return a.Start - b.Start })
	merged := []HighlightSegment{}
	for _, part := range next {
		if n := len(merged); n > 0 && merged[n-1].End == part.Start && merged[n-1].Color == part.Color {
			merged[n-1].End = part.End
		} else {
			merged = append(merged, part)
		}
	}
	if len(merged) > MaxSegments {
		return nil, ErrInvalid
	}
	return merged, nil
}
