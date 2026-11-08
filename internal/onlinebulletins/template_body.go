package onlinebulletins

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Body headings without a semantic source tag are proposals, never publication
// evidence. Preserve all text and require classification/review for every kind.
type bodyMapping struct {
	components     []Component
	slots          []Slot
	fixed          []FixedSlot
	header         []geometryLine
	issues         []ReviewIssue
	framedSpeakers map[string]bool
}

func bodyComponents(pages []GeometryPage) (bodyMapping, error) {
	if len(pages) == 0 || len(pages) > 38 {
		return bodyMapping{}, ErrUnsupportedSource
	}
	lines, header := []geometryLine{}, []geometryLine{}
	fixed, issues := []FixedSlot{}, []ReviewIssue{}
	left := 1.0
	frames := map[int][]GeometryFrame{}
	for index, page := range pages {
		if page.Number != index+2 || !supportedTemplatePaper(page) {
			return bodyMapping{}, ErrUnsupportedSource
		}
		frames[page.Number] = page.Frames
		source, err := sourceLines(page, false)
		if err != nil {
			return bodyMapping{}, fmt.Errorf("%w: body_lines_page_%d", err, page.Number)
		}
		pageNumberFound := false
		for _, line := range source {
			box := textBounds(line.parts)
			if page.Number == 2 && box.Y < 272/templateV1Height && line.parts[0].FontSize > 48 {
				header = append(header, line)
				continue
			}
			printed, numberErr := strconv.Atoi(strings.TrimSpace(lineText(line)))
			if numberErr == nil && bodyPageNumberPlacement(line, page) {
				if printed != page.Number-1 || pageNumberFound {
					return bodyMapping{}, fmt.Errorf("%w: body_header_page_%d", ErrUnsupportedSource, page.Number)
				}
				pageNumberFound = true
				size := 0.0
				for _, part := range line.parts {
					size = math.Max(size, part.FontSize)
				}
				fixed = append(fixed, FixedSlot{sourcePage: page.Number, ID: fmt.Sprintf("fixed-body-page-%d-number", page.Number), Element: "pageNumber", Box: box, Style: ParagraphStyle{FontSize: size, LineHeight: math.Max(size, box.Height*page.Height)}})
				continue
			}
			if page.Number == 2 && box.Y < 272/templateV1Height {
				header = append(header, line) // Canonical word-art and contributor band are handled separately.
				continue
			}
			left = math.Min(left, box.X)
			lines = append(lines, line)
		}
		if !pageNumberFound {
			issues = append(issues, ReviewIssue{Code: "missing_source_page_number", SourcePage: page.Number, Blocking: true})
		}
	}
	if len(lines) < 2 {
		return bodyMapping{}, ErrUnsupportedSource
	}
	components, slots := []Component{}, []Slot{}
	framedSpeakers := map[string]bool{}
	paragraph := []geometryLine{}
	ordinal := 1
	flush := func() error {
		if len(paragraph) == 0 {
			return nil
		}
		component := &components[len(components)-1]
		ordinal++
		block, placements, findings, err := paragraphBlock(component.ID, ordinal, paragraph)
		if err != nil {
			return fmt.Errorf("%w: body_paragraph_%d_page_%d", err, ordinal, paragraph[0].page)
		}
		component.BodySection.Blocks = append(component.BodySection.Blocks, block)
		slots = append(slots, placements...)
		issues = append(issues, findings...)
		paragraph = nil
		return nil
	}
	for index, line := range lines {
		box := textBounds(line.parts)
		text := strings.TrimSpace(lineText(line))
		em := line.parts[0].FontSize / line.width
		indented := box.X-left > em*1.2
		heading := index == 0
		framedTitle, framedTail, framed := framedBodyHeading(line, frames[line.page])
		proposed, inferred := proposedCJKHeadingRuns(line)
		framedEstimate := framed && len(framedTitle)+len(framedTail) != len(line.parts)
		heading = heading || framed
		nextIndented := index+1 < len(lines) && textBounds(lines[index+1].parts).X-box.X > em*1.2
		if !heading && math.Abs(box.X-left) < .006 && nextIndented && utf8.RuneCountInString(text) <= 40 && !strings.ContainsAny(text, "。！？?!：:") {
			heading = true
			for _, part := range line.parts {
				role, known := sourceFontRole(part.FontFamily)
				if known && role != "emphasis" {
					heading = false
				}
			}
		}
		if heading {
			if err := flush(); err != nil {
				return bodyMapping{}, err
			}
			if len(components) >= 250 || (len(components) > 0 && len(components[len(components)-1].BodySection.Blocks) == 0) {
				return bodyMapping{}, fmt.Errorf("%w: adjacent_body_headings", ErrUnsupportedSource)
			}
			id := "body-section-" + strconv.Itoa(len(components)+1)
			ordinal = 1
			titleLine := line
			if inferred {
				titleLine.parts = proposed
			}
			if inferred || framedEstimate {
				issues = append(issues, ReviewIssue{Code: "inferred_inline_speaker_geometry", ComponentID: id, Blocking: true})
			}
			var speakerParts []GeometryText
			var separator *GeometryText
			headingParts := titleLine.parts
			if framed {
				titleLine.parts = framedTitle
				if len(framedTail) > 1 && isSpeakerSeparator(framedTail[0].Text) {
					separator = &framedTail[0]
					speakerParts = framedTail[1:]
				} else if len(framedTail) > 0 {
					// Keep uncertain exterior text rather than silently dropping it.
					titleLine = line
					issues = append(issues, ReviewIssue{Code: "ambiguous_inline_speaker", ComponentID: id, Blocking: true})
				}
			}
			for run, part := range headingParts {
				if framed {
					break
				}
				if isSpeakerSeparator(part.Text) && run > 0 && run+1 < len(headingParts) {
					if separator != nil {
						separator, speakerParts = nil, nil
						titleLine = line
						break
					}
					separator = &headingParts[run]
					speakerParts = headingParts[run+1:]
					titleLine.parts = headingParts[:run]
				}
			}
			titleBox := textBounds(titleLine.parts)
			titleLine.right = titleBox.X + titleBox.Width
			title, placements, findings, err := paragraphBlock(id, ordinal, []geometryLine{titleLine})
			if err != nil {
				return bodyMapping{}, err
			}
			title.Style.Align = "left"
			components = append(components, Component{ID: id, Type: "bodySection", BodySection: &BodySection{Kind: "unknown", Title: title, Blocks: []Block{}}})
			slots = append(slots, placements...)
			issues = append(issues, findings...)
			if separator != nil {
				if framed {
					separators := 0
					for _, char := range text {
						if strings.ContainsRune("～〜~", char) {
							separators++
						}
					}
					framedSpeakers[id] = separators == 1
				}
				speakerLine := line
				speakerLine.parts = speakerParts
				speakerBox := textBounds(speakerParts)
				speakerLine.right = speakerBox.X + speakerBox.Width
				ordinal++
				name, placements, findings, err := paragraphBlock(id, ordinal, []geometryLine{speakerLine})
				if err != nil {
					return bodyMapping{}, err
				}
				name.Style.Align = "left"
				components[len(components)-1].BodySection.Contributors = []Contributor{{Role: "speaker", Name: name}}
				slots = append(slots, placements...)
				issues = append(issues, findings...)
				fixed = append(fixed, FixedSlot{sourcePage: line.page, ID: "fixed-" + id + "-speakerSeparator", Element: "speakerSeparator", Box: separator.Box, Style: ParagraphStyle{FontSize: separator.FontSize, LineHeight: math.Max(separator.FontSize, separator.Box.Height*line.height)}})
			} else if !framed && strings.ContainsAny(text, "～〜~") {
				issues = append(issues, ReviewIssue{Code: "ambiguous_inline_speaker", ComponentID: id, Blocking: true})
			}
			if index != 0 && !framed {
				issues = append(issues, ReviewIssue{Code: "ambiguous_body_heading", ComponentID: id, Blocking: true})
			}
			continue
		}
		if indented && len(paragraph) > 0 {
			if err := flush(); err != nil {
				return bodyMapping{}, err
			}
		}
		paragraph = append(paragraph, line)
	}
	if err := flush(); err != nil {
		return bodyMapping{}, err
	}
	if len(components[len(components)-1].BodySection.Blocks) == 0 {
		return bodyMapping{}, ErrUnsupportedSource
	}
	for index := range issues {
		switch issues[index].Code {
		case "inferred_inline_speaker_geometry", "ambiguous_inline_speaker", "ambiguous_body_heading":
			for _, component := range components {
				if component.ID != issues[index].ComponentID {
					continue
				}
				title := component.BodySection.Title
				issues[index].BlockID = title.ID
				if len(title.Sentences) > 0 {
					issues[index].SentenceID = title.Sentences[0].ID
					if title.Sentences[0].Source != nil {
						issues[index].SourcePage = title.Sentences[0].Source.Page
					}
				}
			}
		}
	}
	return bodyMapping{components: components, slots: slots, fixed: fixed, header: header, issues: issues, framedSpeakers: framedSpeakers}, nil
}

