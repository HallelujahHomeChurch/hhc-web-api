package onlinebulletins

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

var scriptureParagraphStart = regexp.MustCompile(`^(創世記|出埃及記|利未記|民數記|申命記|約書亞記|士師記|路得記|撒母耳記[上下]|列王紀[上下]|歷代志[上下]|以斯拉記|尼希米記|以斯帖記|約伯記|詩篇|箴言|傳道書|雅歌|以賽亞書|耶利米書|耶利米哀歌|以西結書|但以理書|何西阿書|約珥書|阿摩司書|俄巴底亞書|約拿書|彌迦書|那鴻書|哈巴谷書|西番雅書|哈該書|撒迦利亞書|瑪拉基書|馬太福音|馬可福音|路加福音|約翰福音|使徒行傳|羅馬書|哥林多[前後]書|加拉太書|以弗所書|腓立比書|歌羅西書|帖撒羅尼迦[前後]書|提摩太[前後]書|提多書|腓利門書|希伯來書|雅各書|彼得[前後]書|約翰[一二三壹貳參]書|猶大書|啟示錄)[一二三四五六七八九十百零〇廿卅卌0-9]+[章篇][一二三四五六七八九十百零〇廿卅卌0-9]+([-－–至][一二三四五六七八九十百零〇廿卅卌0-9]+)?節[：:]`)

// Verified source exports use A4, Letter, or an A4 cover with Letter inners.
// Keep each page's coordinates and dimensions; paper size is not layout proof.
func supportedTemplatePaper(page GeometryPage) bool {
	return (math.Abs(page.Width-templateV1Width) <= .02 && math.Abs(page.Height-templateV1Height) <= .02) ||
		(math.Abs(page.Width-612) <= .02 && math.Abs(page.Height-792) <= .02)
}

const (
	templateV1Width  = 595.32
	templateV1Height = 841.92
	// Verified against both reference backs. Require the service-table anchor;
	// paper dimensions alone are not sufficient evidence of this exclusion mask.
	templateV1BackContentBottom = 464.0
	templateV1BackAnchorMax     = 500.0
)

// Letter backs are shorter than A4; use the same relative start of the table band.
func backAnchorMinimum(page GeometryPage) float64 {
	return 430 * page.Height / templateV1Height
}

// filterTemplateV1Geometry is the first boundary before candidate persistence or AI.
// Its return value cannot retain a slice backing array containing excluded text.
func filterTemplateV1Geometry(source GeometryDocument) (GeometryDocument, []ReviewIssue, error) {
	if len(source.Pages) < 4 || len(source.Pages) > 40 {
		return GeometryDocument{}, nil, ErrUnsupportedSource
	}
	allowed := GeometryDocument{SourceSHA256: source.SourceSHA256, PopplerVersion: source.PopplerVersion, Pages: make([]GeometryPage, len(source.Pages))}
	var issues []ReviewIssue
	for index, page := range source.Pages {
		if page.Number != index+1 || !supportedTemplatePaper(page) {
			return GeometryDocument{}, nil, ErrUnsupportedSource
		}
		allowed.Pages[index] = GeometryPage{Number: page.Number, Width: page.Width, Height: page.Height, Texts: []GeometryText{}, Images: []GeometryImage{}}
		if index != len(source.Pages)-1 {
			allowed.Pages[index].Texts = append(allowed.Pages[index].Texts, page.Texts...)
			allowed.Pages[index].Images = append(allowed.Pages[index].Images, page.Images...)
			allowed.Pages[index].Glyphs = append(allowed.Pages[index].Glyphs, page.Glyphs...)
			for _, frame := range page.Frames {
				if index > 0 && frame.Element == "bodyHeadingFrame" && validBox(frame.Box) {
					allowed.Pages[index].Frames = append(allowed.Pages[index].Frames, frame)
				}
			}
			continue
		}
		var anchor strings.Builder
		boundary := templateV1BackContentBottom
		foundAnchor := false
		for _, text := range page.Texts {
			top := text.Box.Y * page.Height
			label := strings.Join(strings.Fields(text.Text), "")
			if top >= backAnchorMinimum(page) && top <= templateV1BackAnchorMax && strings.Contains(label, "主日敬拜服事表") {
				if !foundAnchor {
					boundary = top
				} else {
					boundary = math.Min(boundary, top)
				}
				foundAnchor = true
			}
			if top >= 466 && top <= 474 {
				anchor.WriteString(label)
			}
		}
		if !foundAnchor && !strings.Contains(anchor.String(), "主日敬拜服事表") {
			issues = append(issues, ReviewIssue{Code: "unknown_back_layout", Blocking: true})
			continue
		}
		if foundAnchor {
			anchorTop := boundary
			for _, text := range page.Texts {
				top := text.Box.Y * page.Height
				if isWingdingsFont(text.FontFamily) && top <= anchorTop && anchorTop-top < 4 {
					boundary = math.Min(boundary, top)
				}
			}
		}
		allowed.Pages[index].backContentBottom = boundary
		for _, glyph := range page.Glyphs {
			if (glyph.Box.Y+glyph.Box.Height)*page.Height <= boundary {
				allowed.Pages[index].Glyphs = append(allowed.Pages[index].Glyphs, glyph)
			}
		}
		for _, text := range page.Texts {
			top, bottom := text.Box.Y*page.Height, (text.Box.Y+text.Box.Height)*page.Height
			if bottom <= boundary {
				allowed.Pages[index].Texts = append(allowed.Pages[index].Texts, text)
			} else if top < boundary {
				issues = append(issues, ReviewIssue{Code: "source_crosses_exclusion_boundary", Blocking: true})
			}
		}
		for _, image := range page.Images {
			top, bottom := image.Box.Y*page.Height, (image.Box.Y+image.Box.Height)*page.Height
			if bottom <= boundary {
				allowed.Pages[index].Images = append(allowed.Pages[index].Images, image)
			} else if top < boundary {
				// Never crop/copy a source raster that also contains excluded material.
				issues = append(issues, ReviewIssue{Code: "source_image_crosses_exclusion_boundary", Blocking: true})
			}
		}
		for _, frame := range page.Frames {
			if validBox(frame.Box) && (frame.Box.Y+frame.Box.Height)*page.Height <= boundary {
				allowed.Pages[index].Frames = append(allowed.Pages[index].Frames, frame)
			}
		}
	}
	return allowed, issues, nil
}

