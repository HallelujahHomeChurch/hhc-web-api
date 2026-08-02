package bulletins

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalid        = errors.New("invalid input")
	ErrNotFound       = errors.New("not found")
	ErrConflict       = errors.New("conflict")
	ErrPrecondition   = errors.New("precondition failed")
	ErrNotPublishable = errors.New("not publishable")
)

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
	Locale         string     `json:"locale"`
	Title          string     `json:"title"`
	Subtitle       string     `json:"subtitle"`
	PDFAssetID     string     `json:"pdfAssetId"`
	PDFFileName    string     `json:"pdfFileName"`
	PublicGrantID  string     `json:"publicGrantId,omitempty"`
	Status         string     `json:"status"`
	WorkflowStatus string     `json:"workflowStatus,omitempty"`
	WorkflowError  string     `json:"workflowError,omitempty"`
	Version        int64      `json:"version"`
	PublishedAt    *time.Time `json:"publishedAt,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type PublicBulletin struct {
	IssueNumber      *int      `json:"issueNumber,omitempty"`
	IssueDate        string    `json:"issueDate"`
	Locale           string    `json:"locale"`
	Title            string    `json:"title"`
	Subtitle         string    `json:"subtitle"`
	DownloadURL      string    `json:"downloadUrl"`
	DownloadFileName string    `json:"downloadFileName"`
	PublishedAt      time.Time `json:"publishedAt"`
	Version          int64     `json:"version"`
}

type PublicIssue struct {
	IssueNumber *int             `json:"issueNumber,omitempty"`
	IssueDate   string           `json:"issueDate"`
	Versions    []PublicBulletin `json:"versions"`
}

type Workflow struct {
	ID               string    `json:"id"`
	Status           string    `json:"status"`
	AggregateVersion int64     `json:"aggregateVersion"`
	CreatedAt        time.Time `json:"createdAt"`
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
	Locale      string `json:"locale"`
	Title       string `json:"title"`
	Subtitle    string `json:"subtitle"`
	PDFAssetID  string `json:"pdfAssetId"`
	PDFFileName string `json:"pdfFileName"`
}
type UpdateVersionInput struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
}
type PublishInput struct {
	Locale string `json:"locale"`
}
type Page struct {
	Items    []Issue `json:"items"`
	Page     int     `json:"page"`
	PageSize int     `json:"pageSize"`
	Total    int64   `json:"total"`
}
type PublicPage struct {
	Items    []PublicIssue `json:"items"`
	Page     int           `json:"page"`
	PageSize int           `json:"pageSize"`
	Total    int64         `json:"total"`
}

type Repository interface {
	CreateIssue(context.Context, int, string, string, string, time.Time) (Issue, error)
	ListIssues(context.Context, int, int, string, string) (Page, error)
	GetIssue(context.Context, string) (Issue, error)
	UpdateIssue(context.Context, string, int64, UpdateIssueInput, string, time.Time) (Issue, error)
	PutVersion(context.Context, string, int64, PutVersionInput, string, time.Time) (Issue, error)
	UpdateVersion(context.Context, string, string, int64, string, string, string, time.Time) (Issue, error)
	DeleteVersion(context.Context, string, string, int64, string, time.Time) (Issue, error)
	StartPublish(context.Context, string, string, int64, string, time.Time) (Workflow, error)
	Unpublish(context.Context, string, string, int64, string, time.Time) (Issue, error)
	DeleteIssue(context.Context, string, int64, string, time.Time) error
	IssueRevisions(context.Context, string) ([]Revision, error)
	RestoreIssueRevision(context.Context, string, int64, int64, string, time.Time) (Issue, error)
	GetPublicLatest(context.Context, string) (PublicBulletin, error)
	GetPublicByDate(context.Context, string, string) (PublicBulletin, error)
	ListPublic(context.Context, int, int) (PublicPage, error)
}