func isSpeakerSeparator(text string) bool {
	text = strings.TrimSpace(text)
	return text == "～" || text == "〜" || text == "~"
}

// A closed source frame is stronger evidence than font, punctuation or indent.
// Runs crossing the frame remain untouched until glyph bounds can resolve them.
func framedBodyHeading(line geometryLine, frames []GeometryFrame) ([]GeometryText, []GeometryText, bool) {
	for _, frame := range frames {
		if frame.Element != "bodyHeadingFrame" || !validBox(frame.Box) {
			continue
		}
		var inside, outside []GeometryText
		valid := true
		for _, part := range framedHeadingRuns(line, frame.Box) {
			center := part.Box.Y + part.Box.Height/2
			if center < frame.Box.Y || center > frame.Box.Y+frame.Box.Height || part.Box.X < frame.Box.X-.002 {
				valid = false
				break
			}
			// Word includes trailing blank advances in the text run's width.
			blanks := utf8.RuneCountInString(part.Text) - utf8.RuneCountInString(strings.TrimRightFunc(part.Text, unicode.IsSpace))
			right := part.Box.X + part.Box.Width - float64(blanks)*part.FontSize/line.width
			if right <= frame.Box.X+frame.Box.Width+.002 && len(outside) == 0 {
				inside = append(inside, part)
			} else if part.Box.X >= frame.Box.X+frame.Box.Width-.002 {
				outside = append(outside, part)
			} else {
				valid = false
				break
			}
		}
		if valid && len(inside) > 0 {
			return inside, outside, true
		}
	}
	return nil, nil, false
}