type geometryLine struct {
	page          int
	column        int
	width, height float64
	right         float64 // Source column allocation, zero means the body column.
	parts         []GeometryText
}

// sourceLines groups inline font runs by their vertical centers, not exact tops:
// a smaller scripture run on the same baseline has a different top coordinate.
func sourceLines(page GeometryPage, columns bool) ([]geometryLine, error) {
	if page.Number < 1 || page.Number > 40 || !finitePositive(page.Width) || !finitePositive(page.Height) || len(page.Texts) > 250000 {
		return nil, ErrInvalidGeometry
	}
	texts := append([]GeometryText(nil), page.Texts...)
	for _, text := range texts {
		// Group source WordArt too; paragraphBlock still enforces the 96pt
		// content limit after canonical header art has been separated.
		if !geometryBox(text.Box) || !utf8.ValidString(text.Text) || strings.TrimSpace(text.Text) == "" || !finite(text.FontSize) || text.FontSize < 6 || text.FontSize > 192 {
			return nil, ErrInvalidGeometry
		}
	}
	return baselineSourceLines(page, texts, columns), nil
}

// Ordering is shared with full-source recognition, which also retains small text.
func baselineSourceLines(page GeometryPage, texts []GeometryText, columns bool) []geometryLine {
	orderGeometryText(texts, columns)
	lines := []geometryLine{}
	var baseline GeometryText
	for _, text := range texts {
		sameLine := false
		if len(lines) > 0 {
			previous := baseline
			sameColumn := !columns || (previous.Box.X >= .5) == (text.Box.X >= .5)
			center := text.Box.Y + text.Box.Height/2
			previousCenter := previous.Box.Y + previous.Box.Height/2
			sameLine = sameColumn && math.Abs(center-previousCenter) <= math.Min(text.Box.Height, previous.Box.Height)/2
		}
		if sameLine {
			lines[len(lines)-1].parts = append(lines[len(lines)-1].parts, text)
			if text.FontSize > baseline.FontSize {
				baseline = text
			}
			continue
		}
		right := .95
		column := 0
		if columns && text.Box.X < .5 {
			right = .49
		} else if columns {
			column = 1
		}
		lines = append(lines, geometryLine{page: page.Number, column: column, width: page.Width, height: page.Height, right: right, parts: []GeometryText{text}})
		baseline = text
	}
	for index := range lines {
		sort.SliceStable(lines[index].parts, func(i, j int) bool { return lines[index].parts[i].Box.X < lines[index].parts[j].Box.X })
	}
	return lines
}

