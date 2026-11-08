package onlinebulletins

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

// Outlined labels are review proposals, only between proven frames and complete item sequences.
func outlinedBackLabels(page GeometryPage, lines []geometryLine) ([]FixedSlot, bool) {
	if page.backContentBottom <= 0 {
		return nil, false
	}
	frames := map[string]Box{}
	for _, frame := range page.Frames {
		if _, duplicate := frames[frame.Element]; duplicate || !validBox(frame.Box) || (frame.Box.Y+frame.Box.Height)*page.Height > page.backContentBottom {
			return nil, false
		}
		frames[frame.Element] = frame.Box
	}
	summary, hasSummary := frames["summaryFrame"]
	prayers, hasPrayers := frames["prayersFrame"]
	if !hasSummary || !hasPrayers || summary.Y+summary.Height >= prayers.Y {
		return nil, false
	}
	starts := []float64{summary.Y + summary.Height, prayers.Y}
	first := []float64{-1, -1}
	counts := []int{0, 0}
	for _, line := range lines {
		box := textBounds(line.parts)
		if box.Y+box.Height <= starts[0] {
			continue
		}
		if box.Y < starts[0] {
			return nil, false
		}
		field := 0
		if box.Y >= starts[1] {
			field = 1
		} else if box.Y+box.Height > starts[1] {
			return nil, false
		}
		number := coverItemNumber.FindString(lineText(line))
		if number != "" {
			value, err := strconv.Atoi(strings.TrimRight(strings.TrimSpace(number), ".、)"))
			counts[field]++
			if err != nil || value != counts[field] {
				return nil, false
			}
		}
		if counts[field] == 0 {
			return nil, false
		}
		if first[field] < 0 {
			first[field] = box.Y
		}
	}
	slots := []FixedSlot{}
	for i, element := range []string{"announcementsLabel", "prayersLabel"} {
		space := (first[i]-starts[i])*page.Height - 4
		if first[i] < 0 || space < 6 {
			return nil, false
		}
		size := math.Min(12, space)
		box := Box{X: prayers.X, Y: starts[i] + 2/page.Height, Width: prayers.Width, Height: size / page.Height}
		slots = append(slots, FixedSlot{ID: "fixed-back-" + element, Element: element, Box: box, Style: ParagraphStyle{FontSize: size, LineHeight: size}})
	}
	return slots, true
}

func backContentBottom(page GeometryPage) float64 {
	if page.backContentBottom >= backAnchorMinimum(page) && page.backContentBottom <= templateV1BackAnchorMax {
		return page.backContentBottom
	}
	return templateV1BackContentBottom
}

func backFrameSlots(page GeometryPage) ([]FixedSlot, []ReviewIssue) {
	slots, seen := []FixedSlot{}, map[string]bool{}
	for _, frame := range page.Frames {
		if frame.Element != "summaryFrame" && frame.Element != "announcementsFrame" && frame.Element != "prayersFrame" || seen[frame.Element] || !validBox(frame.Box) || (frame.Box.Y+frame.Box.Height)*page.Height > backContentBottom(page) {
			return nil, []ReviewIssue{{Code: "unmapped_back_decoration", SourcePage: page.Number, Blocking: true}}
		}
		seen[frame.Element] = true
		slots = append(slots, FixedSlot{sourcePage: page.Number, ID: "fixed-back-" + frame.Element, Element: frame.Element, Box: frame.Box, Style: ParagraphStyle{FontSize: 6, LineHeight: 6}})
	}
	if len(slots) != 3 {
		return slots, []ReviewIssue{{Code: "unmapped_back_decoration", SourcePage: page.Number, Blocking: true}}
	}
	return slots, nil
}

var backSidebarColumns = []struct {
	element, text string
}{{"summarySidebarTagline", "一看再看、百看不厭的"}, {"summarySidebarTitle", "家教會週報"}}

func backSidebarColumnPart(page GeometryPage, part GeometryText, x float64) bool {
	return part.Box.Y*page.Height < 175 && math.Abs(part.Box.X*page.Width-x) < 2
}

func backSidebarColumnParts(page GeometryPage, expected string) []GeometryText {
	var matched []GeometryText
	origins := map[float64]bool{}
	for _, first := range page.Texts {
		glyph := strings.TrimSpace(first.Text)
		if glyph == "㇐" {
			glyph = "一" // The CJK stroke is a source encoding variant, only at the fixed column's start.
		}
		if glyph == "" || !strings.HasPrefix(expected, glyph) || first.Box.X >= .165 {
			continue
		}
		if origins[first.Box.X] {
			continue
		}
		origins[first.Box.X] = true
		if len(origins) > 64 {
			return nil
		}
		parts := []GeometryText{}
		for _, part := range page.Texts {
			if strings.TrimSpace(part.Text) != "" && backSidebarColumnPart(page, part, first.Box.X*page.Width) {
				parts = append(parts, part)
			}
		}
		sort.Slice(parts, func(i, j int) bool { return parts[i].Box.Y < parts[j].Box.Y })
		text := lineText(geometryLine{parts: parts})
		text = strings.Join(strings.Fields(text), "")
		if strings.HasPrefix(text, "㇐") {
			text = "一" + strings.TrimPrefix(text, "㇐")
		}
		if text == expected {
			if matched != nil {
				return nil
			}
			matched = parts
		}
	}
	return matched
}

