package onlinebulletins

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"
)

// AssembleRecognition consumes validated source-linked semantics, never the
// legacy cover/body/hymn parsers or provider-owned assets/layout proof.
func AssembleRecognition(source GeometryDocument, pages []RecognitionPage, results []RecognitionResult, canonical CanonicalIssue, snapshot TemplateSettings, assets []Asset) (DocumentCandidate, []ReviewIssue, error) {
	if uuid.Validate(canonical.IssueID) != nil || canonical.Series != "general" || canonical.ContentLocale != "zh-Hant" || !validManifestAssets(assets) || snapshot.Version < 1 {
		return DocumentCandidate{}, nil, fmt.Errorf("%w: identity", ErrRecognition)
	}
	if normalized, ok := NormalizeTemplateText(snapshot.TemplateText); !ok || normalized != snapshot.TemplateText {
		return DocumentCandidate{}, nil, fmt.Errorf("%w: snapshot", ErrRecognition)
	}
	allowed, privacy, err := filterTemplateV1Geometry(source)
	if err != nil || len(privacy) != 0 || len(pages) != len(allowed.Pages) || len(results) != len(pages) {
		return DocumentCandidate{}, nil, fmt.Errorf("%w: privacy", ErrRecognition)
	}
	document := Document{IssueID: canonical.IssueID, Series: canonical.Series, ContentLocale: canonical.ContentLocale, SchemaVersion: "1", TemplateVersion: "v1", TemplateSnapshot: &snapshot, SourceAssetChecksum: source.SourceSHA256, SourcePageCount: len(pages), LayoutManifest: LayoutManifest{TemplateVersion: "v1", RendererVersion: "v11", RendererArtifactSha256: RendererV11SHA256, Assets: append([]Asset(nil), assets...)}}
	for _, page := range allowed.Pages {
		id := fmt.Sprintf("page-%d", page.Number)
		document.Pages = append(document.Pages, Page{ID: id, Width: page.Width, Height: page.Height})
		document.LayoutManifest.Pages = append(document.LayoutManifest.Pages, LayoutPage{PageID: id, Slots: []Slot{}})
	}
	cover := Component{ID: "cover", Type: "cover", Cover: &Cover{Welcome: []Block{}, Worship: []Item{}, Work: []Item{}, WordQuestions: []Item{}, WeeklyVerses: []Block{}}}
	hymns := Component{ID: "hymns", Type: "hymnLyrics", HymnLyrics: &HymnLyrics{Hymns: []Hymn{}}}
	back := []Component{{ID: "back-summary", Type: "backSummary", Items: []Item{}}, {ID: "announcements", Type: "announcements", Items: []Item{}}, {ID: "prayers", Type: "victoriesAndPrayers", Items: []Item{}}}
	sourceParts, err := CatalogParts(allowed)
	if err != nil {
		return DocumentCandidate{}, nil, err
	}
	var bodies []Component
	bodyGroups := map[string]int{}
	var issues []ReviewIssue
	groups := map[string]string{}
	hymnGroups := map[string]int{}
	ordinal := 0
	for index, input := range pages {
		page := allowed.Pages[index]
		if !validRecognitionPage(input) || input.Page != page.Number || input.TotalPages != len(pages) || input.SourceSHA256 != source.SourceSHA256 || input.Width != page.Width || input.Height != page.Height {
			return DocumentCandidate{}, nil, fmt.Errorf("%w: page_%d_page_identity", ErrRecognition, page.Number)
		}
		ordered := baselineSourceLines(page, append([]GeometryText(nil), page.Texts...), index == len(pages)-2)
		native := map[string]GeometryText{}
		n := 0
		for _, line := range ordered {
			for _, part := range line.parts {
				if n >= len(input.Native) || input.Native[n].ID != fmt.Sprintf("p%d-t%d", page.Number, n+1) || input.Native[n].Text != part.Text || input.Native[n].Box != part.Box || !slices.Equal(input.Native[n].Characters, recognitionCharacterBoxes(part, page.Glyphs)) {
					return DocumentCandidate{}, nil, fmt.Errorf("%w: page_%d_native_binding", ErrRecognition, page.Number)
				}
				native[input.Native[n].ID] = part
				n++
			}
		}
		if n != len(input.Native) {
			return DocumentCandidate{}, nil, fmt.Errorf("%w: page_%d_native_count", ErrRecognition, page.Number)
		}
		if !slices.Equal(input.Frames, recognitionFrames(page)) {
			return DocumentCandidate{}, nil, ErrRecognition
		}
		expectedImages := []RecognitionImage{}
		for _, part := range sourceParts {
			if part.Kind == "image" && part.Page == input.Page {
				expectedImages = append(expectedImages, RecognitionImage{ID: part.ID, Box: part.Box})
			}
		}
		if len(expectedImages) != len(input.Images) {
			return DocumentCandidate{}, nil, ErrRecognition
		}
		for i := range expectedImages {
			if expectedImages[i] != input.Images[i] {
				return DocumentCandidate{}, nil, ErrRecognition
			}
		}
		result := results[index]
		if _, ok := recognitionSemanticSHA256(input, result); !ok {
			return DocumentCandidate{}, nil, ErrRecognition
		}
		if !validMaterializedRecognition(input, result) {
			return DocumentCandidate{}, nil, fmt.Errorf("%w: page_%d_source_validation", ErrRecognition, page.Number)
		}
		for _, recognized := range result.Blocks {
			if recognized.Role == "fixed_label" || recognized.Role == "illustration" {
				continue
			}
			if recognized.Role == "body_date" || recognized.Role == "body_transcriber" || recognized.Role == "body_editor" {
				continue
			}
			componentID := ""
			hymnIndex := 0
			bodyIndex := 0
			role := recognized.Role
			switch {
			case strings.HasPrefix(role, "cover_"):
				if page.Number != 1 {
					return DocumentCandidate{}, nil, fmt.Errorf("%w: page_%d_cover_page", ErrRecognition, page.Number)
				}
				componentID = cover.ID
			case strings.HasPrefix(role, "body_"):
				if page.Number == 1 || page.Number == len(pages) {
					return DocumentCandidate{}, nil, fmt.Errorf("%w: page_%d_body_page", ErrRecognition, page.Number)
				}
				if role == "body_title" {
					if _, exists := bodyGroups[recognized.Group]; exists {
						return DocumentCandidate{}, nil, ErrRecognition
					}
					id := templateID("body-", fmt.Sprintf("%d:%s", page.Number, recognized.Group))
					bodyGroups[recognized.Group] = len(bodies)
					bodies = append(bodies, Component{ID: id, Type: "bodySection", BodySection: &BodySection{Kind: recognized.SectionKind, Blocks: []Block{}}})
				}
				var exists bool
				bodyIndex, exists = bodyGroups[recognized.Group]
				if !exists {
					return DocumentCandidate{}, nil, fmt.Errorf("%w: page_%d_body_order", ErrRecognition, page.Number)
				}
				componentID = bodies[bodyIndex].ID
			case strings.HasPrefix(role, "hymn_"):
				componentID = hymns.ID
				key := fmt.Sprintf("%d:%s", page.Number, recognized.Group)
				var exists bool
				hymnIndex, exists = hymnGroups[key]
				if !exists {
					hymnIndex = len(hymns.HymnLyrics.Hymns)
					hymnGroups[key] = hymnIndex
					hymns.HymnLyrics.Hymns = append(hymns.HymnLyrics.Hymns, Hymn{ID: templateID("hymn-", key), Sections: []HymnSection{}})
				}
				if role == "hymn_title" && hymns.HymnLyrics.Hymns[hymnIndex].Title.ID != "" {
					return DocumentCandidate{}, nil, ErrRecognition
				}
			case role == "back_summary":
				componentID = back[0].ID
			case role == "announcement":
				componentID = back[1].ID
			case role == "prayer":
				componentID = back[2].ID
			default:
				return DocumentCandidate{}, nil, fmt.Errorf("%w: page_%d_role", ErrRecognition, page.Number)
			}
			if (role == "back_summary" || role == "announcement" || role == "prayer") && page.Number != len(pages) {
				return DocumentCandidate{}, nil, fmt.Errorf("%w: page_%d_back_page", ErrRecognition, page.Number)
			}
			var parts []GeometryText
			if recognized.Mode == "native" {
				selected, ok := recognitionBlockParts(input, recognized)
				if !ok {
					return DocumentCandidate{}, nil, ErrRecognition
				}
				for _, part := range selected {
					original := native[part.ID]
					original.Text, original.Box = part.Text, part.Box
					parts = append(parts, original)
				}
			} else {
				parts = []GeometryText{{Text: recognized.Text, FontFamily: "MingLiU", FontSize: 14, Box: recognized.Box}}
				issues = append(issues, ReviewIssue{Code: "recognition_visual_verification_required", SourcePage: page.Number, ComponentID: componentID, Blocking: true})
			}
			if role == "body_separator" {
				for _, part := range parts {
					document.LayoutManifest.Pages[index].FixedSlots = append(document.LayoutManifest.Pages[index].FixedSlots, FixedSlot{ID: "fixed-" + componentID + "-speakerSeparator", Element: "speakerSeparator", sourcePage: page.Number, Box: part.Box, Style: ParagraphStyle{FontSize: part.FontSize, LineHeight: math.Max(part.FontSize, part.Box.Height*page.Height)}})
				}
				continue
			}
			parts = recognitionContentParts(parts, role)
			if len(parts) == 0 {
				continue
			}
			// Source typography guides emphasis; the fixed reader owns legal fonts and fit.
			for i := range parts {
				if parts[i].FontSize < 9 {
					parts[i].FontSize = 9
				}
				if parts[i].FontSize > 48 {
					parts[i].FontSize = 24
				}
				if _, known := sourceFontRole(parts[i].FontFamily); !known && !isWingdingsFont(parts[i].FontFamily) {
					parts[i].FontFamily = "MingLiU"
				}
			}
			selected := page
			selected.Texts = parts
			lines, err := sourceLines(selected, strings.HasPrefix(role, "hymn_"))
			if err != nil {
				return DocumentCandidate{}, nil, err
			}
			for i := range lines {
				bounds := textBounds(lines[i].parts)
				lines[i].right = max(lines[i].right, bounds.X+bounds.Width)
			}
			ordinal++
			block, slots, blockIssues, err := paragraphBlock(componentID, ordinal, lines)
			if err != nil {
				return DocumentCandidate{}, nil, err
			}
			block.Style.LetterSpacing = 0
			for _, issue := range blockIssues {
				if issue.Code != "non_uniform_source_tracking" {
					issues = append(issues, issue)
				}
			}
			for _, slot := range slots {
				document.LayoutManifest.Pages[slot.sourcePage-1].Slots = append(document.LayoutManifest.Pages[slot.sourcePage-1].Slots, slot)
			}
			appendItem := func(items *[]Item) {
				key := componentID + ":" + role
				if len(*items) == 0 || groups[key] != recognized.Group {
					*items = append(*items, Item{ID: templateID("item-", fmt.Sprintf("%s:%d", key, ordinal)), Blocks: []Block{}})
					groups[key] = recognized.Group
				}
				last := &(*items)[len(*items)-1]
				last.Blocks = append(last.Blocks, block)
			}
			switch role {
			case "cover_welcome":
				cover.Cover.Welcome = append(cover.Cover.Welcome, block)
			case "cover_worship":
				appendItem(&cover.Cover.Worship)
			case "cover_work":
				appendItem(&cover.Cover.Work)
			case "cover_word":
				appendItem(&cover.Cover.WordQuestions)
			case "cover_verse":
				cover.Cover.WeeklyVerses = append(cover.Cover.WeeklyVerses, block)
			case "body_title":
				bodies[bodyIndex].BodySection.Title = block
			case "body_subtitle":
				body := bodies[bodyIndex].BodySection
				if body.Subtitle == nil && len(body.Blocks) == 0 {
					body.Subtitle = &block
				} else {
					body.Blocks = append(body.Blocks, block)
				}
			case "body_speaker":
				bodies[bodyIndex].BodySection.Contributors = append(bodies[bodyIndex].BodySection.Contributors, Contributor{Role: "speaker", Name: block})
			case "body_paragraph":
				bodies[bodyIndex].BodySection.Blocks = append(bodies[bodyIndex].BodySection.Blocks, block)
			case "hymn_title":
				hymns.HymnLyrics.Hymns[hymnIndex].Title = block
			case "hymn_number":
				hymns.HymnLyrics.Hymns[hymnIndex].Number = &block
			case "hymn_source":
				hymns.HymnLyrics.Hymns[hymnIndex].SourceLabel = &block
			case "hymn_verse", "hymn_chorus", "hymn_bridge":
				h := &hymns.HymnLyrics.Hymns[hymnIndex]
				kind := strings.TrimPrefix(role, "hymn_")
				if len(h.Sections) == 0 || h.Sections[len(h.Sections)-1].Kind != kind {
					h.Sections = append(h.Sections, HymnSection{ID: templateID("section-", fmt.Sprintf("%s:%d", h.ID, ordinal)), Kind: kind, Lines: []Block{}})
				}
				h.Sections[len(h.Sections)-1].Lines = append(h.Sections[len(h.Sections)-1].Lines, block)
			case "back_summary":
				appendItem(&back[0].Items)
			case "announcement":
				appendItem(&back[1].Items)
			case "prayer":
				appendItem(&back[2].Items)
			}
		}
	}
	document.Components = append([]Component{cover}, bodies...)
	document.Components = append(document.Components, hymns)
	document.Components = append(document.Components, back...)
	recognitionFixedSlots(&document, canonical.Metadata)
	validation, err := ValidateDocument(document)
	if err != nil {
		return DocumentCandidate{}, nil, fmt.Errorf("%w: document: %v", ErrRecognition, err)
	}
	issues = append(issues, validation...)
	return DocumentCandidate{Document: &document, Evidence: allowed}, issues, nil
}

