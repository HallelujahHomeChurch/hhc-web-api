package onlinebulletins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"sort"
	"strings"
)

// FontCoverage uses the same role-to-font mapping and Unicode ranges as the
// pinned Traditional reader. A fallback changes only the affected glyph run.
type FontCoverage map[string][][2]rune

func LoadRecognitionFontCoverage(path string, assets []Asset) (FontCoverage, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != "289411e8b8f843cfeaf6862274744f495642e74eff6ec72cc8fb1767d5eb2dab" {
		return nil, ErrTemplateAssets
	}
	var fonts map[string][][2]rune
	if err := json.Unmarshal(raw, &fonts); err != nil {
		return nil, err
	}
	coverage := FontCoverage{}
	for _, asset := range assets {
		if asset.Kind != "font" {
			continue
		}
		ranges := fonts[asset.SHA256]
		if len(ranges) == 0 {
			return nil, ErrTemplateAssets
		}
		coverage[asset.FontRole] = ranges
	}
	coverage["reference"] = coverage["body"]
	coverage["foreignText"] = coverage["body"]
	return coverage, nil
}

func (c FontCoverage) contains(role string, ch rune) bool {
	ranges := c[role]
	i := sort.Search(len(ranges), func(i int) bool { return ranges[i][1] >= ch })
	return i < len(ranges) && ranges[i][0] <= ch
}

func (c FontCoverage) apply(document *Document) []ReviewIssue {
	var issues []ReviewIssue
	for i := range document.Components {
		component := &document.Components[i]
		for _, block := range componentBlocks(component) {
			for j := range block.Sentences {
				sentence := &block.Sentences[j]
				var spans []Span
				missing := false
				for _, span := range sentence.Spans {
					runs, ok := c.glyphRuns(span)
					spans = append(spans, runs...)
					missing = missing || !ok
				}
				sentence.Spans = spans
				if missing {
					issue := ReviewIssue{Code: "unknown_font_role", ComponentID: component.ID, BlockID: block.ID, SentenceID: sentence.ID, Blocking: true}
					if sentence.Source != nil {
						issue.SourcePage = sentence.Source.Page
					}
					issues = append(issues, issue)
				}
			}
		}
	}
	return issues
}

func (c FontCoverage) glyphRuns(span Span) ([]Span, bool) {
	var runs []Span
	start, role := 0, span.FontRole
	supported := true
	for index, ch := range span.Text {
		chosen := span.FontRole
		if !strings.ContainsRune("\r\n\t", ch) && !c.contains(chosen, ch) {
			switch {
			case chosen != "symbol" && c.contains("body", ch):
				chosen = "body"
			case chosen != "symbol" && c.contains("scripture", ch):
				chosen = "scripture"
			default:
				supported = false
			}
		}
		if chosen == role {
			continue
		}
		if start < index {
			part := span
			part.Text, part.FontRole = span.Text[start:index], role
			runs = append(runs, part)
		}
		start, role = index, chosen
	}
	span.Text, span.FontRole = span.Text[start:], role
	return append(runs, span), supported
}
