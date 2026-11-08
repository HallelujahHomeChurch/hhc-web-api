package onlinebulletins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
)

const MaxIllustrations = 8
const MaxIllustrationBytes = 2 << 20
const MaxIllustrationTotalBytes = 2 << 20

var ErrSourceRepair = errors.New("invalid source-linked repair")

// Illustration references sanitized private Asset bytes. PNG is populated only
// in an authorized response, never in an immutable stored document or worker input.
type Illustration struct {
	ID           string `json:"id"`
	ComponentID  string `json:"componentId"`
	AfterBlockID string `json:"afterBlockId,omitempty"`
	PageID       string `json:"pageId"`
	Box          Box    `json:"box"`
	Alt          string `json:"alt"`
	AssetID      string `json:"assetId,omitempty"`
	SHA256       string `json:"sha256"`
	SizeBytes    int    `json:"sizeBytes"`
	PNG          string `json:"png,omitempty"`
}

type SourcePart struct {
	imageSHA  string
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Page      int    `json:"page"`
	Box       Box    `json:"box"`
	Text      string `json:"text,omitempty"`
	FontRole  string `json:"fontRole,omitempty"`
	PNG       string `json:"png,omitempty"`
	Available bool   `json:"available"`
}

type SourceImage struct {
	AssetID   string `json:"assetId"`
	SHA256    string `json:"sha256"`
	SizeBytes int    `json:"sizeBytes"`
}

type SourceRepairCatalog struct {
	Images           map[string]SourceImage `json:"-"`
	DocumentID       string                 `json:"documentId"`
	Version          int64                  `json:"version"`
	Revision         int64                  `json:"revision"`
	CanonicalVersion int64                  `json:"canonicalVersion"`
	SourceChecksum   string                 `json:"sourceChecksum"`
	Parts            []SourcePart           `json:"parts"`
}

type SourceRepairInput struct {
	Remove           bool   `json:"remove,omitempty"`
	OnlineVersion    int64  `json:"-"`
	CanonicalVersion int64  `json:"canonicalVersion"`
	Revision         int64  `json:"revision"`
	SourceChecksum   string `json:"sourceChecksum"`
	PartID           string `json:"partId"`
	ComponentID      string `json:"componentId"`
	AfterBlockID     string `json:"afterBlockId,omitempty"`
	Alt              string `json:"alt,omitempty"`
	FontRole         string `json:"fontRole,omitempty"`
}

type PreparedSourceRepair struct {
	Input          SourceRepairInput
	Part           SourcePart
	Illustration   *Illustration
	RendererSHA256 string
}

type SourceRepairResult struct {
	Status string      `json:"status"`
	Draft  *SavedDraft `json:"draft,omitempty"`
}

// ApplySourceRepair consumes a server-selected source part and owned clean image,
// never caller-supplied content, source coordinates, assets or validation proof.
func ApplySourceRepair(current Document, repair PreparedSourceRepair) (Document, error) {
	if repair.Input.SourceChecksum != current.SourceAssetChecksum || repair.Part.ID != repair.Input.PartID || repair.Part.Page < 1 || repair.Part.Page > len(current.Pages) || !validBox(repair.Part.Box) {
		return Document{}, ErrSourceRepair
	}
	if _, err := ValidateDocument(current); err != nil {
		return Document{}, ErrSourceRepair
	}
	raw, _ := json.Marshal(current)
	var draft Document
	if json.Unmarshal(raw, &draft) != nil {
		return Document{}, ErrSourceRepair
	}
	page := &draft.LayoutManifest.Pages[repair.Part.Page-1]
	belongs := false
	for _, slot := range page.Slots {
		belongs = belongs || slot.ComponentID == repair.Input.ComponentID
	}
	if !belongs {
		return Document{}, ErrSourceRepair
	}
	var selected *Component
	for i := range draft.Components {
		if draft.Components[i].ID == repair.Input.ComponentID {
			selected = &draft.Components[i]
			break
		}
	}
	if selected == nil {
		return Document{}, ErrSourceRepair
	}
	switch repair.Part.Kind {
	case "image":
		if repair.Input.Remove {
			found := false
			kept := selected.Illustrations[:0]
			for _, picture := range selected.Illustrations {
				if picture.ID == repair.Part.ID {
					found = true
				} else {
					kept = append(kept, picture)
				}
			}
			if !found {
				return Document{}, ErrSourceRepair
			}
			selected.Illustrations = kept
			break
		}
		if repair.Illustration == nil || draft.TemplateSnapshot == nil || !hashPattern.MatchString(repair.RendererSHA256) {
			return Document{}, ErrSourceRepair
		}
		picture := *repair.Illustration
		picture.ID, picture.ComponentID, picture.AfterBlockID = repair.Part.ID, selected.ID, repair.Input.AfterBlockID
		picture.PageID, picture.Box, picture.Alt = page.PageID, repair.Part.Box, strings.TrimSpace(repair.Input.Alt)
		for i := range draft.Components {
			kept := draft.Components[i].Illustrations[:0]
			for _, old := range draft.Components[i].Illustrations {
				if old.ID != picture.ID {
					kept = append(kept, old)
				}
			}
			draft.Components[i].Illustrations = kept
		}
		selected.Illustrations = append(selected.Illustrations, picture)
		draft.LayoutManifest.RendererVersion, draft.LayoutManifest.RendererArtifactSha256 = "v11", repair.RendererSHA256
	case "text":
		if repair.Input.Remove || selected.BodySection == nil || !boundedText(repair.Part.Text, true) {
			return Document{}, ErrSourceRepair
		}
		role := repair.Input.FontRole
		if role == "" {
			role = "body"
		}
		if role != "body" && role != "scripture" && role != "emphasis" {
			return Document{}, ErrSourceRepair
		}
		id := "restored-block-" + repair.Part.ID
		for _, component := range draft.Components {
			copy := component
			for _, block := range componentBlocks(&copy) {
				if block.ID == id {
					return Document{}, ErrSourceRepair
				}
			}
		}
		body := selected.BodySection
		position := len(body.Blocks)
		if repair.Input.AfterBlockID != "" {
			position = -1
			if repair.Input.AfterBlockID == body.Title.ID {
				position = 0
			}
			for i, block := range body.Blocks {
				if block.ID == repair.Input.AfterBlockID {
					position = i + 1
				}
			}
			if position < 0 {
				return Document{}, ErrSourceRepair
			}
		}
		style := ParagraphStyle{FontSize: 14, LineHeight: 20}
		if len(body.Blocks) > 0 {
			style = body.Blocks[max(0, position-1)].Style
		}
		sentence := Sentence{ID: "restored-sentence-" + repair.Part.ID, Spans: []Span{{Text: repair.Part.Text, FontRole: role}}, Source: &SourceLocation{Page: repair.Part.Page, Box: repair.Part.Box}}
		block := Block{ID: id, Style: style, Sentences: []Sentence{sentence}}
		body.Blocks = append(body.Blocks[:position], append([]Block{block}, body.Blocks[position:]...)...)
		page.Slots = append(page.Slots, Slot{ID: "restored-slot-" + repair.Part.ID, ComponentID: selected.ID, BlockID: id, Box: repair.Part.Box, Fragments: []Fragment{{SentenceID: sentence.ID, Start: 0, End: utf8.RuneCountInString(repair.Part.Text)}}})
	default:
		return Document{}, ErrSourceRepair
	}
	draft.LayoutManifest.ContentHash, draft.LayoutManifest.LayoutValidationHash = "", ""
	if _, err := ValidateDocument(draft); err != nil {
		return Document{}, ErrSourceRepair
	}
	return draft, nil
}