// The rotated sidebar is invariant template text, not a sermon sentence.
// Match both complete source columns before substituting legal native glyphs.
func backSidebarSlots(page GeometryPage) ([]FixedSlot, []ReviewIssue) {
	if slots := outlinedBackSidebarSlots(page); len(slots) != 0 {
		return slots, []ReviewIssue{{Code: "unmapped_fixed_template", ComponentID: "back-summary", SourcePage: page.Number, Blocking: true}}
	}
	slots, issues := []FixedSlot{}, []ReviewIssue{}
	for _, column := range backSidebarColumns {
		parts := backSidebarColumnParts(page, column.text)
		size := 0.0
		for _, part := range parts {
			size = math.Max(size, part.FontSize)
		}
		if page.Number < 4 || len(parts) == 0 || !finitePositive(size) {
			issues = append(issues, ReviewIssue{Code: "fixed_template_source_mismatch", ComponentID: "back-summary", Blocking: true})
			continue
		}
		box := textBounds(parts)
		// CSS tracks after the last glyph too; reserve one point for measured ink rounding.
		tracking := (box.Height*page.Height-1)/float64(len([]rune(column.text)))/size - 1
		if !finite(tracking) || tracking < -.5 || tracking > 1 {
			issues = append(issues, ReviewIssue{Code: "unmapped_vertical_template", ComponentID: "back-summary", Blocking: true})
			continue
		}
		slots = append(slots, FixedSlot{sourcePage: page.Number, ID: "fixed-back-" + column.element, Element: column.element, Box: box, Style: ParagraphStyle{FontSize: size, LineHeight: size, LetterSpacing: tracking}})
	}
	return slots, issues
}

func outlinedBackSidebarSlots(page GeometryPage) []FixedSlot {
	if page.Number < 4 || !supportedTemplatePaper(page) {
		return nil
	}
	lines, err := sourceLines(page, false)
	if err != nil {
		return nil
	}
	if _, ok := outlinedBackLabels(page, lines); !ok {
		return nil
	}
	var summary, prayers Box
	for _, frame := range page.Frames {
		if frame.Element == "summaryFrame" {
			summary = frame.Box
		} else if frame.Element == "prayersFrame" {
			prayers = frame.Box
		}
	}
	for _, part := range page.Texts {
		if strings.TrimSpace(part.Text) != "" && part.Box.X < summary.X && part.Box.Y < summary.Y+summary.Height {
			return nil // Native wording, even incomplete, must never be replaced by an outline proposal.
		}
	}
	slots := []FixedSlot{}
	for i, element := range []string{"summarySidebarTagline", "summarySidebarTitle"} {
		height := 110.0
		if i == 1 {
			height = 55
		}
		box := Box{X: prayers.X + (2+13*float64(i))/page.Width, Y: summary.Y + (30+27*float64(i))/page.Height, Width: 11 / page.Width, Height: height / page.Height}
		if !validBox(box) || box.X+box.Width >= summary.X || box.Y+box.Height > summary.Y+summary.Height {
			return nil
		}
		slots = append(slots, FixedSlot{sourcePage: page.Number, ID: "fixed-back-" + element, Element: element, Box: box, Style: ParagraphStyle{FontSize: 11, LineHeight: 11}})
	}
	return slots
}