// These are output-template positions, not recognition assumptions about the PDF.
func recognitionFixedSlots(document *Document, canonical CanonicalMetadata) {
	add := func(page int, element string, x, y, w, h, size float64) {
		document.LayoutManifest.Pages[page].FixedSlots = append(document.LayoutManifest.Pages[page].FixedSlots, FixedSlot{ID: fmt.Sprintf("fixed-%d-%s", page, element), Element: element, sourcePage: page + 1, Box: Box{X: x, Y: y, Width: w, Height: h}, Style: ParagraphStyle{FontSize: size, LineHeight: size * 1.25, Align: "left"}})
	}
	for _, f := range []struct {
		element          string
		x, y, w, h, size float64
	}{
		{"logo", .06, .05, .18, .12, 12}, {"masthead", .28, .08, .65, .06, 24},
		{"date", .1, .185, .2, .025, 11}, {"issueNumber", .32, .185, .18, .025, 11}, {"pastor", .1, .215, .4, .025, 11},
		{"visionMission", .45, .15, .5, .025, 10}, {"visionFellowship", .45, .18, .5, .025, 10}, {"visionCommitment", .45, .21, .5, .025, 10},
		{"titleLabel", .08, .25, .16, .025, 11}, {"title", .27, .25, .66, .03, 14},
		{"welcomeLabel", .08, .29, .15, .025, 11}, {"worshipLabel", .08, .34, .15, .025, 11}, {"workLabel", .08, .39, .15, .025, 11}, {"wordLabel", .08, .48, .15, .025, 11}, {"verseLabel", .08, .79, .15, .025, 11},
	} {
		add(0, f.element, f.x, f.y, f.w, f.h, f.size)
	}
	if canonical.Subtitle != "" {
		add(0, "subtitle", .27, .28, .66, .025, 12)
	}
	add(1, "title", .12, .07, .76, .08, 26)
	if canonical.Subtitle != "" {
		add(1, "subtitle", .12, .20, .76, .05, 20)
	}
	add(1, "topRule", .1, .29, .8, .003, 9)
	last := len(document.Pages) - 1
	add(last, "announcementsLabel", .06, .24, .4, .025, 12)
	add(last, "prayersLabel", .06, .44, .4, .025, 12)
	add(last, "summarySidebarTitle", .025, .04, .05, .15, 12)
	add(last, "summarySidebarTagline", .075, .04, .05, .15, 10)
	add(last, "summaryFrame", .13, .025, .82, .2, 10)
	add(last, "announcementsFrame", .04, .23, .91, .19, 10)
	add(last, "prayersFrame", .04, .43, .91, .12, 10)
	add(last-1, "hymnLabel", .06, .07, .85, .04, 16)
}