func templateID(prefix, key string) string {
	digest := sha256.Sum256([]byte(key))
	return prefix + hex.EncodeToString(digest[:12])
}

func sourceFontRole(family string) (string, bool) {
	family = strings.ToLower(family)
	name := family
	if _, suffix, subset := strings.Cut(family, "+"); subset {
		name = suffix
	}
	switch name {
	case "aptos", "calibri", "cambria", "helvetica", "microsoftjhengheiregular", "monotypecorsiva":
		return "body", true
	}
	switch {
	case strings.Contains(family, "kai"):
		return "scripture", true
	case strings.Contains(family, "xing"), strings.Contains(family, "dfgirl"), name == "全真顏體<":
		return "emphasis", true
	case strings.Contains(family, "mingliu"), strings.Contains(family, "mingli"), strings.Contains(family, "times"), strings.Contains(family, "arial"), strings.Contains(family, "notosans"), strings.Contains(family, "notoserif"):
		return "body", true
	default:
		return "body", false
	}
}

func isWingdingsFont(family string) bool {
	if len(family) > 7 && family[6] == '+' {
		for _, point := range family[:6] {
			if point < 'A' || point > 'Z' {
				return false
			}
		}
		family = family[7:]
	}
	family = strings.TrimSpace(family)
	return strings.EqualFold(family, "Wingdings") || strings.EqualFold(family, "Wingdings-Regular")
}

// Original Wingdings only; callers must verify the font identity first. Circle
// numbers exist in the legal body face, while the two stars need Symbols2.
func wingdingsRune(point rune) (rune, string, bool) {
	if point >= '\uf08c' && point <= '\uf095' {
		return '➊' + point - '\uf08c', "body", true
	}
	switch point {
	case '\uf0ab':
		return '★', "symbol", true
	case '\uf0ac':
		return '\U0001f7cb', "symbol", true
	case '\uf0ad':
		// Unicode WG2 N4363, source 1173: HEAVY EIGHT POINTED BLACK STAR.
		return '\U0001f7cf', "symbol", true
	default:
		return point, "body", unicode.IsSpace(point)
	}
}

// Full-em CJK runs expose source tracking without knowing proportional Latin
// advances. Capture it once; edited text never triggers an automatic fit.
func paragraphSourceTracking(lines []geometryLine, size float64) (float64, bool) {
	var samples []float64
	for _, line := range lines {
		for _, part := range line.parts {
			_, known := sourceFontRole(part.FontFamily)
			if !known || part.FontSize != size {
				continue
			}
			count := 0
			for _, point := range part.Text {
				if !unicode.Is(unicode.Han, point) && (point < 0x3000 || point > 0x303f) {
					count = 0
					break
				}
				count++
			}
			if count < 4 {
				continue
			}
			spacing := (part.Box.Width*line.width - float64(count)*size) / (float64(count-1) * size)
			if finite(spacing) && spacing >= -.5 && spacing <= 1 {
				samples = append(samples, spacing)
			}
		}
	}
	if len(samples) == 0 {
		return 0, false
	}
	sort.Float64s(samples)
	return samples[len(samples)/2], samples[len(samples)-1]-samples[0] > .04
}

