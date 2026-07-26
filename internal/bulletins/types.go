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
	IssueDate   string    `json:"issueDate"`
	Locale      string    `json:"locale"`
	Title       string    `json:"title"`
	DownloadURL string    `json:"downloadUrl"`
	PublishedAt time.Time `json:"publishedAt"`
	Version     int64     `json:"version"`
}

type Workflow struct {
	ID               string    `json:"id"`
	Status           string    `json:"status"`
	AggregateVersion int64     `json:"aggregateVersion"`
	CreatedAt        time.Time `json:"createdAt"`
}

type CreateIssueInput struct {
	IssueDate string `json:"issueDate"`
}
type PutVersionInput struct {
	Locale      string `json:"locale"`
	Title       string `json:"title"`
	PDFAssetID  string `json:"pdfAssetId"`
	PDFFileName string `json:"pdfFileName"`
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
	Items    []PublicBulletin `json:"items"`
	Page     int              `json:"page"`
	PageSize int              `json:"pageSize"`
	Total    int64            `json:"total"`
}

type Repository interface {
	CreateIssue(context.Context, string, string, string, time.Time) (Issue, error)
	ListIssues(context.Context, int, int, string) (Page, error)
	GetIssue(context.Context, string) (Issue, error)
	PutVersion(context.Context, string, int64, PutVersionInput, string, time.Time) (Issue, error)
	StartPublish(context.Context, string, string, int64, string, time.Time) (Workflow, error)
	Unpublish(context.Context, string, string, int64, string, time.Time) (Issue, error)
	GetPublicLatest(context.Context, string) (PublicBulletin, error)
	GetPublicByDate(context.Context, string, string) (PublicBulletin, error)
	ListPublic(context.Context, string, int, int) (PublicPage, error)
}