// backComponents refuses raw back pages: exclusion must have completed before
// any component or classification payload can be constructed.
func backComponents(page GeometryPage) ([]Component, []Slot, []FixedSlot, []ReviewIssue, error) {
	if page.Number < 4 || !supportedTemplatePaper(page) {
		return nil, nil, nil, nil, ErrUnsupportedSource
	}
	for _, part := range page.Texts {
		if (part.Box.Y+part.Box.Height)*page.Height > backContentBottom(page) {
			return nil, nil, nil, nil, ErrUnsupportedSource
		}
	}
	for _, image := range page.Images {
		if (image.Box.Y+image.Box.Height)*page.Height > backContentBottom(page) {
			return nil, nil, nil, nil, ErrUnsupportedSource
		}
	}
	lines, err := sourceLines(page, false)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	anchors := []float64{-1, -1}
	fixed := []FixedSlot{}
	var inferredIssues []ReviewIssue
	for index, line := range lines {
		text := strings.Join(strings.Fields(lineText(line)), "")
		label := -1
		switch text {
		case "家教會公佈欄", "家教會公布欄":
			label = 0
		case "得勝與代求":
			label = 1
		}
		if label < 0 {
			continue
		}
		if anchors[label] >= 0 {
			return nil, nil, nil, nil, ErrUnsupportedSource
		}
		box := textBounds(line.parts)
		fontSize := 0.0
		for _, part := range line.parts {
			fontSize = math.Max(fontSize, part.FontSize)
		}
		element := []string{"announcementsLabel", "prayersLabel"}[label]
		fixed = append(fixed, FixedSlot{ID: "fixed-back-" + element, Element: element, Box: box, Style: ParagraphStyle{FontSize: fontSize, LineHeight: math.Max(fontSize, box.Height*page.Height)}})
		anchors[label] = box.Y
		lines[index].parts = nil
	}
	if anchors[0] < 0 && anchors[1] < 0 {
		var ok bool
		fixed, ok = outlinedBackLabels(page, lines)
		if !ok {
			return nil, nil, nil, nil, ErrUnsupportedSource
		}
		anchors[0], anchors[1] = fixed[0].Box.Y, fixed[1].Box.Y
		inferredIssues = append(inferredIssues, ReviewIssue{Code: "unmapped_fixed_template", SourcePage: page.Number, Blocking: true})
	}
	if anchors[0] < 0 || anchors[1] <= anchors[0] {
		return nil, nil, nil, nil, ErrUnsupportedSource
	}
	fields := make([][]geometryLine, 3)
	sidebars, _ := backSidebarSlots(page)
	sidebarParts := map[GeometryText]bool{}
	for _, sidebar := range sidebars {
		for _, column := range backSidebarColumns {
			if sidebar.Element == column.element {
				for _, part := range backSidebarColumnParts(page, column.text) {
					sidebarParts[part] = true
				}
			}
		}
	}
	unmatchedMargin := false
	for _, line := range lines {
		if len(line.parts) == 0 {
			continue
		}
		field := 0
		if line.parts[0].Box.Y >= anchors[1]-.004 {
			field = 2
		} else if line.parts[0].Box.Y >= anchors[0]-.004 {
			field = 1
		}
		if field == 0 {
			// Only proven template columns may be omitted from editable content.
			body := []GeometryText{}
			for _, part := range line.parts {
				fixedSidebar := sidebarParts[part]
				if part.Box.X < .165 {
					unmatchedMargin = unmatchedMargin || !fixedSidebar
				}
				if !fixedSidebar {
					body = append(body, part)
				}
			}
			line.parts = body
			if len(body) == 0 {
				continue
			}
		}
		fields[field] = append(fields[field], line)
	}
	if unmatchedMargin {
		inferredIssues = append(inferredIssues, ReviewIssue{Code: "fixed_template_source_mismatch", ComponentID: "back-summary", SourcePage: page.Number, Blocking: true})
	}
	components := []Component{{ID: "back-summary", Type: "backSummary", Items: []Item{}}, {ID: "announcements", Type: "announcements", Items: []Item{}}, {ID: "prayers", Type: "victoriesAndPrayers", Items: []Item{}}}
	slots, issues := []Slot{}, inferredIssues
	for field, source := range fields {
		groups := [][]geometryLine{}
		if field == 0 {
			if len(source) == 0 {
				return nil, nil, nil, nil, fmt.Errorf("%w: missing_back_summary", ErrUnsupportedSource)
			}
			groups = append(groups, source)
		} else {
			for _, line := range source {
				if coverItemNumber.MatchString(lineText(line)) {
					groups = append(groups, []geometryLine{})
				}
				if len(groups) == 0 {
					return nil, nil, nil, nil, fmt.Errorf("%w: missing_back_item_number_%d", ErrUnsupportedSource, field)
				}
				groups[len(groups)-1] = append(groups[len(groups)-1], line)
			}
		}
		columnRight := 0.0
		for _, line := range source {
			for _, part := range line.parts {
				columnRight = math.Max(columnRight, part.Box.X+part.Box.Width)
			}
		}
		for number, group := range groups {
			for index := range group {
				group[index].right = columnRight
			}
			block, placements, findings, err := paragraphBlock(components[field].ID, number+1, group)
			if err != nil {
				return nil, nil, nil, nil, fmt.Errorf("%w: back_item_%d_%d", err, field, number+1)
			}
			components[field].Items = append(components[field].Items, Item{ID: fmt.Sprintf("%s-item-%d", components[field].ID, number+1), Blocks: []Block{block}})
			slots = append(slots, placements...)
			issues = append(issues, findings...)
		}
	}
	return components, slots, fixed, issues, nil
}
