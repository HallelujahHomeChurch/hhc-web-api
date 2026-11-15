package bulletins

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalid              = errors.New("invalid input")
	ErrNotFound             = errors.New("not found")
	ErrConflict             = errors.New("conflict")
	ErrPrecondition         = errors.New("precondition failed")
	ErrNotPublishable       = errors.New("not publishable")
	ErrAuditUnavailable     = errors.New("audit recording unavailable")
	ErrOnlineDocumentExists = errors.New("online bulletin history exists")
)

type BulletinSeries string
type BulletinLocale string

type BulletinEdition struct {
	Series BulletinSeries `json:"series"`
	Locale BulletinLocale `json:"locale"`
}

const (
	BulletinSeriesGeneral            BulletinSeries = "general"
	BulletinSeriesChildren           BulletinSeries = "children"
	BulletinLocaleTraditionalChinese BulletinLocale = "zh-Hant"
	BulletinLocaleSimplifiedChinese  BulletinLocale = "zh-Hans"
	BulletinLocaleEnglish            BulletinLocale = "en"
)

var BulletinEditionOrder = [...]BulletinEdition{
	{Series: BulletinSeriesGeneral, Locale: BulletinLocaleTraditionalChinese},
	{Series: BulletinSeriesGeneral, Locale: BulletinLocaleSimplifiedChinese},
	{Series: BulletinSeriesGeneral, Locale: BulletinLocaleEnglish},
	{Series: BulletinSeriesChildren, Locale: BulletinLocaleTraditionalChinese},
	{Series: BulletinSeriesChildren, Locale: BulletinLocaleEnglish},
}

func NormalizeBulletinEdition(series, locale string) (BulletinEdition, bool) {
	if series == "" {
		series = string(BulletinSeriesGeneral)
	}
	for _, edition := range BulletinEditionOrder {
		if series == string(edition.Series) && locale == string(edition.Locale) {
			return edition, true
		}
	}
	return BulletinEdition{}, false
}

func IsBulletinEdition(locale string) bool {
	_, ok := NormalizeBulletinEdition("general", locale)
	return ok
}

func IsBulletinTranslationTarget(value string) bool {
	return value == string(BulletinLocaleSimplifiedChinese) || value == string(BulletinLocaleEnglish)
}

type Issue struct {
	ID          string     `json:"id"`
	IssueNumber *int       `json:"issueNumber,omitempty"`
	IssueDate   string     `json:"issueDate"`
	Status      string     `json:"status"`
	Version     int64      `json:"version"`
	CreatedBy   string     `json:"createdBy"`
	UpdatedBy   string     `json:"updatedBy"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
	CreatedAt   time.Time  `json:"createdAt"`
	UpdatedAt   time.Time  `json:"updatedAt"`
	Versions    []Version  `json:"versions"`
}

type Version struct {
	ID             string     `json:"id"`
	IssueID        string     `json:"issueId"`
	Series         string     `json:"series"`
	Locale         string     `json:"locale"`
	Title          string     `json:"title"`
	Subtitle       string     `json:"subtitle"`
	PDFAssetID     string     `json:"pdfAssetId"`
	PDFFileName    string     `json:"pdfFileName"`
	PublicGrantID  string     `json:"-"`
	Status         string     `json:"status"`
	WorkflowStatus string     `json:"workflowStatus,omitempty"`
	WorkflowError  string     `json:"workflowError,omitempty"`
	Version        int64      `json:"version"`
	PublishedAt    *time.Time `json:"publishedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type Workflow struct {
	OnlinePublication *OnlinePublicationOutcome `json:"onlinePublication,omitempty"`
	ID                string                    `json:"id"`
	Status            string                    `json:"status"`
	AggregateVersion  int64                     `json:"aggregateVersion"`
	CreatedAt         time.Time                 `json:"createdAt"`
}

type Revision struct {
	Version   int64     `json:"version"`
	Snapshot  Issue     `json:"snapshot"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}

type CreateIssueInput struct {
	IssueNumber int    `json:"issueNumber"`
	IssueDate   string `json:"issueDate"`
}
type UpdateIssueInput struct {
	IssueNumber int    `json:"issueNumber"`
	IssueDate   string `json:"issueDate"`
}
type PutVersionInput struct {
	AutoPublish       bool   `json:"autoPublish,omitempty"`
	NotifySubscribers bool   `json:"notifySubscribers,omitempty"`
	Series            string `json:"series,omitempty"`
	Locale            string `json:"locale"`
	Title             string `json:"title"`
	Subtitle          string `json:"subtitle"`
	PDFAssetID        string `json:"pdfAssetId"`
	PDFFileName       string `json:"pdfFileName"`
}
type UpdateVersionInput struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
}
type PublishInput struct {
	OnlinePublication string `json:"onlinePublication,omitempty"`
	OnlineVersion     *int64 `json:"onlineVersion,omitempty"`
	Series            string `json:"series,omitempty"`
	Locale            string `json:"locale"`
	NotifySubscribers bool   `json:"notifySubscribers,omitempty"`
}

type UnpublishInput struct {
	Series            string `json:"series,omitempty"`
	Locale            string `json:"locale"`
	NotifySubscribers bool   `json:"notifySubscribers,omitempty"`
	UnpublishOnline   bool   `json:"unpublishOnline,omitempty"`
	OnlineVersion     *int64 `json:"onlineVersion,omitempty"`
}
type Page struct {
	Items    []Issue `json:"items"`
	Page     int     `json:"page"`
	PageSize int     `json:"pageSize"`
	Total    int64   `json:"total"`
}
type ProtectedBulletin struct {
	IssueID          string    `json:"issueId"`
	IssueNumber      *int      `json:"issueNumber,omitempty"`
	IssueDate        string    `json:"issueDate"`
	Series           string    `json:"series"`
	Locale           string    `json:"locale"`
	Title            string    `json:"title"`
	Subtitle         string    `json:"subtitle"`
	DownloadName     string    `json:"downloadFileName"`
	PublishedAt      time.Time `json:"publishedAt"`
	Version          int64     `json:"version"`
	ProtectedAssetID string    `json:"-"`
}

type ProtectedPage struct {
	Items    []ProtectedBulletin `json:"items"`
	Page     int                 `json:"page"`
	PageSize int                 `json:"pageSize"`
	Total    int64               `json:"total"`
}

type Repository interface {
	CreateIssue(context.Context, int, string, string, string, time.Time) (Issue, error)
	ListIssues(context.Context, int, int, string, string, string, string) (Page, error)
	GetIssue(context.Context, string) (Issue, error)
	UpdateIssue(context.Context, string, int64, UpdateIssueInput, string, time.Time) (Issue, error)
	PutVersion(context.Context, string, int64, PutVersionInput, string, time.Time) (Issue, error)
	UpdateVersion(context.Context, string, string, string, int64, string, string, string, time.Time) (Issue, error)
	DeleteVersion(context.Context, string, string, string, int64, string, time.Time) (Issue, error)
	StartPublish(context.Context, string, string, string, int64, bool, string, time.Time) (Workflow, error)
	Unpublish(context.Context, string, string, string, int64, string, time.Time) (Issue, error)
	DeleteIssue(context.Context, string, int64, string, time.Time) error
	IssueRevisions(context.Context, string) ([]Revision, error)
	RestoreIssueRevision(context.Context, string, int64, int64, string, time.Time) (Issue, error)
}
