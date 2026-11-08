package onlinebulletins

import (
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Canonical CMS fields stay read-only. A PDF mismatch is review evidence, never
// an instruction to replace CMS metadata or an excuse to estimate glyph boxes.
func coverMetadataSlots(page GeometryPage, metadata CanonicalMetadata) ([]FixedSlot, []ReviewIssue, error) {
	date, err := time.Parse("2006-01-02", metadata.Date)
	if err != nil || page.Number != 1 || !boundedText(metadata.Title, true) || !boundedText(metadata.Subtitle, false) || metadata.IssueNumber < 1 {
		return nil, nil, ErrUnsupportedSource
	}
	lines, err := sourceLines(page, false)
	if err != nil {
		return nil, nil, err
	}
	fields := []struct{ element, text string }{
		{"title", metadata.Title}, {"subtitle", metadata.Subtitle},
		{"date", date.Format("Jan.2.2006")}, {"issueNumber", "第" + strconv.Itoa(metadata.IssueNumber) + "期"},
	}
	slots, issues := []FixedSlot{}, []ReviewIssue{}
	for _, field := range fields {
		if field.text == "" {
			continue
		}
		var matched []GeometryText
		for _, line := range lines {
			if textBounds(line.parts).Y > .28 {
				continue
			}
			parts := wholeSourceRuns(line.parts, field.text)
			if len(parts) == 0 && field.element == "date" {
				parts = wholeSourceRuns(line.parts, date.Format("Jan.02.2006"))
			}
			if len(parts) == 0 {
				continue
			}
			if len(matched) != 0 {
				return nil, nil, ErrUnsupportedSource
			}
			matched = parts
		}
		if len(matched) == 0 {
			issues = append(issues, ReviewIssue{Code: "canonical_source_mismatch", ComponentID: "cover", Blocking: true})
			continue
		}
		box := textBounds(matched)
		size := 0.0
		for _, part := range matched {
			size = math.Max(size, part.FontSize)
		}
		style := ParagraphStyle{FontSize: size, LineHeight: math.Max(size, box.Height*page.Height)}
		if field.element == "title" {
			style.LetterSpacing = sourceCJKTitleTracking(matched, page.Width, size)
		}
		slots = append(slots, FixedSlot{ID: "fixed-cover-" + field.element, Element: field.element, Box: box, Style: style})
	}
	return slots, issues, nil
}

// Reviewed Word-template XingShu CJK titles use full-em advances and explicit
// tracking, not a smaller font. Capture their aggregate source spacing once;
// never recompute it from later canonical edits or assume Latin glyph advances.
func sourceCJKTitleTracking(parts []GeometryText, width, size float64) float64 {
	count := 0
	for _, part := range parts {
		if part.FontSize != size || !strings.Contains(strings.ToLower(part.FontFamily), "xingshu") {
			return 0
		}
		for _, point := range part.Text {
			if !unicode.Is(unicode.Han, point) && (point < 0x3000 || point > 0x303f) {
				return 0
			}
			count++
		}
	}
	if count < 2 {
		return 0
	}
	spacing := (textBounds(parts).Width*width - float64(count)*size) / (float64(count-1) * size)
	if !finite(spacing) || spacing < -.5 || spacing > 1 {
		return 0
	}
	return spacing
}

// Prefer complete native WordArt runs; outlined foregrounds fall back to shadow
// bands. Both propose canonical placement without approving source images or
// bypassing the independent whole-page visual gate.
func bodyCanonicalArtSlots(page GeometryPage, metadata CanonicalMetadata) ([]FixedSlot, []ReviewIssue) {
	issues := []ReviewIssue{{Code: "inferred_body_art_geometry", SourcePage: page.Number, Blocking: true}}
	if page.Number != 2 || !finite(page.Width) || !finite(page.Height) || page.Width <= 0 || page.Height <= 0 || !boundedText(metadata.Title, true) || !boundedText(metadata.Subtitle, false) {
		return nil, issues
	}
	lines, err := sourceLines(page, false)
	if err != nil {
		return nil, issues
	}
	slots := []FixedSlot{}
	for _, field := range []struct {
		element, text                    string
		minY, maxY, minHeight, maxHeight float64
	}{
		{"title", metadata.Title, 55, 100, 35, 80},
		{"subtitle", metadata.Subtitle, 160, 200, 20, 45},
	} {
		if field.text == "" {
			continue
		}
		var native []GeometryText
		for _, line := range lines {
			if textBounds(line.parts).Y*page.Height >= 272 {
				continue
			}
			for start := range line.parts {
				parts := wholeSourceRuns(line.parts[start:], field.text)
				if len(parts) == 0 {
					break
				}
				if &parts[0] != &line.parts[start] || parts[0].FontSize <= 48 {
					continue
				}
				if native != nil {
					return nil, issues
				}
				native = parts
			}
		}
		if native != nil {
			box := textBounds(native)
			size := 0.0
			for _, part := range native {
				size = math.Max(size, part.FontSize)
			}
			// WordArt can exceed paragraph limits; the measured renderer fits canonical text to its observed box.
			size = math.Min(size, 96)
			style := ParagraphStyle{FontSize: size, LineHeight: math.Max(size, box.Height*page.Height)}
			if !validBox(box) || !validParagraphStyle(style) {
				return nil, issues
			}
			slots = append(slots, FixedSlot{sourcePage: 2, ID: "fixed-body-" + field.element, Element: field.element, Box: box, Style: style})
			continue
		}
		var shadow *GeometryImage
		layers := []Box{}
		seenLayers := map[Box]bool{}
		for i := range page.Images {
			image := &page.Images[i]
			y, height := image.Box.Y*page.Height, image.Box.Height*page.Height
			if !validBox(image.Box) || y < field.minY || y > field.maxY || height < field.minHeight || height > field.maxHeight {
				continue
			}
			if seenLayers[image.Box] {
				return nil, issues
			}
			seenLayers[image.Box] = true
			layers = append(layers, image.Box)
			if image.Box.Width < .2 {
				continue
			}
			if shadow != nil && image.Box.Width == shadow.Box.Width {
				return nil, issues
			}
			if shadow == nil || image.Box.Width > shadow.Box.Width {
				shadow = image
			}
		}
		if shadow == nil || len(layers) > 12 || !connectedArtworkLayers(layers) {
			return nil, issues
		}
		// Connected WordArt fragments share one horizontal envelope, not just the widest fragment.
		left, right := shadow.Box.X, shadow.Box.X+shadow.Box.Width
		for _, layer := range layers {
			left, right = math.Min(left, layer.X), math.Max(right, layer.X+layer.Width)
		}
		// ponytail: V1 proposal only; verified vector geometry replaces this
		// shadow-to-foreground profile before automatic publication is enabled.
		size := shadow.Box.Height * page.Height * .8
		shift := size * .35 / page.Width
		box := Box{X: left + shift, Y: shadow.Box.Y - size/page.Height, Width: right - left - shift, Height: size * 2 / page.Height}
		if field.element == "title" {
			for _, part := range page.Texts {
				if strings.TrimSpace(part.Text) == strconv.Itoa(page.Number-1) && part.Box.Y*page.Height < 65 && part.Box.X < box.X+box.Width && part.Box.X+part.Box.Width > box.X {
					// Native title ink must stay below the observed page number, not its WordArt shadow.
					box.Y = math.Max(box.Y, part.Box.Y+part.Box.Height+2/page.Height)
				}
			}
		}
		units, count := 0.0, 0
		for _, point := range field.text {
			units += .5
			// Native bold Latin/digits are wider than half an em. This remains
			// a proposal; the pinned renderer verifies actual glyph fitting.
			if point >= '0' && point <= '9' || point >= 'A' && point <= 'Z' || point >= 'a' && point <= 'z' {
				units += .15
			}
			if unicode.Is(unicode.Han, point) || point >= 0x3000 && point <= 0xffef {
				units += .5
			}
			count++
		}
		style := ParagraphStyle{FontSize: size, LineHeight: size * 2}
		if count > 1 {
			// CSS includes trailing tracking; reserve one point for rounding.
			style.LetterSpacing = (box.Width*page.Width - 1 - units*size) / (float64(count) * size)
			if style.LetterSpacing < -.5 {
				// Mixed-size outlined titles need a smaller proposal, not missing canonical fields.
				size = (box.Width*page.Width - 1) / (units - .5*float64(count))
				if !finite(size) || size < 12 {
					return nil, issues
				}
				style.FontSize, style.LetterSpacing = size, -.5
			}
		}
		if !validBox(box) || !validParagraphStyle(style) {
			return nil, issues
		}
		slots = append(slots, FixedSlot{sourcePage: 2, ID: "fixed-body-" + field.element, Element: field.element, Box: box, Style: style})
	}
	slots = append(slots, FixedSlot{sourcePage: 2, ID: "fixed-body-topRule", Element: "topRule", Box: Box{X: 68.064 / page.Width, Y: 269.8 / page.Height, Width: 456.5 / page.Width, Height: .75 / page.Height}, Style: ParagraphStyle{FontSize: 6, LineHeight: 6}})
	return slots, issues
}

// Only complete, contiguous source runs qualify. Splitting a run by character
// count would invent positions for proportional and kerned glyphs.
func wholeSourceRuns(parts []GeometryText, target string) []GeometryText {
	target = strings.Join(strings.Fields(target), "")
	if target == "" {
		return nil
	}
	for start := range parts {
		text := ""
		for end := start; end < len(parts); end++ {
			text += strings.Join(strings.Fields(parts[end].Text), "")
			if text == target {
				return parts[start : end+1]
			}
			if !strings.HasPrefix(target, text) {
				break
			}
		}
	}
	return nil
}