// CatalogParts is called only on retained, privacy-filtered extraction evidence.
// Source IDs bind page, geometry and content to this exact PDF checksum.
func CatalogParts(evidence GeometryDocument) ([]SourcePart, error) {
	if !hashPattern.MatchString(evidence.SourceSHA256) || evidence.PopplerVersion != PopplerVersion || len(evidence.Pages) < 4 || len(evidence.Pages) > 40 {
		return nil, ErrSourceRepair
	}
	parts := []SourcePart{}
	appendPart := func(part SourcePart, identity any) {
		raw, _ := json.Marshal([]any{evidence.SourceSHA256, part.Kind, part.Page, part.Box, identity})
		sum := sha256.Sum256(raw)
		part.ID = hex.EncodeToString(sum[:])
		parts = append(parts, part)
	}
	for _, page := range evidence.Pages {
		for _, image := range page.Images {
			if !validBox(image.Box) || !hashPattern.MatchString(image.SHA256) {
				return nil, ErrSourceRepair
			}
			if page.Number == 1 && (image.Box.Y >= .85 || isExcludedCoverQR(image)) {
				continue
			}
			appendPart(SourcePart{Kind: "image", Page: page.Number, Box: image.Box, imageSHA: image.SHA256}, image.SHA256)
		}
		for _, text := range page.Texts {
			if !validBox(text.Box) || !boundedText(text.Text, true) {
				return nil, ErrSourceRepair
			}
			if page.Number == 1 && text.Box.Y >= .85 {
				continue
			}
			appendPart(SourcePart{Kind: "text", Page: page.Number, Box: text.Box, Text: text.Text}, []any{text.Text, text.FontFamily, text.FontSize})
		}
	}
	if len(parts) > 10000 {
		return nil, ErrSourceRepair
	}
	return parts, nil
}

func ValidateIllustrations(document Document) error {
	for _, component := range document.Components {
		for _, image := range component.Illustrations {
			if image.ComponentID != component.ID {
				return ErrSourceRepair
			}
		}
	}
	images := DocumentIllustrations(document)
	if len(images) == 0 {
		return nil
	}
	if document.LayoutManifest.RendererVersion != "v11" || len(images) > MaxIllustrations {
		return ErrSourceRepair
	}
	seen, total := map[string]bool{}, 0
	for _, image := range images {
		if !hashPattern.MatchString(image.ID) || seen[image.ID] || !hashPattern.MatchString(image.SHA256) || uuid.Validate(image.AssetID) != nil || image.PNG != "" || !validBox(image.Box) || image.SizeBytes < 1 || image.SizeBytes > MaxIllustrationBytes || !boundedText(image.Alt, true) || len([]rune(strings.TrimSpace(image.Alt))) > 256 {
			return ErrSourceRepair
		}
		seen[image.ID] = true
		total += image.SizeBytes
		if total > MaxIllustrationTotalBytes {
			return ErrSourceRepair
		}
		page, component, block := false, false, image.AfterBlockID == ""
		for _, layout := range document.LayoutManifest.Pages {
			if layout.PageID != image.PageID {
				continue
			}
			page = true
			for _, slot := range layout.Slots {
				component = component || slot.ComponentID == image.ComponentID
				block = block || slot.ComponentID == image.ComponentID && slot.BlockID == image.AfterBlockID
			}
		}
		if !page || !component || !block {
			return fmt.Errorf("%w: illustration must belong to the selected source page and section", ErrSourceRepair)
		}
	}
	return nil
}

func DocumentIllustrations(document Document) []Illustration {
	var images []Illustration
	for _, component := range document.Components {
		images = append(images, component.Illustrations...)
	}
	return images
}
