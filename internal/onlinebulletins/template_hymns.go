package onlinebulletins

import (
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

var hymnHeading = regexp.MustCompile(`^([1-9][0-9]?)\s*[.．、]\s*[^()（）]+[（(][^()（）]+[)）]\s*$`)

// A lyrics page may start with the previous article's final paragraph. Split
// at the measured label, not at a PDF page boundary; never copy source slices.
func splitHymnPage(page GeometryPage) (*GeometryPage, GeometryPage, error) {
	lines, err := sourceLines(page, true)
	if err != nil {
		return nil, GeometryPage{}, err
	}
	boundary := -1.0
	for _, line := range lines {
		if strings.TrimSpace(lineText(line)) == "小組詩歌" {
			if boundary >= 0 {
				return nil, GeometryPage{}, ErrUnsupportedSource
			}
			boundary = textBounds(line.parts).Y
		}
	}
	if boundary < 0 {
		return nil, GeometryPage{}, ErrUnsupportedSource
	}
	body, hymns := page, page
	body.Texts, hymns.Texts = []GeometryText{}, []GeometryText{}
	hasBody := false
	for _, part := range page.Texts {
		if part.Box.Y < .07 {
			body.Texts = append(body.Texts, part)
			hymns.Texts = append(hymns.Texts, part)
		} else if part.Box.Y < boundary-.002 {
			if part.Box.Y+part.Box.Height > boundary+.002 {
				return nil, GeometryPage{}, ErrUnsupportedSource
			}
			body.Texts = append(body.Texts, part)
			hasBody = true
		} else {
			hymns.Texts = append(hymns.Texts, part)
		}
	}
	if !hasBody {
		return nil, hymns, nil
	}
	return &body, hymns, nil
}

// Hymn numbers define semantic order, independently of their two-column slots.
// Unmarked stanzas remain verses; chorus/bridge are not inferred from wording.
func hymnComponent(page GeometryPage) (Component, []Slot, []FixedSlot, []ReviewIssue, error) {
	component := Component{ID: "hymns", Type: "hymnLyrics", HymnLyrics: &HymnLyrics{Hymns: []Hymn{}}}
	if page.Number < 3 || !supportedTemplatePaper(page) {
		return Component{}, nil, nil, nil, ErrUnsupportedSource
	}
	lines, err := sourceLines(page, true)
	if err != nil {
		return Component{}, nil, nil, nil, err
	}
	type song struct {
		number int
		lines  []geometryLine
	}
	songs := []song{}
	fixed := []FixedSlot{}
	current := -1
	numbers := map[int]bool{}
	rightColumnStart := .95
	pageNumberFound, labelFound := false, false
	for _, line := range lines {
		text := strings.TrimSpace(lineText(line))
		box := textBounds(line.parts)
		if box.Y < .07 {
			printed, err := strconv.Atoi(text)
			if err != nil || printed != page.Number-1 || pageNumberFound {
				return Component{}, nil, nil, nil, ErrUnsupportedSource
			}
			pageNumberFound = true
			size := 0.0
			for _, part := range line.parts {
				size = math.Max(size, part.FontSize)
			}
			fixed = append(fixed, FixedSlot{sourcePage: page.Number, ID: "fixed-hymn-page-number", Element: "pageNumber", Box: box, Style: ParagraphStyle{FontSize: size, LineHeight: math.Max(size, box.Height*page.Height)}})
			continue
		}
		if text == "小組詩歌" {
			if labelFound {
				return Component{}, nil, nil, nil, ErrUnsupportedSource
			}
			labelFound = true
			fixed = append(fixed, FixedSlot{sourcePage: page.Number, ID: "fixed-hymn-label", Element: "hymnLabel", Box: box, Style: ParagraphStyle{FontSize: line.parts[0].FontSize, LineHeight: math.Max(line.parts[0].FontSize, box.Height*page.Height)}})
			continue
		}
		if match := hymnHeading.FindStringSubmatch(text); match != nil {
			if line.column == 1 {
				rightColumnStart = math.Min(rightColumnStart, box.X)
			}
			number, _ := strconv.Atoi(match[1])
			if numbers[number] || len(songs) >= 20 {
				return Component{}, nil, nil, nil, ErrUnsupportedSource
			}
			numbers[number] = true
			songs = append(songs, song{number: number, lines: []geometryLine{line}})
			current = len(songs) - 1
			continue
		}
		if current < 0 || songs[current].lines[0].column != line.column {
			return Component{}, nil, nil, nil, ErrUnsupportedSource
		}
		songs[current].lines = append(songs[current].lines, line)
	}
	if !labelFound || len(songs) == 0 {
		return Component{}, nil, nil, nil, ErrUnsupportedSource
	}
	sort.Slice(songs, func(i, j int) bool { return songs[i].number < songs[j].number })
	slots, issues := []Slot{}, []ReviewIssue{}
	if !pageNumberFound {
		issues = append(issues, ReviewIssue{Code: "missing_source_page_number", SourcePage: page.Number, Blocking: true})
	}
	ordinal := 0
	for _, song := range songs {
		if len(song.lines) < 2 {
			return Component{}, nil, nil, nil, ErrUnsupportedSource
		}
		id := "hymns-song-" + strconv.Itoa(song.number)
		hymn := Hymn{ID: id, Sections: []HymnSection{}}
		for index, line := range song.lines {
			if line.column == 0 && rightColumnStart < .95 {
				line.right = math.Max(.49, rightColumnStart-.01)
			}
			ordinal++
			block, placements, findings, err := paragraphBlock(component.ID, ordinal, []geometryLine{line})
			if err != nil {
				return Component{}, nil, nil, nil, err
			}
			block.Style.Align = "left"
			if index == 0 {
				hymn.Title = block // Preserve source number and source label without estimating their glyph widths.
			} else {
				previous := song.lines[index-1]
				gap := (textBounds(line.parts).Y - textBounds(previous.parts).Y) * page.Height
				if len(hymn.Sections) == 0 || gap > line.parts[0].FontSize*1.8 {
					hymn.Sections = append(hymn.Sections, HymnSection{ID: id + "-stanza-" + strconv.Itoa(len(hymn.Sections)+1), Kind: "verse", Lines: []Block{}})
				}
				section := &hymn.Sections[len(hymn.Sections)-1]
				section.Lines = append(section.Lines, block)
			}
			slots = append(slots, placements...)
			issues = append(issues, findings...)
		}
		component.HymnLyrics.Hymns = append(component.HymnLyrics.Hymns, hymn)
	}
	return component, slots, fixed, issues, nil
}
