package onlinebulletins

import (
	"fmt"
	"math"
	"regexp"
	"strings"
)

var coverItemNumber = regexp.MustCompile(`^\s*[0-9]{1,2}[.、)]`)
var verseReferenceEnd = regexp.MustCompile(`[(（](((創|出|利|民|申|書|士|得|撒上|撒下|王上|王下|代上|代下|拉|尼|斯|伯|詩|箴|傳|歌|賽|耶|哀|結|但|何|珥|摩|俄|拿|彌|鴻|哈|番|該|亞|瑪|太|可|路|約|徒|羅|林前|林後|加|弗|腓|西|帖前|帖後|提前|提後|多|門|來|雅|彼前|彼後|約壹|約貳|約參|猶|啟)[一二三四五六七八九十百零〇廿卅卌0-9]+|俄|門|約貳|約參|猶)/[0-9]+([-－–][0-9]+)?|詩篇[一二三四五六七八九十百零〇廿卅卌0-9]+篇)[)）][。.]?$`)

// Missing outlined labels can only yield a review proposal: a narrow small-type
// verse column ending in a Bible citation, not an arbitrary final paragraph.
func proposedVerseBand(lines []geometryLine, footerTop float64) (Box, bool) {
	var parts []GeometryText
	var text strings.Builder
	lastBottom := 0.0
	for _, line := range lines {
		if len(line.parts) == 0 {
			continue
		}
		box := textBounds(line.parts)
		if box.Y < .65 || box.Y >= footerTop {
			continue
		}
		eligible := box.X >= .2 && box.X <= .35
		for _, part := range line.parts {
			eligible = eligible && part.FontSize <= 11.5
		}
		if !eligible {
			if len(parts) > 0 {
				return Box{}, false
			}
			continue
		}
		if lastBottom > 0 && box.Y-lastBottom > 2*box.Height {
			return Box{}, false
		}
		lastBottom = box.Y + box.Height
		parts = append(parts, line.parts...)
		text.WriteString(lineText(line))
	}
	if len(parts) == 0 || !verseReferenceEnd.MatchString(strings.Join(strings.Fields(text.String()), "")) {
		return Box{}, false
	}
	return textBounds(parts), true
}

func lineText(line geometryLine) string {
	var text strings.Builder
	for _, part := range line.parts {
		text.WriteString(part.Text)
	}
	return text.String()
}

func textBounds(parts []GeometryText) Box {
	box := parts[0].Box
	for _, part := range parts[1:] {
		right, bottom := math.Max(box.X+box.Width, part.Box.X+part.Box.Width), math.Max(box.Y+box.Height, part.Box.Y+part.Box.Height)
		box.X, box.Y = math.Min(box.X, part.Box.X), math.Min(box.Y, part.Box.Y)
		box.Width, box.Height = right-box.X, bottom-box.Y
	}
	return box
}