// Split only when a full-width title prefix ends at the measured frame edge.
// The proportional speaker suffix keeps its remaining run bounds; estimates
// still require review and never masquerade as measured glyph geometry.
func framedHeadingRuns(line geometryLine, frame Box) []GeometryText {
	parts := []GeometryText{}
	for _, part := range line.parts {
		points := []rune(part.Text)
		marker := -1
		em := part.Box.Height * line.height / line.width
		advance := 0.0
		fullWidth := func(points []rune) bool {
			for _, point := range points {
				if point != '～' && point != '〜' && !unicode.In(point, unicode.Han) && !(unicode.IsPunct(point) && point >= 0x3000) {
					return false
				}
			}
			return true
		}
		for i, point := range points {
			if (point != '～' && point != '〜') || i == 0 {
				continue
			}
			for _, candidate := range []struct {
				valid  bool
				offset float64
			}{
				{fullWidth(points[:i]), float64(i) * em},
				{fullWidth(points[i:]), part.Box.Width - float64(len(points)-i)*em},
			} {
				if candidate.valid && math.Abs((part.Box.X+candidate.offset-frame.X-frame.Width)*line.width) < 1.5 {
					marker, advance = i, candidate.offset
					break
				}
			}
			if marker >= 0 {
				break
			}
		}
		if marker < 0 || advance <= 0 || advance+em > part.Box.Width+.001 {
			parts = append(parts, part)
			continue
		}
		for _, span := range [][2]int{{0, marker}, {marker, marker + 1}, {marker + 1, len(points)}} {
			if span[0] == span[1] {
				continue
			}
			piece := part
			piece.Text = string(points[span[0]:span[1]])
			if span[0] == 0 {
				piece.Box.Width = advance
			} else {
				piece.Box.X += advance + float64(span[0]-marker)*em
				piece.Box.Width = em
			}
			if span[1] == len(points) {
				piece.Box.Width = part.Box.X + part.Box.Width - piece.Box.X
			}
			parts = append(parts, piece)
		}
	}
	return parts
}

// Some Word exports merge a CJK title, separator and speaker into one run.
// This is a review-only grid proposal, not evidence of individual glyph bounds.
func proposedCJKHeadingRuns(line geometryLine) ([]GeometryText, bool) {
	parts := []GeometryText{}
	inferred := false
	for _, part := range line.parts {
		points := []rune(part.Text)
		marker := -1
		grid := len(points) >= 3 && len(points) <= 128 && strings.Count(part.Text, "～") == 1
		for index, point := range points {
			if point == '～' {
				marker = index
			} else if !unicode.In(point, unicode.Han) && !(unicode.IsPunct(point) && point >= 0x3000) {
				grid = false
			}
		}
		if !grid || marker <= 0 || marker >= len(points)-1 || math.Abs(part.Box.Width*line.width/(float64(len(points))*part.FontSize)-1) > .01 {
			parts = append(parts, part)
			continue
		}
		for _, span := range [][2]int{{0, marker}, {marker, marker + 1}, {marker + 1, len(points)}} {
			piece := part
			piece.Text = string(points[span[0]:span[1]])
			piece.Box.X += part.Box.Width * float64(span[0]) / float64(len(points))
			piece.Box.Width = part.Box.Width * float64(span[1]-span[0]) / float64(len(points))
			parts = append(parts, piece)
		}
		inferred = true
	}
	return parts, inferred
}

// Page-number bands are separate from the left-aligned prose, including numeric prose.
func bodyPageNumberPlacement(line geometryLine, page GeometryPage) bool {
	box := textBounds(line.parts)
	top := box.Y * page.Height
	center := box.X + box.Width/2
	if top < 20 || top > 52 || box.Width > .045 || !(center >= .45 && center <= .55 || center >= .80 && center <= .90) {
		return false
	}
	for _, part := range line.parts {
		if part.FontSize < 8 || part.FontSize > 12 {
			return false
		}
	}
	return true
}