var recognitionCoverLabels = map[string]*regexp.Regexp{
	"cover_welcome": regexp.MustCompile(`(?i)^\s*(?:[一二三四][、．.]\s*)?Welcome\s*[:：]\s*`),
	"cover_worship": regexp.MustCompile(`(?i)^\s*(?:[一二三四][、．.]\s*)?Worship\s*[:：]\s*`),
	"cover_work":    regexp.MustCompile(`(?i)^\s*(?:[一二三四][、．.]\s*)?Work\s*[:：]\s*`),
	"cover_word":    regexp.MustCompile(`(?i)^\s*(?:[一二三四][、．.]\s*)?Word\s*[:：]\s*`),
}

// Fixed reader labels may share one native run with editable prose.
func recognitionContentParts(parts []GeometryText, role string) []GeometryText {
	label := recognitionCoverLabels[role]
	if label == nil {
		return parts
	}
	var text strings.Builder
	for _, part := range parts {
		text.WriteString(part.Text)
	}
	match := label.FindStringIndex(text.String())
	if match == nil {
		return parts
	}
	parts = append([]GeometryText(nil), parts...)
	remaining := match[1]
	for len(parts) > 0 && remaining > 0 {
		if remaining >= len(parts[0].Text) {
			remaining -= len(parts[0].Text)
			parts = parts[1:]
		} else {
			parts[0].Text = parts[0].Text[remaining:]
			remaining = 0
		}
	}
	return parts
}
