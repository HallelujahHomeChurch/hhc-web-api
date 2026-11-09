package onlinebulletins

import (
	"errors"
	"time"
)

var (
	ErrExtractionJob   = errors.New("invalid extraction job")
	ErrExtractionLease = errors.New("extraction lease lost")
	ErrLayoutJob       = errors.New("invalid layout job")
	ErrLayoutLease     = errors.New("layout lease lost")
)

type ExtractionJob struct {
	LeaseUntil                         time.Time
	ID, DocumentID, LeaseToken         string
	SourceAssetID, SourceAssetChecksum string
	TemplateVersion, ExtractorVersion  string
	Attempts                           int
	Canonical                          CanonicalIssue
	TemplateSnapshot                   *TemplateSettings
	LearningRules                      []LearningRuleInput
}

type LayoutJob struct {
	Automatic                                                       bool
	Repair                                                          *LayoutRepairEvidence
	ID, DocumentID, LeaseToken, ContentHash, RendererArtifactSHA256 string
	Revision                                                        int64
	Attempts                                                        int
	LeaseUntil                                                      time.Time
	Canonical                                                       CanonicalMetadata
	Document                                                        Document
}

type ConversionJob struct {
	AssessmentAttempts                                    []UpdateAttempt
	PublicationEventID                                    string
	ID, DocumentID, IssueID, LeaseToken, ConverterVersion string
	Attempts                                              int
	LeaseUntil                                            time.Time
	Source                                                Document
	SourceMetadata, TargetMetadata                        CanonicalMetadata
	InitializesMetadata                                   bool
}
