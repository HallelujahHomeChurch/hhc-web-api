package content

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/sitesettings"
)

const (
	statementDocumentVersion = 1
	maxStatementBlocks       = 200
	maxStatementImages       = 10
)

var richBlockID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,120}$`)
var richTextColor = regexp.MustCompile(`^#[0-9A-F]{6}$`)

type RichDocument struct {
	SchemaVersion int         `json:"schemaVersion"`
	Blocks        []RichBlock `json:"blocks"`
}

type RichBlock struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Level     int            `json:"level,omitempty"`
	Alignment string         `json:"alignment,omitempty"`
	Size      string         `json:"size,omitempty"`
	AssetID   string         `json:"assetId,omitempty"`
	URL       string         `json:"url,omitempty"`
	Ordered   bool           `json:"ordered,omitempty"`
	Alt       RichImageAlt   `json:"alt,omitzero"`
	Content   []RichInline   `json:"content,omitempty"`
	Source    []RichInline   `json:"source,omitempty"`
	Caption   []RichInline   `json:"caption,omitempty"`
	Items     []RichListItem `json:"items,omitempty"`
}

type RichImageAlt struct {
	Mode string `json:"mode,omitempty"`
	Text string `json:"text,omitempty"`
}

type RichListItem struct {
	Content []RichInline `json:"content"`
}

type RichInline struct {
	Type      string       `json:"type"`
	Text      string       `json:"text,omitempty"`
	Marks     []string     `json:"marks,omitempty"`
	Color     *string      `json:"color,omitempty"`
	Highlight *string      `json:"highlight,omitempty"`
	Href      string       `json:"href,omitempty"`
	Title     string       `json:"title,omitempty"`
	Content   []RichInline `json:"content,omitempty"`
}

type StatementImageRef struct {
	Locale  string
	BlockID string
	AssetID string
}

func StatementImageRefs(translations []Translation) ([]StatementImageRef, error) {
	references := []StatementImageRef{}
	for _, translation := range translations {
		if len(translation.BodyJSON) == 0 {
			continue
		}
		document, err := ParseStatementDocument(translation.BodyJSON, false)
		if err != nil {
			return nil, err
		}
		for _, block := range document.Blocks {
			if block.Type == "image" {
				references = append(references, StatementImageRef{Locale: translation.Locale, BlockID: block.ID, AssetID: block.AssetID})
			}
		}
	}
	return references, nil
}

func StatementTranslationFields(title string, raw json.RawMessage) (map[string]string, error) {
	document, err := ParseStatementDocument(raw, false)
	if err != nil {
		return nil, err
	}
	fields := map[string]string{"title": title}
	for blockIndex := range document.Blocks {
		block := &document.Blocks[blockIndex]
		prefix := "block." + strconv.Itoa(blockIndex)
		collectInlineTranslationFields(fields, prefix+".content", block.Content)
		collectInlineTranslationFields(fields, prefix+".source", block.Source)
		collectInlineTranslationFields(fields, prefix+".caption", block.Caption)
		if block.Type == "image" && block.Alt.Mode == "text" && block.Alt.Text != "" {
			fields[prefix+".alt"] = block.Alt.Text
		}
		for itemIndex := range block.Items {
			collectInlineTranslationFields(fields, prefix+".item."+strconv.Itoa(itemIndex), block.Items[itemIndex].Content)
		}
	}
	return fields, nil
}

