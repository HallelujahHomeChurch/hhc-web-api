package onlinebulletins

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const MaxDocumentBytes = 8 << 20

var (
	idPattern    = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	hashPattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	assetPattern = regexp.MustCompile(`^/assets/weekly/v[12]/[a-zA-Z0-9_-]*[0-9a-f]{64}\.(woff2|png|svg|webp)$`)
)

func DecodeDocument(r io.Reader) (Document, error) {
	var d Document
	data, err := io.ReadAll(io.LimitReader(r, MaxDocumentBytes+1))
	if err != nil {
		return d, err
	}
	if len(data) > MaxDocumentBytes {
		return d, errors.New("online document exceeds size limit")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&d); err != nil {
		return d, errors.New("invalid online document JSON")
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return d, errors.New("trailing online document JSON")
	}
	_, err = ValidateDocument(d)
	return d, err
}

func finite(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func validBox(b Box) bool {
	return finite(b.X) && finite(b.Y) && finite(b.Width) && finite(b.Height) && b.X >= 0 && b.Y >= 0 && b.Width > 0 && b.Height > 0 && b.X+b.Width <= 1.000000001 && b.Y+b.Height <= 1.000000001
}
func fontRole(role string) bool {
	switch role {
	case "body", "scripture", "emphasis", "reference", "foreignText", "symbol":
		return true
	}
	return false
}
func boundedText(text string, required bool) bool {
	return utf8.ValidString(text) && utf8.RuneCountInString(text) <= 4096 && (!required || strings.TrimSpace(text) != "")
}

func validManifestAssets(assets []Asset) bool {
	if len(assets) == 0 || len(assets) > 64 {
		return false
	}
	urls := map[string]bool{}
	for _, asset := range assets {
		if !hashPattern.MatchString(asset.SHA256) || !assetPattern.MatchString(asset.URL) || !strings.Contains(asset.URL, asset.SHA256+".") || urls[asset.URL] || (asset.Kind != "font" && asset.Kind != "decoration") || (asset.Kind == "font" && (!fontRole(asset.FontRole) || !strings.HasSuffix(asset.URL, ".woff2"))) || (asset.Kind == "decoration" && (asset.FontRole != "" || strings.HasSuffix(asset.URL, ".woff2"))) {
			return false
		}
		urls[asset.URL] = true
	}
	return true
}

// ValidateDocument distinguishes malformed data from editable, blocking review
// findings. It validates a draft, not proof of authoritative layout measurement.
func ValidateDocument(d Document) ([]ReviewIssue, error) {
	invalid := func(reason string) ([]ReviewIssue, error) {
		return nil, fmt.Errorf("invalid online document: %s", reason)
	}
	if uuid.Validate(d.IssueID) != nil || d.Series != "general" || (d.ContentLocale != "zh-Hant" && d.ContentLocale != "zh-Hans") || d.SchemaVersion != "1" || (d.TemplateVersion != "v1" && d.TemplateVersion != "v2") || (d.ContentLocale == "zh-Hans" && d.TemplateVersion != "v2") {
		return invalid("unsupported identity or schema/template")
	}
	if !hashPattern.MatchString(d.SourceAssetChecksum) || d.SourcePageCount < 4 || d.SourcePageCount > 40 || len(d.Pages) != d.SourcePageCount || len(d.Components) < 6 || len(d.Components) > 256 {
		return invalid("provenance or collection limits")
	}
	m := d.LayoutManifest
	if m.RendererVersion == "v9" || m.RendererVersion == "v10" || m.RendererVersion == "v11" {
		if d.TemplateSnapshot == nil || d.TemplateSnapshot.Version < 1 || d.TemplateSnapshot.Version > 9007199254740991 {
			return invalid("missing or invalid template snapshot")
		}
		text, ok := NormalizeTemplateText(d.TemplateSnapshot.TemplateText)
		if !ok || text != d.TemplateSnapshot.TemplateText {
			return invalid("invalid template snapshot text")
		}
	} else if d.TemplateSnapshot != nil {
		return invalid("template snapshot requires V9 or later")
	}
	if m.TemplateVersion != d.TemplateVersion || (m.RendererVersion != d.TemplateVersion && m.RendererVersion != "v3" && m.RendererVersion != "v4" && m.RendererVersion != "v5" && m.RendererVersion != "v6" && m.RendererVersion != "v7" && m.RendererVersion != "v8" && m.RendererVersion != "v9" && m.RendererVersion != "v10" && m.RendererVersion != "v11") || !hashPattern.MatchString(m.RendererArtifactSha256) || (m.ContentHash != "" && !hashPattern.MatchString(m.ContentHash)) || (m.LayoutValidationHash != "" && !hashPattern.MatchString(m.LayoutValidationHash)) || len(m.Pages) != len(d.Pages) || len(m.Assets) == 0 || len(m.Assets) > 64 {
		return invalid("layout identity or limits")
	}
	if !validManifestAssets(m.Assets) {
		return invalid("immutable asset")
	}
	if err := ValidateIllustrations(d); err != nil {
		return invalid("source illustration")
	}
	ids := map[string]bool{}
	addID := func(id string) bool {
		if !idPattern.MatchString(id) || ids[id] {
			return false
		}
		ids[id] = true
		return true
	}
	for i, p := range d.Pages {
		if !addID(p.ID) || !finite(p.Width) || !finite(p.Height) || p.Width <= 0 || p.Height <= 0 || p.Width > 2000 || p.Height > 2000 || m.Pages[i].PageID != p.ID || m.Pages[i].Slots == nil {
			return invalid("page definition/reference")
		}
	}
	type anchor struct {
		component, block string
		length           int
	}
	sentences := map[string]anchor{}
	blocks := map[string]string{}
	components := map[string]bool{}
	counts := map[string]int{}
	var issues []ReviewIssue
	blockCount := 0
	checkBlocks := func(component string, list []Block) error {
		if len(list) > 4096 {
			return errors.New("block limit")
		}
		for _, b := range list {
			blockCount++
			if blockCount > 4096 || !addID(b.ID) || len(b.Sentences) == 0 || len(b.Sentences) > 1024 {
				return errors.New("block definition/limits")
			}
			blocks[b.ID] = component
			s := b.Style
			if !validParagraphStyle(s) {
				return errors.New("paragraph style")
			}
			for _, sentence := range b.Sentences {
				if strings.HasPrefix(sentence.ID, "canonical-") || !addID(sentence.ID) || len(sentences) >= 50000 || len(sentence.Spans) == 0 || len(sentence.Spans) > 32 {
					return errors.New("sentence definition/limits")
				}
				var text strings.Builder
				for _, span := range sentence.Spans {
					if span.Text == "" || !boundedText(span.Text, false) || !fontRole(span.FontRole) || (span.FontSize != nil && (!finite(*span.FontSize) || *span.FontSize < 6 || *span.FontSize > 96)) {
						return errors.New("span")
					}
					text.WriteString(span.Text)
				}
				if !boundedText(text.String(), true) {
					return errors.New("sentence text limit")
				}
				if loc := sentence.Source; loc != nil && (loc.Page < 1 || loc.Page > d.SourcePageCount || !validBox(loc.Box)) {
					return errors.New("source location")
				}
				sentences[sentence.ID] = anchor{component, b.ID, utf8.RuneCountInString(text.String())}
			}
		}
		return nil
	}
	checkItems := func(component string, items []Item, required bool) error {
		if len(items) > 256 || (required && len(items) == 0) {
			return errors.New("item limit")
		}
		for _, item := range items {
			if !addID(item.ID) || item.Blocks == nil || (item.Title == nil && len(item.Blocks) == 0) {
				return errors.New("empty item")
			}
			if item.Title != nil {
				if err := checkBlocks(component, []Block{*item.Title}); err != nil {
					return err
				}
			}
			if err := checkBlocks(component, item.Blocks); err != nil {
				return err
			}
		}
		return nil
	}
	for _, c := range d.Components {
		if !addID(c.ID) {
			return invalid("duplicate/invalid component ID")
		}
		components[c.ID] = true
		counts[c.Type]++
		payloads := 0
		if c.Cover != nil {
			payloads++
		}
		if c.BodySection != nil {
			payloads++
		}
		if c.HymnLyrics != nil {
			payloads++
		}
		if c.Items != nil {
			payloads++
		}
		if payloads != 1 {
			return invalid("component union")
		}
		var err error
		switch c.Type {
		case "cover":
			if c.Cover == nil {
				return invalid("cover payload")
			}
			cover := c.Cover
			if len(cover.Welcome) == 0 || len(cover.WeeklyVerses) == 0 {
				return invalid("empty cover")
			}
			for _, list := range [][]Block{cover.Welcome, cover.WeeklyVerses} {
				if err = checkBlocks(c.ID, list); err != nil {
					break
				}
			}
			if err == nil {
				for _, list := range [][]Item{cover.Worship, cover.Work, cover.WordQuestions} {
					if err = checkItems(c.ID, list, true); err != nil {
						break
					}
				}
			}
		case "bodySection":
			if c.BodySection == nil {
				return invalid("body payload")
			}
			body := c.BodySection
			if header := body.Header; header != nil {
				if counts["bodySection"] != 1 || header.Contributors == nil || len(header.Contributors) > 3 {
					return invalid("body header")
				}
				blocks := []Block{header.LectureDate}
				roles := map[string]bool{}
				for _, person := range header.Contributors {
					if (person.Role != "speaker" && person.Role != "transcriber" && person.Role != "editor") || roles[person.Role] {
						return invalid("body header contributor")
					}
					roles[person.Role] = true
					blocks = append(blocks, person.Name)
				}
				if len(header.Contributors) != 3 {
					issues = append(issues, ReviewIssue{Code: "missing_body_header_contributors", ComponentID: c.ID, Blocking: true})
				}
				if err = checkBlocks(c.ID, blocks); err != nil {
					return invalid(err.Error())
				}
			}
			switch body.Kind {
			case "sermon", "testimony", "teaching", "reflection", "unknown":
				// Optional editorial classification does not alter readable content.
			default:
				return invalid("body kind")
			}
			if len(body.Blocks) == 0 || len(body.Contributors) > 32 {
				return invalid("body fields")
			}
			if err = checkBlocks(c.ID, []Block{body.Title}); err != nil {
				return invalid(err.Error())
			}
			if body.Subtitle != nil {
				if err = checkBlocks(c.ID, []Block{*body.Subtitle}); err != nil {
					return invalid(err.Error())
				}
			}
			for _, p := range body.Contributors {
				if p.Role != "speaker" && p.Role != "transcriber" && p.Role != "editor" && p.Role != "author" {
					return invalid("contributor")
				}
				if err = checkBlocks(c.ID, []Block{p.Name}); err != nil {
					return invalid(err.Error())
				}
			}
			err = checkBlocks(c.ID, body.Blocks)
		case "hymnLyrics":
			if c.HymnLyrics == nil || len(c.HymnLyrics.Hymns) == 0 || len(c.HymnLyrics.Hymns) > 32 {
				return invalid("hymn payload")
			}
			for _, h := range c.HymnLyrics.Hymns {
				if !addID(h.ID) || len(h.Sections) == 0 || len(h.Sections) > 64 {
					return invalid("hymn fields")
				}
				if err = checkBlocks(c.ID, []Block{h.Title}); err != nil {
					return invalid(err.Error())
				}
				for _, optional := range []*Block{h.Number, h.SourceLabel} {
					if optional != nil {
						if err = checkBlocks(c.ID, []Block{*optional}); err != nil {
							return invalid(err.Error())
						}
					}
				}
				for _, s := range h.Sections {
					if !addID(s.ID) || (s.Kind != "verse" && s.Kind != "chorus" && s.Kind != "bridge") || len(s.Lines) == 0 {
						return invalid("hymn section")
					}
					if err = checkBlocks(c.ID, s.Lines); err != nil {
						break
					}
				}
				if err != nil {
					break
				}
			}
		case "backSummary", "announcements", "victoriesAndPrayers":
			if c.Items == nil {
				return invalid("back payload")
			}
			err = checkItems(c.ID, c.Items, false)
		default:
			return invalid("component type")
		}
		if err != nil {
			return invalid(err.Error())
		}
	}
	for _, kind := range []string{"cover", "hymnLyrics", "backSummary", "announcements", "victoriesAndPrayers"} {
		if counts[kind] != 1 {
			return invalid("missing/duplicate required component")
		}
	}
	if counts["bodySection"] == 0 {
		return invalid("missing body")
	}
	covered := map[string]int{}
	slots := map[string]Slot{}
	slotCount := 0
	for _, page := range m.Pages {
		if len(page.FixedSlots) > 128 {
			return invalid("fixed slot limit")
		}
		for _, slot := range page.FixedSlots {
			if historicalFixedElement(slot.Element) && m.RendererVersion != "v6" && m.RendererVersion != "v7" && m.RendererVersion != "v8" && m.RendererVersion != "v9" && m.RendererVersion != "v10" && m.RendererVersion != "v11" {
				return invalid("historical fixed element requires V6")
			}
			if !addID(slot.ID) || !fixedElement(slot.Element) || !validBox(slot.Box) || !validParagraphStyle(slot.Style) {
				return invalid("fixed slot definition")
			}
		}
		for _, slot := range page.Slots {
			slotCount++
			if slotCount > 50000 || !addID(slot.ID) || !components[slot.ComponentID] || blocks[slot.BlockID] != slot.ComponentID || !validBox(slot.Box) || len(slot.Fragments) == 0 || len(slot.Fragments) > 1024 {
				return invalid("slot definition/references")
			}
			if slot.ContinuationOf != "" {
				prev, ok := slots[slot.ContinuationOf]
				if !ok || prev.ComponentID != slot.ComponentID || prev.BlockID != slot.BlockID {
					return invalid("continuation reference")
				}
			}
			for _, f := range slot.Fragments {
				a, ok := sentences[f.SentenceID]
				if !ok || a.component != slot.ComponentID || a.block != slot.BlockID || f.Start != covered[f.SentenceID] || f.End <= f.Start || f.End > a.length {
					return invalid("fragment reference/coverage")
				}
				covered[f.SentenceID] = f.End
			}
			slots[slot.ID] = slot
		}
	}
	for id, a := range sentences {
		if covered[id] != a.length {
			return invalid("unassigned sentence")
		}
	}
	data, err := json.Marshal(d)
	if err != nil || len(data) > MaxDocumentBytes {
		return invalid("document size")
	}
	return issues, nil
}

func validParagraphStyle(s ParagraphStyle) bool {
	return finite(s.FontSize) && finite(s.LineHeight) && finite(s.LetterSpacing) && s.LetterSpacing >= -.5 && s.LetterSpacing <= 1 && s.FontSize >= 6 && s.FontSize <= 96 && s.LineHeight >= s.FontSize && s.LineHeight <= 192 &&
		(s.Align == "" || s.Align == "left" || s.Align == "center" || s.Align == "right" || s.Align == "justify") &&
		finite(s.Indent) && finite(s.FirstLineIndent) && finite(s.SpaceBefore) && finite(s.SpaceAfter) &&
		s.Indent >= 0 && s.Indent <= 20 && math.Abs(s.FirstLineIndent) <= 20 && s.SpaceBefore >= 0 && s.SpaceBefore <= 20 && s.SpaceAfter >= 0 && s.SpaceAfter <= 20
}

func fixedElement(element string) bool {
	if historicalFixedElement(element) {
		return true
	}
	switch element {
	case "summarySidebarTitle", "summarySidebarTagline":
		return true
	case "title", "subtitle", "date", "issueNumber", "pageNumber", "masthead", "vision", "visionMission", "visionFellowship", "visionCommitment", "pastor", "contact", "scanHint", "websiteQRLabel", "youtubeQRLabel", "streamQRLabel", "welcomeLabel", "worshipLabel", "workLabel", "wordLabel", "verseLabel", "hymnLabel", "summaryLabel", "announcementsLabel", "prayersLabel", "titleLabel", "speakerLabel", "speakerSeparator", "lectureDateMarker", "bodyIssueSummary", "bodySpeakerLabel", "transcriberLabel", "editorLabel", "authorLabel", "logo", "backgroundLogo", "websiteQR", "youtubeQR", "streamQR", "topRule", "footerRule", "summaryFrame", "announcementsFrame", "prayersFrame":
		return true
	}
	return false
}
