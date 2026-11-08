package onlinebulletins

import (
	"bytes"
	"encoding/json"
	"errors"
)

// Document stores content and immutable rendering evidence, not editable copies
// of canonical issue metadata or a reference to replaceable PDF bytes.
type Document struct {
	IssueID             string            `json:"issueId"`
	Series              string            `json:"series"`
	ContentLocale       string            `json:"contentLocale"`
	SchemaVersion       string            `json:"schemaVersion"`
	TemplateVersion     string            `json:"templateVersion"`
	TemplateSnapshot    *TemplateSettings `json:"templateSnapshot,omitempty"`
	SourceAssetChecksum string            `json:"sourceAssetChecksum"`
	SourcePageCount     int               `json:"sourcePageCount"`
	Pages               []Page            `json:"pages"`
	LayoutManifest      LayoutManifest    `json:"layoutManifest"`
	Components          []Component       `json:"components"`
}

func (d *Document) UnmarshalJSON(data []byte) error {
	type plain Document
	var decoded plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	// A null pointer must not disguise a prohibited legacy property as absent.
	if decoded.TemplateSnapshot == nil {
		var fields struct {
			Snapshot json.RawMessage `json:"templateSnapshot"`
		}
		if err := json.Unmarshal(data, &fields); err != nil {
			return err
		}
		if fields.Snapshot != nil {
			return errors.New("null template snapshot")
		}
	}
	*d = Document(decoded)
	return nil
}

