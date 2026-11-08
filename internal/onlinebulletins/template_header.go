package onlinebulletins

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var lectureHeader = regexp.MustCompile(`^‧([A-Z][a-z]{2}\.[0-9]{1,2}\.[0-9]{4})[（(]([0-9]+)共([0-9]+)頁[）)]$`)

type bodyHeaderMapping struct {
	header *BodyHeader
	slots  []Slot
	fixed  []FixedSlot
	issues []ReviewIssue
}

// Header geometry that cuts a merged font run is a review-only proposal. It
// never supplies layout proof, even when its fixed V1 source band is recognized.
func bodyHeaderComponent(lines []geometryLine, metadata CanonicalMetadata, pageCount int, componentID string) (bodyHeaderMapping, error) {
	unsupported := func(code string) bodyHeaderMapping {
		return bodyHeaderMapping{issues: []ReviewIssue{{Code: code, ComponentID: componentID, Blocking: true}}}
	}
	fields := make([]geometryLine, 4)
	for _, line := range lines {
		if line.page != 2 {
			return unsupported("unmapped_body_header"), nil
		}
		for _, part := range line.parts {
			row := -1
			if math.Abs(part.Box.Y*line.height-234.17696) < 4 {
				row = 0
			} else if math.Abs(part.Box.Y*line.height-254.21696) < 4 {
				row = 1
			}
			if row < 0 {
				continue
			}
			field := row
			if part.Box.X >= .60 {
				field += 2
			}
			if fields[field].page == 0 {
				fields[field] = line
				fields[field].parts = nil
			}
			fields[field].parts = append(fields[field].parts, part)
		}
	}
	for _, field := range fields {
		if len(field.parts) == 0 {
			return unsupported("unmapped_body_header"), nil
		}
	}
	compact := strings.Join(strings.Fields(lineText(fields[0])), "")
	match := lectureHeader.FindStringSubmatch(compact)
	if match == nil {
		return unsupported("unmapped_body_header"), nil
	}
	issue, _ := strconv.Atoi(match[2])
	count, _ := strconv.Atoi(match[3])
	if issue != metadata.IssueNumber || count != pageCount-2 {
		return unsupported("canonical_source_mismatch"), nil
	}
	if _, err := time.Parse("Jan.2.2006", match[1]); err != nil {
		return unsupported("unmapped_body_header"), nil
	}
	mapping := bodyHeaderMapping{header: &BodyHeader{Contributors: []Contributor{}}, issues: []ReviewIssue{{Code: "inferred_body_header_geometry", ComponentID: componentID, SourcePage: 2, Blocking: true}}}
	date := fields[0]
	box := textBounds(date.parts)
	date.parts = []GeometryText{{Text: match[1], FontFamily: "DFXingShu", FontSize: 11, Box: Box{X: (68.064 + 9.12) / date.width, Y: box.Y, Width: 90 / date.width, Height: 11.04 / date.height}}}
	date.right = date.parts[0].Box.X + date.parts[0].Box.Width
	add := func(role string, line geometryLine) (Block, error) {
		// Separate stable header namespace; actual ownership remains the same body component.
		block, slots, issues, err := paragraphBlock(componentID+"-header-"+role, 1, []geometryLine{line})
		for index := range slots {
			slots[index].ComponentID = componentID
		}
		for index := range issues {
			issues[index].ComponentID = componentID
		}
		mapping.slots = append(mapping.slots, slots...)
		mapping.issues = append(mapping.issues, issues...)
		block.Style.Align = "left"
		return block, err
	}
	var err error
	if mapping.header.LectureDate, err = add("lecture-date", date); err != nil {
		return bodyHeaderMapping{}, err
	}
	style := ParagraphStyle{FontSize: 11, LineHeight: 11.04}
	mapping.fixed = append(mapping.fixed, FixedSlot{sourcePage: 2, ID: "fixed-body-lecture-date-marker", Element: "lectureDateMarker", Box: Box{X: 68.064 / date.width, Y: box.Y, Width: 11.04 / date.width, Height: 14 / date.height}, Style: style}, FixedSlot{sourcePage: 2, ID: "fixed-body-issue-summary", Element: "bodyIssueSummary", Box: Box{X: 203.69 / date.width, Y: box.Y, Width: 74 / date.width, Height: 14 / date.height}, Style: style})
	for _, field := range []struct {
		index                 int
		role, prefix, element string
	}{{1, "speaker", "‧講員：", "bodySpeakerLabel"}, {2, "transcriber", "謄修：", "transcriberLabel"}, {3, "editor", "完稿：", "editorLabel"}} {
		line := fields[field.index]
		parts, label, tracking, ok := headerNameRuns(line, field.prefix)
		if !ok {
			return unsupported("unmapped_body_header"), nil
		}
		line.parts = parts
		nameBox := textBounds(parts)
		line.right = nameBox.X + nameBox.Width
		name, err := add(field.role, line)
		if err != nil {
			return bodyHeaderMapping{}, err
		}
		name.Style.LetterSpacing = tracking
		mapping.header.Contributors = append(mapping.header.Contributors, Contributor{Role: field.role, Name: name})
		labelStyle := style
		labelStyle.LetterSpacing = tracking
		mapping.fixed = append(mapping.fixed, FixedSlot{sourcePage: 2, ID: "fixed-body-header-" + field.role, Element: field.element, Box: label, Style: labelStyle})
	}
	return mapping, nil
}

func headerNameRuns(line geometryLine, prefix string) ([]GeometryText, Box, float64, bool) {
	remaining := []rune(prefix)
	names, labelParts := []GeometryText{}, []GeometryText{}
	tracking := 0.0
	for _, part := range line.parts {
		if len(remaining) == 0 {
			names = append(names, part)
			continue
		}
		points := []rune(part.Text)
		cut := 0
		for cut < len(points) && len(remaining) > 0 {
			if !unicode.IsSpace(points[cut]) {
				if points[cut] != remaining[0] {
					return nil, Box{}, 0, false
				}
				remaining = remaining[1:]
			}
			cut++
		}
		labelPart := part
		if cut < len(points) {
			if !strings.Contains(strings.ToLower(part.FontFamily), "xing") || len(points) < 2 {
				return nil, Box{}, 0, false
			}
			for _, point := range points {
				if !unicode.Is(unicode.Han, point) && point != '：' && point != '‧' {
					return nil, Box{}, 0, false
				}
			}
			advance := (part.Box.Width*line.width - part.FontSize) / float64(len(points)-1)
			tracking = advance/part.FontSize - 1
			if !finite(tracking) || tracking < -.5 || tracking > 1 {
				return nil, Box{}, 0, false
			}
			labelPart.Box.Width = (float64(cut-1)*advance + part.FontSize) / line.width
			piece := part
			piece.Text = string(points[cut:])
			piece.Box.X += float64(cut) * advance / line.width
			piece.Box.Width = part.Box.X + part.Box.Width - piece.Box.X
			names = append(names, piece)
		}
		labelParts = append(labelParts, labelPart)
	}
	if len(remaining) > 0 || len(names) == 0 || len(labelParts) == 0 {
		return nil, Box{}, 0, false
	}
	return names, textBounds(labelParts), tracking, true
}