// coverComponent extracts only dynamic cover fields. Canonical masthead metadata
// and church-owned header/footer text are handled by the fixed template, not AI.
func coverComponent(page GeometryPage) (Component, []Slot, []FixedSlot, []ReviewIssue, error) {
	component := Component{ID: "cover", Type: "cover", Cover: &Cover{Welcome: []Block{}, Worship: []Item{}, Work: []Item{}, WordQuestions: []Item{}, WeeklyVerses: []Block{}}}
	lines, err := sourceLines(page, false)
	if err != nil || page.Number != 1 {
		return Component{}, nil, nil, nil, fmt.Errorf("%w: cover_lines", ErrUnsupportedSource)
	}
	// Dense covers can place the last verse line below the old 85% cutoff.
	// Use the actual contact heading, never discard content at a guessed height.
	footerTop := 1.0
	for _, line := range lines {
		box := textBounds(line.parts)
		text := strings.Join(strings.Fields(lineText(line)), "")
		if box.Y > .6 && box.X < .2 && (strings.HasPrefix(text, "教會地址") || strings.HasPrefix(text, "教会地址") || strings.HasPrefix(text, "Address:")) {
			footerTop = math.Min(footerTop, box.Y)
		}
	}
	if footerTop == 1 {
		return Component{}, nil, nil, nil, fmt.Errorf("%w: missing_cover_footer_anchor", ErrUnsupportedSource)
	}
	labels := []string{"Welcome", "Worship", "Work", "Word"}
	elements := []string{"welcomeLabel", "worshipLabel", "workLabel", "wordLabel", "verseLabel"}
	anchorY := []float64{-1, -1, -1, -1, -1}
	fields := make([][]geometryLine, 5)
	fixed := []FixedSlot{}
	var inferredIssues []ReviewIssue
	for index := range lines {
		line := &lines[index]
		label := -1
		cut := 0
		for partIndex, part := range line.parts {
			text := strings.TrimSpace(strings.TrimRight(strings.TrimSpace(part.Text), ":："))
			if part.Box.X > .2 {
				continue
			}
			for number, name := range labels {
				if text == name {
					label, cut = number, partIndex+1
				}
			}
			if strings.Join(strings.Fields(part.Text), "") == "本週金句" && part.Box.Y > .6 {
				label, cut = 4, partIndex+1
			}
			if label >= 0 {
				break
			}
		}
		if label < 0 {
			continue
		}
		if anchorY[label] >= 0 {
			return Component{}, nil, nil, nil, fmt.Errorf("%w: duplicate_cover_anchor_%d", ErrUnsupportedSource, label)
		}
		for cut < len(line.parts) {
			punctuation := strings.TrimSpace(line.parts[cut].Text)
			if punctuation != ":" && punctuation != "：" {
				break
			}
			cut++
		}
		labelParts := line.parts[:cut]
		// Poppler may join the separator and first prose run. Keep the separator
		// in the fixed label, leaving only prose in the editable component.
		if cut < len(line.parts) {
			part := &line.parts[cut]
			if strings.HasPrefix(part.Text, ":") || strings.HasPrefix(part.Text, "：") {
				separator := *part
				separator.Text = string([]rune(part.Text)[0])
				separator.Box.Width = math.Min(part.Box.Width, part.FontSize/page.Width)
				labelParts = append(append([]GeometryText{}, labelParts...), separator)
				part.Text = strings.TrimPrefix(part.Text, separator.Text)
				part.Box.X += separator.Box.Width
				part.Box.Width -= separator.Box.Width
			}
		}
		box := textBounds(labelParts)
		fontSize := 0.0
		for _, part := range labelParts {
			fontSize = math.Max(fontSize, part.FontSize)
		}
		style := ParagraphStyle{FontSize: fontSize, LineHeight: math.Max(fontSize, box.Height*page.Height)}
		fixed = append(fixed, FixedSlot{ID: "fixed-cover-" + elements[label], Element: elements[label], Box: box, Style: style})
		anchorY[label] = box.Y
		line.parts = line.parts[cut:]
	}
	if anchorY[4] < 0 && anchorY[3] >= 0 {
		proposalPage := page
		proposalPage.Texts = nil
		var residues []GeometryText
		for _, line := range lines {
			for _, part := range line.parts {
				// Outlined left labels can leave an extractable full stop beside the verse column.
				if part.Box.X < .2 && part.Box.Y > .65 && part.Box.Y < footerTop && part.FontSize <= 11.5 && strings.TrimSpace(part.Text) == "。" {
					residues = append(residues, part)
					continue
				}
				proposalPage.Texts = append(proposalPage.Texts, part)
			}
		}
		proposal, proposalError := sourceLines(proposalPage, false)
		if verse, ok := proposedVerseBand(proposal, footerTop); proposalError == nil && ok && verse.Y > anchorY[3] {
			for _, part := range residues {
				if part.Box.Y < verse.Y-16/page.Height || part.Box.Y > verse.Y+verse.Height {
					proposalPage.Texts = append(proposalPage.Texts, part)
				}
			}
			proposal, proposalError = sourceLines(proposalPage, false)
			if proposalError != nil {
				return Component{}, nil, nil, nil, proposalError
			}
			lines = proposal
			anchorY[4] = verse.Y + 16/page.Height
			fixed = append(fixed, FixedSlot{ID: "fixed-cover-verseLabel", Element: "verseLabel", Box: Box{X: .08, Y: anchorY[4], Width: verse.X - .08 - 8/page.Width, Height: 14 / page.Height}, Style: ParagraphStyle{FontSize: 11, LineHeight: 14}})
			inferredIssues = append(inferredIssues, ReviewIssue{Code: "unmapped_fixed_template", ComponentID: "cover", Blocking: true})
		}
	}
	for index, y := range anchorY {
		if y < 0 || (index > 0 && y <= anchorY[index-1]) {
			return Component{}, nil, nil, nil, fmt.Errorf("%w: missing_cover_anchor_%d", ErrUnsupportedSource, index)
		}
	}
	// The verse label is vertically centered beside the source verse lines.
	verseTop := anchorY[4] - 16/page.Height
	for _, line := range lines {
		if len(line.parts) == 0 {
			continue
		}
		y := line.parts[0].Box.Y
		if y < anchorY[0]-.004 || y >= footerTop {
			continue
		}
		field := 0
		for index := 1; index < 4; index++ {
			if y >= anchorY[index]-.004 {
				field = index
			}
		}
		if y >= verseTop {
			field = 4
		}
		fields[field] = append(fields[field], line)
	}
	// Freeze the observed cover column edge at extraction. Some reference runs
	// extend past Word's nominal margin; never widen it later to fit edited text.
	columnRight := 0.0
	for _, field := range []int{0, 2, 3, 4} {
		for _, line := range fields[field] {
			for _, part := range line.parts {
				columnRight = math.Max(columnRight, part.Box.X+part.Box.Width)
			}
		}
	}
	for field := range fields {
		for index := range fields[field] {
			fields[field][index].right = columnRight
		}
	}
	slots, issues := []Slot{}, inferredIssues
	ordinal := 0
	addBlock := func(source []geometryLine, scripture bool) (Block, error) {
		ordinal++
		block, placements, findings, err := paragraphBlock(component.ID, ordinal, source)
		if err != nil {
			return Block{}, err
		}
		// The weekly-verse field defines its role, even when the PDF uses an
		// emphasis font or omits font names. Keep text, IDs and geometry intact.
		unknownSymbol := false
		if scripture {
			for _, line := range source {
				for _, part := range line.parts {
					if isWingdingsFont(part.FontFamily) {
						for _, character := range part.Text {
							_, _, known := wingdingsRune(character)
							unknownSymbol = unknownSymbol || !known
						}
					}
				}
			}
			for i := range block.Sentences {
				for j := range block.Sentences[i].Spans {
					if block.Sentences[i].Spans[j].FontRole != "symbol" {
						block.Sentences[i].Spans[j].FontRole = "scripture"
					}
				}
			}
		}
		slots = append(slots, placements...)
		for _, finding := range findings {
			if !scripture || unknownSymbol || finding.Code != "unknown_font_role" {
				issues = append(issues, finding)
			}
		}
		return block, nil
	}
	welcome, err := addBlock(fields[0], false)
	if err != nil {
		return Component{}, nil, nil, nil, fmt.Errorf("%w: cover_welcome: %w", ErrUnsupportedSource, err)
	}
	component.Cover.Welcome = append(component.Cover.Welcome, welcome)
	for field := 1; field < 4; field++ {
		groups := [][]geometryLine{}
		if field == 1 {
			for _, line := range fields[field] {
				for _, part := range line.parts {
					text := strings.TrimSpace(part.Text)
					points := []rune(text)
					marker := len(points) == 1 && strings.ContainsRune("①②③④⑤➊➋➌➍➎\uf08c\uf08d\uf08e\uf08f\uf090", points[0])
					if marker {
						if points[0] >= '\uf08c' && points[0] <= '\uf090' {
							if !isWingdingsFont(part.FontFamily) {
								return Component{}, nil, nil, nil, fmt.Errorf("%w: unknown_song_marker_font", ErrUnsupportedSource)
							}
						}
						groups = append(groups, []geometryLine{{page: line.page, width: line.width, height: line.height, parts: []GeometryText{}}})
					}
					if len(groups) == 0 {
						return Component{}, nil, nil, nil, fmt.Errorf("%w: missing_song_marker", ErrUnsupportedSource)
					}
					group := &groups[len(groups)-1]
					if len((*group)[len(*group)-1].parts) > 0 && (*group)[len(*group)-1].parts[0].Box.Y+.008 < part.Box.Y {
						*group = append(*group, geometryLine{page: line.page, width: line.width, height: line.height, parts: []GeometryText{}})
					}
					last := &(*group)[len(*group)-1]
					last.parts = append(last.parts, part)
					last.right = math.Max(last.right, part.Box.X+part.Box.Width)
				}
			}
		} else {
			for _, line := range fields[field] {
				if coverItemNumber.MatchString(lineText(line)) {
					groups = append(groups, []geometryLine{})
				}
				if len(groups) == 0 {
					return Component{}, nil, nil, nil, fmt.Errorf("%w: missing_item_number_%d", ErrUnsupportedSource, field)
				}
				groups[len(groups)-1] = append(groups[len(groups)-1], line)
			}
		}
		for number, group := range groups {
			block, err := addBlock(group, false)
			if err != nil {
				return Component{}, nil, nil, nil, fmt.Errorf("%w: cover_item_%d_%d", err, field, number+1)
			}
			item := Item{ID: fmt.Sprintf("cover-item-%d-%d", field, number+1), Blocks: []Block{block}}
			switch field {
			case 1:
				component.Cover.Worship = append(component.Cover.Worship, item)
			case 2:
				component.Cover.Work = append(component.Cover.Work, item)
			case 3:
				component.Cover.WordQuestions = append(component.Cover.WordQuestions, item)
			}
		}
	}
	verse, err := addBlock(fields[4], true)
	if err != nil {
		return Component{}, nil, nil, nil, fmt.Errorf("%w: cover_verse: %w", ErrUnsupportedSource, err)
	}
	component.Cover.WeeklyVerses = append(component.Cover.WeeklyVerses, verse)
	if len(component.Cover.Worship) < 3 || len(component.Cover.Worship) > 5 || len(component.Cover.Work) != 3 || len(component.Cover.WordQuestions) < 6 {
		issues = append(issues, ReviewIssue{Code: "unexpected_cover_item_count", ComponentID: component.ID, Blocking: true})
	}
	return component, slots, fixed, issues, nil
}
