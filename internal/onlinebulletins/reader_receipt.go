package onlinebulletins

import (
	"encoding/json"
	"time"
)

const OfflineValidity = 7 * 24 * time.Hour

// MemberContent is intentionally not Document: extraction provenance never
// crosses the member boundary. Components retain semantic IDs and typography.
type MemberContent struct {
	IllustrationAssets            map[string]string `json:"-"`
	HeaderSpeakerStartComponentID string            `json:"headerSpeakerStartComponentId,omitempty"`
	PrintedBodyPageCount          int               `json:"printedBodyPageCount"`
	SchemaVersion                 string            `json:"schemaVersion"`
	TemplateVersion               string            `json:"templateVersion"`
	TemplateSnapshot              *TemplateSettings `json:"templateSnapshot,omitempty"`
	Pages                         []Page            `json:"pages"`
	LayoutManifest                LayoutManifest    `json:"layoutManifest"`
	Components                    []Component       `json:"components"`
}

func ProjectMemberContent(source Document) (MemberContent, error) {
	raw, err := json.Marshal(source)
	if err != nil {
		return MemberContent{}, err
	}
	var content MemberContent
	if err = json.Unmarshal(raw, &content); err != nil {
		return MemberContent{}, err
	}
	content.HeaderSpeakerStartComponentID = headerSpeakerStart(source)
	content.PrintedBodyPageCount = source.SourcePageCount - 2
	content.IllustrationAssets = map[string]string{}
	for i := range content.Components {
		for j := range content.Components[i].Illustrations {
			picture := &content.Components[i].Illustrations[j]
			content.IllustrationAssets[picture.ID] = picture.AssetID
			picture.AssetID, picture.PNG = "", ""
		}
		for _, block := range componentBlocks(&content.Components[i]) {
			for j := range block.Sentences {
				block.Sentences[j].Source = nil
			}
		}
	}
	return content, nil
}

type PDFAction struct {
	IssueID string `json:"issueId"`
	Series  string `json:"series"`
	Locale  string `json:"locale"`
}

type MemberDocument struct {
	IssueID             string            `json:"issueId"`
	DocumentID          string            `json:"documentId"`
	Series              string            `json:"series"`
	ContentLocale       string            `json:"contentLocale"`
	Revision            int64             `json:"revision"`
	CanonicalMetadata   CanonicalMetadata `json:"canonicalMetadata"`
	MetadataSyncPending bool              `json:"metadataSyncPending"`
	PDFPublished        bool              `json:"pdfPublished"`
	Download            *PDFAction        `json:"download,omitempty"`
	Content             MemberContent     `json:"content"`
}

type ReaderAccessInput struct {
	Revision        *int64 `json:"revision,omitempty"`
	ClientRequestID string `json:"clientRequestId"`
	ReceiptID       string `json:"receiptId,omitempty"`
}

type ReaderAccess struct {
	AccountID         string    `json:"accountId"`
	DocumentID        string    `json:"documentId"`
	Series            string    `json:"series"`
	ContentLocale     string    `json:"contentLocale"`
	Revision          int64     `json:"revision"`
	CurrentRevision   int64     `json:"currentRevision"`
	ReceiptID         string    `json:"receiptId"`
	TraceCode         string    `json:"traceCode"`
	ValidatedAt       time.Time `json:"validatedAt"`
	OfflineValidUntil time.Time `json:"offlineValidUntil"`
}

type ReaderResponse struct {
	Document MemberDocument `json:"document"`
	Access   ReaderAccess   `json:"access"`
}

type DiscoveryItem struct {
	IssueNumber       *int              `json:"issueNumber"`
	IssueDate         string            `json:"issueDate"`
	IssueID           string            `json:"issueId"`
	Series            string            `json:"series"`
	ContentLocale     string            `json:"contentLocale"`
	CanonicalMetadata CanonicalMetadata `json:"canonicalMetadata"`
	DocumentID        string            `json:"documentId,omitempty"`
	OnlineRevision    *int64            `json:"onlineRevision"`
	PDFPublished      bool              `json:"pdfPublished"`
	Download          *PDFAction        `json:"download,omitempty"`
}

type DiscoveryPage struct {
	Items  []DiscoveryItem `json:"items"`
	Total  int64           `json:"total"`
	Offset int             `json:"offset"`
	Limit  int             `json:"limit"`
}

// Only the known source mapping can turn article-level attribution into a
// section start. Publish the semantic ID, never the private PDF coordinates.
func headerSpeakerStart(source Document) string {
	if source.LayoutManifest.RendererVersion != "v9" && source.LayoutManifest.RendererVersion != "v10" && source.LayoutManifest.RendererVersion != "v11" {
		return ""
	}
	onOpeningPage := func(block Block) bool {
		if len(block.Sentences) == 0 {
			return false
		}
		for _, sentence := range block.Sentences {
			if sentence.Source == nil || sentence.Source.Page != 2 {
				return false
			}
		}
		return true
	}
	for _, component := range source.Components {
		if component.Type != "bodySection" || component.BodySection == nil {
			continue
		}
		body := component.BodySection
		if body.Header == nil || !onOpeningPage(body.Title) {
			return ""
		}
		for _, person := range body.Contributors {
			if person.Role == "speaker" {
				return ""
			}
		}
		speakers := 0
		for _, person := range body.Header.Contributors {
			if person.Role == "speaker" {
				speakers++
				if !onOpeningPage(person.Name) {
					return ""
				}
			}
		}
		if speakers == 1 {
			return component.ID
		}
		return ""
	}
	return ""
}