func StatementSourceHash(title string, raw json.RawMessage) (string, error) {
	fields, err := StatementTranslationFields(strings.TrimSpace(title), raw)
	if err != nil {
		return "", err
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	digest := sha256.New()
	for _, key := range keys {
		digest.Write([]byte(key))
		digest.Write([]byte{0})
		digest.Write([]byte(strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(fields[key])))
		digest.Write([]byte{0})
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func LegacyStatementDocument(body string) json.RawMessage {
	document := RichDocument{SchemaVersion: statementDocumentVersion, Blocks: []RichBlock{{ID: "legacy-body", Type: "paragraph", Alignment: "start"}}}
	if body != "" {
		document.Blocks[0].Content = []RichInline{{Type: "text", Text: body}}
	}
	encoded, _ := json.Marshal(document)
	return encoded
}

func collectInlineTranslationFields(fields map[string]string, prefix string, nodes []RichInline) {
	for index := range nodes {
		key := prefix + "." + strconv.Itoa(index)
		switch nodes[index].Type {
		case "text":
			fields[key] = nodes[index].Text
		case "link":
			collectInlineTranslationFields(fields, key+".link", nodes[index].Content)
		}
	}
}

func ApplyStatementTranslation(raw json.RawMessage, translated map[string]string) (json.RawMessage, error) {
	document, err := ParseStatementDocument(raw, false)
	if err != nil {
		return nil, err
	}
	expected := map[string]bool{"title": true}
	for blockIndex := range document.Blocks {
		block := &document.Blocks[blockIndex]
		prefix := "block." + strconv.Itoa(blockIndex)
		if !applyInlineTranslation(translated, expected, prefix+".content", block.Content) || !applyInlineTranslation(translated, expected, prefix+".source", block.Source) || !applyInlineTranslation(translated, expected, prefix+".caption", block.Caption) {
			return nil, ErrInvalid
		}
		if block.Type == "image" && block.Alt.Mode == "text" && block.Alt.Text != "" {
			key := prefix + ".alt"
			value, ok := translated[key]
			if !ok || strings.TrimSpace(value) == "" || utf8.RuneCountInString(value) > 300 {
				return nil, ErrInvalid
			}
			expected[key] = true
			block.Alt.Text = value
		}
		for itemIndex := range block.Items {
			if !applyInlineTranslation(translated, expected, prefix+".item."+strconv.Itoa(itemIndex), block.Items[itemIndex].Content) {
				return nil, ErrInvalid
			}
		}
	}
	if len(expected) != len(translated) {
		return nil, ErrInvalid
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func applyInlineTranslation(translated map[string]string, expected map[string]bool, prefix string, nodes []RichInline) bool {
	for index := range nodes {
		key := prefix + "." + strconv.Itoa(index)
		switch nodes[index].Type {
		case "text":
			value, ok := translated[key]
			if !ok || !utf8.ValidString(value) {
				return false
			}
			expected[key] = true
			nodes[index].Text = value
		case "link":
			if !applyInlineTranslation(translated, expected, key+".link", nodes[index].Content) {
				return false
			}
		}
	}
	return true
}

// ParseStatementDocument is the single validation boundary for saved Statement AST.
func ParseStatementDocument(raw json.RawMessage, publish bool) (RichDocument, error) {
	if len(raw) == 0 || len(raw) > 500_000 {
		return RichDocument{}, ErrInvalid
	}
	var envelope struct {
		SchemaVersion int               `json:"schemaVersion"`
		Blocks        []json.RawMessage `json:"blocks"`
	}
	if decodeStrict(raw, &envelope) != nil || envelope.SchemaVersion != statementDocumentVersion || len(envelope.Blocks) > maxStatementBlocks {
		return RichDocument{}, ErrInvalid
	}
	document := RichDocument{SchemaVersion: envelope.SchemaVersion, Blocks: make([]RichBlock, 0, len(envelope.Blocks))}
	seen, images, textLength := map[string]bool{}, 0, 0
	for _, rawBlock := range envelope.Blocks {
		var block RichBlock
		if decodeStrict(rawBlock, &block) != nil || !validRichBlockFields(rawBlock, block.Type) {
			return RichDocument{}, ErrInvalid
		}
		if !richBlockID.MatchString(block.ID) || seen[block.ID] {
			return RichDocument{}, ErrInvalid
		}
		seen[block.ID] = true
		if !validRichBlock(block, publish, &images, &textLength) {
			return RichDocument{}, ErrInvalid
		}
		document.Blocks = append(document.Blocks, block)
	}
	if images > maxStatementImages || textLength > 100_000 {
		return RichDocument{}, ErrInvalid
	}
	return document, nil
}

func validRichBlockFields(raw json.RawMessage, blockType string) bool {
	allowed := map[string]map[string]bool{
		"paragraph": {"id": true, "type": true, "alignment": true, "content": true},
		"heading":   {"id": true, "type": true, "level": true, "alignment": true, "content": true},
		"quote":     {"id": true, "type": true, "content": true, "source": true},
		"list":      {"id": true, "type": true, "ordered": true, "items": true},
		"image":     {"id": true, "type": true, "assetId": true, "size": true, "alignment": true, "alt": true, "caption": true},
	}[blockType]
	if allowed == nil {
		return false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return false
	}
	for field := range fields {
		if !allowed[field] {
			return false
		}
	}
	return fields["id"] != nil && fields["type"] != nil
}

func validRichBlock(block RichBlock, publish bool, images, textLength *int) bool {
	switch block.Type {
	case "paragraph":
		return noRichBlockExtras(block, "content", "alignment") && validAlignment(block.Alignment) && validRichInline(block.Content, true, textLength)
	case "heading":
		return noRichBlockExtras(block, "content", "level", "alignment") && (block.Level == 2 || block.Level == 3) && validAlignment(block.Alignment) && validRichInline(block.Content, true, textLength) && (!publish || richInlineText(block.Content) != "")
	case "quote":
		return noRichBlockExtras(block, "content", "source") && validRichInline(block.Content, true, textLength) && validRichInline(block.Source, true, textLength) && (!publish || richInlineText(block.Content) != "")
	case "list":
		if !noRichBlockExtras(block, "items", "ordered") || (publish && len(block.Items) == 0) || len(block.Items) > 100 {
			return false
		}
		for _, item := range block.Items {
			if !validRichInline(item.Content, true, textLength) || (publish && richInlineText(item.Content) == "") {
				return false
			}
		}
		return true
	case "image":
		*images = *images + 1
		if !noRichBlockExtras(block, "assetId", "size", "alignment", "alt", "caption") || !richBlockID.MatchString(block.AssetID) || !validImageSize(block.Size) || !validAlignment(block.Alignment) || !validRichInline(block.Caption, true, textLength) {
			return false
		}
		if block.Alt.Mode != "text" && block.Alt.Mode != "decorative" {
			return false
		}
		if block.Alt.Mode == "decorative" {
			return block.Alt.Text == ""
		}
		return true
	default:
		return false
	}
}

func noRichBlockExtras(block RichBlock, allowed ...string) bool {
	want := map[string]bool{}
	for _, field := range allowed {
		want[field] = true
	}
	return (want["level"] || block.Level == 0) &&
		(want["alignment"] || block.Alignment == "") &&
		(want["size"] || block.Size == "") &&
		(want["assetId"] || block.AssetID == "") &&
		(want["url"] || block.URL == "") &&
		(want["ordered"] || !block.Ordered) &&
		(want["alt"] || block.Alt == (RichImageAlt{})) &&
		(want["content"] || len(block.Content) == 0) &&
		(want["source"] || len(block.Source) == 0) &&
		(want["caption"] || len(block.Caption) == 0) &&
		(want["items"] || len(block.Items) == 0)
}

func BuildPublicStatementDocument(raw json.RawMessage, publicURLs map[string]string) (json.RawMessage, error) {
	document, err := ParseStatementDocument(raw, true)
	if err != nil {
		return nil, err
	}
	for index := range document.Blocks {
		block := &document.Blocks[index]
		if block.Type != "image" {
			continue
		}
		publicURL := strings.TrimSpace(publicURLs[block.AssetID])
		if publicURL == "" {
			return nil, ErrInvalid
		}
		block.AssetID = ""
		block.URL = publicURL
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	return encoded, nil
}

func validRichInline(nodes []RichInline, links bool, textLength *int) bool {
	for _, node := range nodes {
		switch node.Type {
		case "text":
			if node.Text == "" || node.Href != "" || node.Title != "" || len(node.Content) != 0 || !validMarks(node.Marks) || !utf8.ValidString(node.Text) || (node.Color != nil && !richTextColor.MatchString(*node.Color)) || (node.Highlight != nil && !richTextColor.MatchString(*node.Highlight)) {
				return false
			}
			*textLength += utf8.RuneCountInString(node.Text)
		case "lineBreak":
			if node.Text != "" || node.Href != "" || node.Title != "" || len(node.Marks) != 0 || len(node.Content) != 0 || node.Color != nil || node.Highlight != nil {
				return false
			}
		case "link":
			if !links || node.Text != "" || len(node.Marks) != 0 || len(node.Content) == 0 || node.Color != nil || node.Highlight != nil || !validRichLink(node.Href) || !validRichInline(node.Content, false, textLength) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func validMarks(marks []string) bool {
	seen := map[string]bool{}
	for _, mark := range marks {
		if (mark != "strong" && mark != "emphasis") || seen[mark] {
			return false
		}
		seen[mark] = true
	}
	return true
}

func validRichLink(value string) bool {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "/") {
		parsed, err := url.Parse(value)
		if err != nil || parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil || strings.HasPrefix(value, "//") {
			return false
		}
		clean := path.Clean(parsed.Path)
		return clean == parsed.Path && clean != "/api" && !strings.HasPrefix(clean, "/api/") && clean != "/priv" && !strings.HasPrefix(clean, "/priv/") && clean != "/admin" && !strings.HasPrefix(clean, "/admin/")
	}
	return sitesettings.ValidExternalURL(value)
}

func validAlignment(value string) bool {
	return value == "" || value == "start" || value == "center" || value == "end"
}
func validImageSize(value string) bool {
	return value == "" || value == "small" || value == "medium" || value == "full"
}

func StatementDocumentText(document RichDocument) string {
	lines := make([]string, 0, len(document.Blocks))
	for _, block := range document.Blocks {
		switch block.Type {
		case "paragraph", "heading":
			lines = appendTextLine(lines, richInlineText(block.Content))
		case "quote":
			lines = appendTextLine(lines, richInlineText(block.Content))
			lines = appendTextLine(lines, richInlineText(block.Source))
		case "list":
			for index, item := range block.Items {
				prefix := "- "
				if block.Ordered {
					prefix = strconv.Itoa(index+1) + ". "
				}
				if text := richInlineText(item.Content); text != "" {
					lines = append(lines, prefix+text)
				}
			}
		case "image":
			lines = appendTextLine(lines, richInlineText(block.Caption))
		}
	}
	return strings.Join(lines, "\n\n")
}

func appendTextLine(lines []string, value string) []string {
	if value := strings.TrimSpace(value); value != "" {
		return append(lines, value)
	}
	return lines
}

func richInlineText(nodes []RichInline) string {
	var value strings.Builder
	for _, node := range nodes {
		switch node.Type {
		case "text":
			value.WriteString(node.Text)
		case "lineBreak":
			value.WriteByte('\n')
		case "link":
			value.WriteString(richInlineText(node.Content))
		}
	}
	return strings.TrimSpace(value.String())
}