type Page struct {
	ID     string  `json:"id"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// Box uses page-relative coordinates; source paper sizes remain in PDF points.
type Box struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

func (b *Box) UnmarshalJSON(data []byte) error {
	type plain Box
	return decodeRequiredLayoutNumbers(data, (*plain)(b), "x", "y", "width", "height")
}

// JSON null/missing cannot become a valid zero coordinate or indentation.
// Nested custom decoders must retain the outer unknown-field protection.
func decodeRequiredLayoutNumbers(data []byte, target any, required ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range required {
		if _, exists := fields[name]; !exists {
			return errors.New("missing layout number")
		}
	}
	for _, value := range fields {
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return errors.New("null layout property")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}

type LayoutManifest struct {
	TemplateVersion        string       `json:"templateVersion"`
	RendererVersion        string       `json:"rendererVersion"`
	RendererArtifactSha256 string       `json:"rendererArtifactSha256"`
	ContentHash            string       `json:"contentHash,omitempty"`
	LayoutValidationHash   string       `json:"layoutValidationHash,omitempty"`
	Assets                 []Asset      `json:"assets"`
	Pages                  []LayoutPage `json:"pages"`
}

type Asset struct {
	URL      string `json:"url"`
	SHA256   string `json:"sha256"`
	Kind     string `json:"kind"`
	FontRole string `json:"fontRole,omitempty"`
}

type LayoutPage struct {
	PageID     string      `json:"pageId"`
	Slots      []Slot      `json:"slots"`
	FixedSlots []FixedSlot `json:"fixedSlots,omitempty"`
}

// FixedSlot references code-owned labels or canonical metadata, never copied
// editable text. Its geometry is produced by the template, not a canvas editor.
type FixedSlot struct {
	sourcePage int            // Extraction-only placement provenance; never editable or serialized.
	ID         string         `json:"id"`
	Element    string         `json:"element"`
	Box        Box            `json:"box"`
	Style      ParagraphStyle `json:"style"`
}

// CanonicalMetadata is a publication/measurement snapshot of existing CMS fields.
// It is deliberately separate from the editable structured document.
type CanonicalMetadata struct {
	Title       string `json:"title"`
	Subtitle    string `json:"subtitle"`
	IssueNumber int    `json:"issueNumber"`
	Date        string `json:"date"`
}

type Slot struct {
	sourcePage     int        // Extraction-only placement provenance; never editable or serialized.
	ID             string     `json:"id"`
	ComponentID    string     `json:"componentId"`
	BlockID        string     `json:"blockId"`
	Box            Box        `json:"box"`
	ContinuationOf string     `json:"continuationOf,omitempty"`
	Fragments      []Fragment `json:"fragments"`
}

// Fragment offsets are Unicode code points, not UTF-8 bytes or UTF-16 units.
// A long sentence may continue onto another page without changing its ID.
type Fragment struct {
	SentenceID string `json:"sentenceId"`
	Start      int    `json:"start"`
	End        int    `json:"end"`
}

// Component is a closed tagged union. Exactly the payload for Type is present.
type Component struct {
	Illustrations []Illustration `json:"illustrations,omitempty"`
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	Cover         *Cover         `json:"cover,omitempty"`
	BodySection   *BodySection   `json:"bodySection,omitempty"`
	HymnLyrics    *HymnLyrics    `json:"hymnLyrics,omitempty"`
	Items         []Item         `json:"items,omitempty"`
}

func (c Component) MarshalJSON() ([]byte, error) {
	type plain Component
	switch c.Type {
	case "backSummary", "announcements", "victoriesAndPrayers":
		// The union requires an explicit empty array, rather than omitting its
		// payload when a week has no announcements or prayer requests.
		items := c.Items
		if items == nil {
			items = []Item{}
		}
		return json.Marshal(struct {
			plain
			Items []Item `json:"items"`
		}{plain(c), items})
	default:
		return json.Marshal(plain(c))
	}
}

type Cover struct {
	Welcome       []Block `json:"welcome"`
	Worship       []Item  `json:"worship"`
	Work          []Item  `json:"work"`
	WordQuestions []Item  `json:"wordQuestions"`
	WeeklyVerses  []Block `json:"weeklyVerses"`
}

type BodySection struct {
	Kind         string        `json:"kind"`
	Header       *BodyHeader   `json:"header,omitempty"`
	Title        Block         `json:"title"`
	Subtitle     *Block        `json:"subtitle,omitempty"`
	Contributors []Contributor `json:"contributors,omitempty"`
	Blocks       []Block       `json:"blocks"`
}

// Only the first body section owns this band. LectureDate is source content,
// not a second editable copy of the canonical issue publication date.
type BodyHeader struct {
	LectureDate  Block         `json:"lectureDate"`
	Contributors []Contributor `json:"contributors"`
}

type Contributor struct {
	Role string `json:"role"`
	Name Block  `json:"name"`
}

type Item struct {
	ID     string  `json:"id"`
	Title  *Block  `json:"title,omitempty"`
	Blocks []Block `json:"blocks"`
}

type HymnLyrics struct {
	Hymns []Hymn `json:"hymns"`
}
type Hymn struct {
	ID          string        `json:"id"`
	Number      *Block        `json:"number,omitempty"`
	Title       Block         `json:"title"`
	SourceLabel *Block        `json:"sourceLabel,omitempty"`
	Sections    []HymnSection `json:"sections"`
}
type HymnSection struct {
	ID    string  `json:"id"`
	Kind  string  `json:"kind"`
	Lines []Block `json:"lines"`
}

type Block struct {
	ID        string         `json:"id"`
	Style     ParagraphStyle `json:"style"`
	Sentences []Sentence     `json:"sentences"`
}
type ParagraphStyle struct {
	FontSize        float64 `json:"fontSize"`
	LineHeight      float64 `json:"lineHeight"`
	LetterSpacing   float64 `json:"letterSpacing,omitempty"`
	Align           string  `json:"align,omitempty"`
	Indent          float64 `json:"indent"`
	FirstLineIndent float64 `json:"firstLineIndent"`
	SpaceBefore     float64 `json:"spaceBefore"`
	SpaceAfter      float64 `json:"spaceAfter"`
}

func (s *ParagraphStyle) UnmarshalJSON(data []byte) error {
	type plain ParagraphStyle
	return decodeRequiredLayoutNumbers(data, (*plain)(s), "fontSize", "lineHeight", "indent", "firstLineIndent", "spaceBefore", "spaceAfter")
}

type Sentence struct {
	ID     string          `json:"id"`
	Spans  []Span          `json:"spans"`
	Source *SourceLocation `json:"source,omitempty"`
}
type SourceLocation struct {
	Page int `json:"page"`
	Box  Box `json:"box"`
}
type Span struct {
	Text     string   `json:"text"`
	FontRole string   `json:"fontRole"`
	FontSize *float64 `json:"fontSize,omitempty"`
}

type ReviewIssue struct {
	FixedElement string `json:"fixedElement,omitempty"`
	Code         string `json:"code"`
	ComponentID  string `json:"componentId,omitempty"`
	BlockID      string `json:"blockId,omitempty"`
	SentenceID   string `json:"sentenceId,omitempty"`
	SourcePage   int    `json:"sourcePage,omitempty"`
	Blocking     bool   `json:"blocking"`
}

type DraftStatus string

const (
	Extracting       DraftStatus = "extracting"
	ReviewRequired   DraftStatus = "reviewRequired"
	Ready            DraftStatus = "ready"
	Published        DraftStatus = "published"
	ExtractionFailed DraftStatus = "extractionFailed"
)