// paragraphBlock joins semantic text, not rendered lines. A page continuation
// refers to the same Unicode sentence; only its fragment offsets change.
func paragraphBlock(componentID string, ordinal int, lines []geometryLine) (Block, []Slot, []ReviewIssue, error) {
	block := Block{ID: templateID("b-", fmt.Sprintf("%s:%d", componentID, ordinal)), Sentences: []Sentence{}}
	if len(lines) == 0 || len(lines) > 4096 {
		return Block{}, nil, nil, ErrGeometryLimits
	}
	var issues []ReviewIssue
	fontSize := 0.0
	scripture := scriptureParagraphStart.MatchString(strings.Join(strings.Fields(lineText(lines[0])), ""))
	lastPage := 0
	lastColumn := 0
	for _, line := range lines {
		if line.page < 1 || line.page > 40 || line.page < lastPage || line.column < 0 || line.column > 1 || (line.page == lastPage && line.column < lastColumn) || !finitePositive(line.width) || !finitePositive(line.height) || len(line.parts) == 0 || !finite(line.right) || line.right < 0 || line.right > 1 {
			return Block{}, nil, nil, fmt.Errorf("%w: paragraph_line", ErrInvalidGeometry)
		}
		lastPage = line.page
		lastColumn = line.column
		for _, part := range line.parts {
			if part.FontSize < 6 || part.FontSize > 96 || !finite(part.FontSize) || !geometryBox(part.Box) || !utf8.ValidString(part.Text) {
				return Block{}, nil, nil, fmt.Errorf("%w: paragraph_text", ErrInvalidGeometry)
			}
			fontSize = math.Max(fontSize, part.FontSize)
		}
	}
	lineHeight := fontSize
	for _, part := range lines[0].parts {
		lineHeight = math.Max(lineHeight, part.Box.Height*lines[0].height)
	}
	var pitches []float64
	for index, line := range lines[1:] {
		previous := lines[index]
		if line.page != previous.page || line.column != previous.column {
			continue
		}
		currentBox, previousBox := textBounds(line.parts), textBounds(previous.parts)
		pitch := (currentBox.Y + currentBox.Height/2 - previousBox.Y - previousBox.Height/2) * line.height
		if pitch > 0 {
			pitches = append(pitches, pitch)
		}
	}
	if len(pitches) > 0 {
		sort.Float64s(pitches)
		lineHeight = math.Max(fontSize, pitches[len(pitches)/2])
	}
	block.Style = ParagraphStyle{FontSize: fontSize, LineHeight: lineHeight, Align: "justify"}
	var varyingTracking bool
	block.Style.LetterSpacing, varyingTracking = paragraphSourceTracking(lines, fontSize)
	if varyingTracking {
		issues = append(issues, ReviewIssue{Code: "non_uniform_source_tracking", ComponentID: componentID, BlockID: block.ID, Blocking: true})
	}
	left := lines[0].parts[0].Box.X
	for index, line := range lines[1:] {
		if line.page != lines[0].page || line.column != lines[0].column {
			break
		}
		if index == 0 {
			left = line.parts[0].Box.X
		} else {
			left = math.Min(left, line.parts[0].Box.X)
		}
	}
	block.Style.FirstLineIndent = (lines[0].parts[0].Box.X - left) * lines[0].width / fontSize
	if block.Style.FirstLineIndent < 0 {
		block.Style.Indent = -block.Style.FirstLineIndent
	}
	if !validParagraphStyle(block.Style) {
		return Block{}, nil, nil, fmt.Errorf("%w: paragraph_style", ErrInvalidGeometry)
	}
	slots := []Slot{}
	type placement struct{ page, column int }
	pageIndex := map[placement]int{}
	sentence := Sentence{Spans: []Span{}}
	sentenceIndex, sentenceLength := 0, 0
	var spanText strings.Builder
	spanRole, spanSize := "", 0.0
	spanKnown := false
	var pendingEnd bool
	unknownFonts := map[string]bool{}
	sentenceIDs := []string{}
	occurrences := map[string]int{}
	flushSpan := func() {
		if spanText.Len() == 0 {
			return
		}
		span := Span{Text: spanText.String(), FontRole: spanRole}
		if spanSize != fontSize {
			size := spanSize
			span.FontSize = &size
		}
		sentence.Spans = append(sentence.Spans, span)
		spanText.Reset()
	}
	finishSentence := func() error {
		flushSpan()
		if sentenceLength == 0 {
			return nil
		}
		if len(sentence.Spans) > 32 || len(block.Sentences) >= 1024 {
			return ErrGeometryLimits
		}
		var content strings.Builder
		for _, span := range sentence.Spans {
			content.WriteString(span.Text)
		}
		key := componentID + ":" + block.ID + ":" + content.String()
		occurrences[key]++
		sentence.ID = templateID("s-", fmt.Sprintf("%s:%d", key, occurrences[key]))
		sentenceIDs = append(sentenceIDs, sentence.ID)
		block.Sentences = append(block.Sentences, sentence)
		sentence = Sentence{Spans: []Span{}}
		sentenceLength = 0
		sentenceIndex++
		pendingEnd = false
		return nil
	}
	for _, line := range lines {
		key := placement{line.page, line.column}
		index, exists := pageIndex[key]
		if !exists {
			index = len(slots)
			pageIndex[key] = index
			slot := Slot{sourcePage: line.page, ID: fmt.Sprintf("%s-p%d", block.ID, line.page), ComponentID: componentID, BlockID: block.ID, Fragments: []Fragment{}}
			if line.column > 0 {
				slot.ID += "-c2"
			}
			if index > 0 {
				slot.ContinuationOf = slots[index-1].ID
			}
			slots = append(slots, slot)
		}
		box := textBounds(line.parts)
		// The line's glyph bounds are not its paragraph allocation width.
		right := line.right
		if right == 0 {
			right = .95
		}
		if box.X+box.Width > right+.002 {
			return Block{}, nil, nil, fmt.Errorf("%w: paragraph_column", ErrInvalidGeometry)
		}
		box.Width = math.Max(box.Width, right-box.X)
		leading := (block.Style.LineHeight - fontSize) / 2 / line.height
		box.Y = math.Max(0, box.Y-leading)
		box.Height += leading
		if exists {
			old := slots[index].Box
			right, bottom := math.Max(old.X+old.Width, box.X+box.Width), math.Max(old.Y+old.Height, box.Y+box.Height)
			box.X, box.Y = math.Min(old.X, box.X), math.Min(old.Y, box.Y)
			box.Width, box.Height = right-box.X, bottom-box.Y
		}
		slots[index].Box = box
		for _, part := range line.parts {
			partRole, partKnown := sourceFontRole(part.FontFamily)
			for _, character := range part.Text {
				role, known := partRole, partKnown
				if isWingdingsFont(part.FontFamily) {
					character, role, known = wingdingsRune(character)
				} else if scripture {
					// Explicit quotation headers propose scripture styling, but never
					// erase uncertainty about unidentified source fonts.
					role = "scripture"
				}
				closingQuote := unicode.Is(unicode.Pe, character) || unicode.Is(unicode.Pf, character) || character == '"' || character == '\''
				terminal := strings.ContainsRune("。！？!?", character)
				if pendingEnd && !closingQuote && !terminal && !unicode.IsSpace(character) {
					if err := finishSentence(); err != nil {
						return Block{}, nil, nil, err
					}
				}
				if !known && !unknownFonts[part.FontFamily] {
					unknownFonts[part.FontFamily] = true
					issues = append(issues, ReviewIssue{Code: "unknown_font_role", ComponentID: componentID, BlockID: block.ID, SentenceID: fmt.Sprintf("sentence-%d", sentenceIndex), SourcePage: line.page, Blocking: true})
				}
				if sentence.Source == nil {
					sentence.Source = &SourceLocation{Page: line.page, Box: part.Box}
				}
				if spanRole != role || spanSize != part.FontSize || spanKnown != known {
					flushSpan()
					spanRole, spanSize = role, part.FontSize
					spanKnown = known
				}
				spanText.WriteRune(character)
				placeholder := fmt.Sprintf("sentence-%d", sentenceIndex)
				fragments := &slots[index].Fragments
				if len(*fragments) == 0 || (*fragments)[len(*fragments)-1].SentenceID != placeholder {
					*fragments = append(*fragments, Fragment{SentenceID: placeholder, Start: sentenceLength, End: sentenceLength})
				}
				sentenceLength++
				(*fragments)[len(*fragments)-1].End = sentenceLength
				if sentenceLength > 4096 {
					return Block{}, nil, nil, ErrGeometryLimits
				}
				if terminal {
					pendingEnd = true
				}
			}
		}
	}
	if err := finishSentence(); err != nil {
		return Block{}, nil, nil, err
	}
	if len(block.Sentences) == 0 {
		return Block{}, nil, nil, ErrInvalidGeometry
	}
	for index := range slots {
		for n := range slots[index].Fragments {
			var sentence int
			if _, err := fmt.Sscanf(slots[index].Fragments[n].SentenceID, "sentence-%d", &sentence); err != nil || sentence >= len(sentenceIDs) {
				return Block{}, nil, nil, ErrInvalidGeometry
			}
			slots[index].Fragments[n].SentenceID = sentenceIDs[sentence]
		}
	}
	for index := range issues {
		if issues[index].SentenceID == "" {
			continue
		}
		var sentence int
		if _, err := fmt.Sscanf(issues[index].SentenceID, "sentence-%d", &sentence); err != nil || sentence >= len(sentenceIDs) {
			return Block{}, nil, nil, ErrInvalidGeometry
		}
		issues[index].SentenceID = sentenceIDs[sentence]
	}
	return block, slots, issues, nil
}
