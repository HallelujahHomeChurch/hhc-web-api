package onlinebulletins

import "time"

// Bump the parser/template rule revision when extraction semantics change.
const ExtractorVersion = "poppler-" + PopplerVersion + "-weekly-v1.23"

// Source fields are verified by the API's Asset lookup, never decoded from an
// Admin request body. Canonical and Online versions are independent CAS values.
type QueueExtractionInput struct {
	AutoPublish, NotifySubscribers  bool
	CanonicalVersion, OnlineVersion int64
	SourceAssetID, SourceChecksum   string
	Retry                           bool
}

type QueueSimplifiedInput struct {
	CanonicalVersion    int64 `json:"canonicalVersion"`
	SourceOnlineVersion int64 `json:"sourceOnlineVersion"`
	SourceRevision      int64 `json:"sourceRevision"`
	TargetOnlineVersion int64 `json:"-"`
	Retry               bool  `json:"retry,omitempty"`
}

type QueuedExtraction struct {
	DocumentID string   `json:"documentId"`
	Version    int64    `json:"version"`
	Job        AdminJob `json:"job"`
}

type SaveDraftInput struct {
	CanonicalVersion int64       `json:"canonicalVersion"`
	OnlineVersion    int64       `json:"-"`
	Components       []Component `json:"components"`
}

type SavedDraft struct {
	DocumentID  string `json:"documentId"`
	Version     int64  `json:"version"`
	Revision    int64  `json:"revision"`
	LayoutJobID string `json:"layoutJobId"`
}

type CompareInput struct {
	CanonicalVersion int64              `json:"canonicalVersion"`
	OnlineVersion    int64              `json:"-"`
	BaseRevision     int64              `json:"baseRevision"`
	LocalRevision    int64              `json:"localRevision"`
	IncomingRevision int64              `json:"incomingRevision"`
	Choices          []ComparisonChoice `json:"choices"`
}

type Comparison struct {
	DocumentID       string           `json:"documentId"`
	Version          int64            `json:"version"`
	CanonicalVersion int64            `json:"canonicalVersion"`
	BaseRevision     int64            `json:"baseRevision"`
	LocalRevision    int64            `json:"localRevision"`
	IncomingRevision int64            `json:"incomingRevision"`
	Units            []ComparisonUnit `json:"units"`
}

type ConfirmInput struct {
	CanonicalVersion     int64         `json:"canonicalVersion"`
	OnlineVersion        int64         `json:"-"`
	Revision             int64         `json:"revision"`
	LayoutValidationHash string        `json:"layoutValidationHash"`
	PageCount            int           `json:"pageCount"`
	AcceptedIssues       []ReviewIssue `json:"acceptedIssues"`
}

type RestoreInput struct {
	CanonicalVersion int64 `json:"canonicalVersion"`
	OnlineVersion    int64 `json:"-"`
	Revision         int64 `json:"revision"`
}

type PublishInput struct {
	CanonicalVersion int64             `json:"canonicalVersion"`
	OnlineVersion    int64             `json:"-"`
	Revision         int64             `json:"revision"`
	Mappings         []SentenceMapping `json:"mappings,omitempty"`
}

type UnpublishedRevision struct {
	DocumentID string `json:"documentId"`
	Version    int64  `json:"version"`
}

type RevisionSummary struct {
	Revision            int64     `json:"revision"`
	CreatedBy           string    `json:"createdBy"`
	CreatedAt           time.Time `json:"createdAt"`
	SourceAssetChecksum string    `json:"sourceAssetChecksum"`
	LayoutValidated     bool      `json:"layoutValidated"`
}

type RevisionPage struct {
	DocumentID string            `json:"documentId"`
	Items      []RevisionSummary `json:"items"`
	NextBefore *int64            `json:"nextBefore"`
}

type HistoricalRevision struct {
	AdminRevision
	DocumentID        string            `json:"documentId"`
	CanonicalMetadata CanonicalMetadata `json:"canonicalMetadata"`
	CreatedBy         string            `json:"createdBy"`
	CreatedAt         time.Time         `json:"createdAt"`
}

type ConfirmedRevision struct {
	DocumentID string `json:"documentId"`
	Version    int64  `json:"version"`
	Revision   int64  `json:"revision"`
}

// AdminState keeps canonical CMS metadata separate from the frozen publication.
// Revision pointers and version belong only to this exact issue/series/locale.
type AdminState struct {
	Production          *AdminProduction   `json:"production,omitempty"`
	Capabilities        *AdminCapabilities `json:"capabilities,omitempty"`
	Automation          *AdminAutomation   `json:"automation,omitempty"`
	IssueID             string             `json:"issueId"`
	Series              string             `json:"series"`
	ContentLocale       string             `json:"contentLocale"`
	CanonicalVersion    int64              `json:"canonicalVersion"`
	CanonicalMetadata   CanonicalMetadata  `json:"canonicalMetadata"`
	PDFStatus           string             `json:"pdfStatus"`
	DocumentID          string             `json:"documentId"`
	Version             int64              `json:"version"`
	DraftStatus         string             `json:"draftStatus"`
	BaseRevision        *int64             `json:"baseRevision"`
	Local               *AdminRevision     `json:"local"`
	IncomingRevision    *int64             `json:"incomingRevision"`
	PublishedRevision   *int64             `json:"publishedRevision"`
	PublishedMetadata   *CanonicalMetadata `json:"publishedMetadata"`
	MetadataSyncPending bool               `json:"metadataSyncPending"`
	Confirmed           bool               `json:"confirmed"`
	Extraction          *AdminJob          `json:"extraction"`
	Layout              *AdminJob          `json:"layout"`
	Conversion          *AdminJob          `json:"conversion,omitempty"`
	Derivation          *AdminDerivation   `json:"derivation,omitempty"`
}

type AdminAutomation struct {
	Disposition *PublicationDisposition `json:"disposition,omitempty"`
	AutoPublish bool                    `json:"autoPublish"`
	Update      *AdminJob               `json:"update"`
	Summary     string                  `json:"summary"`
	ChangeKind  string                  `json:"changeKind"`
}

// Provenance belongs only to the Admin projection, never member content.
type AdminDerivation struct {
	SourceDocumentID      string             `json:"sourceDocumentId"`
	SourceRevision        int64              `json:"sourceRevision"`
	SourceContentHash     string             `json:"sourceContentHash"`
	SourceMetadataVersion int64              `json:"sourceMetadataVersion"`
	ConverterVersion      string             `json:"converterVersion"`
	ResultRevision        *int64             `json:"resultRevision"`
	ConvertedMetadata     *CanonicalMetadata `json:"convertedMetadata"`
	Stale                 bool               `json:"stale"`
}

type AdminRevision struct {
	Revision     int64         `json:"revision"`
	Document     Document      `json:"document"`
	ReviewIssues []ReviewIssue `json:"reviewIssues"`
}

// No leases, source asset IDs, raw snapshots, or upstream error text reach Admin.
type AdminJob struct {
	ResultRevision *int64        `json:"resultRevision"`
	ReviewIssues   []ReviewIssue `json:"reviewIssues,omitempty"`
	ID             string        `json:"id"`
	Status         string        `json:"status"`
	Attempts       int           `json:"attempts"`
	ErrorCode      string        `json:"errorCode"`
}
