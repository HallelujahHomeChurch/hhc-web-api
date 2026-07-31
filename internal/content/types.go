package content

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalid        = errors.New("invalid content input")
	ErrNotFound       = errors.New("content not found")
	ErrConflict       = errors.New("content conflict")
	ErrPrecondition   = errors.New("content precondition failed")
	ErrNotPublishable = errors.New("content not publishable")
)

type Module string

const (
	ModuleNews    Module = "news"
	ModuleHistory Module = "history"
	ModuleVideos  Module = "videos"
)

const (
	StatusDraft           = "draft"
	StatusPublishing      = "publishing"
	StatusPublished       = "published"
	StatusUnpublishing    = "unpublishing"
	StatusPublishFailed   = "publish_failed"
	StatusUnpublishFailed = "unpublish_failed"
	StatusUnpublished     = "unpublished"
	StatusArchived        = "archived"
)

type Translation struct {
	Locale    string `json:"locale"`
	Title     string `json:"title"`
	Summary   string `json:"summary,omitempty"`
	Body      string `json:"body,omitempty"`
	DateLabel string `json:"dateLabel,omitempty"`
	ImageAlt  string `json:"imageAlt,omitempty"`
}

type WriteInput struct {
	Slug           string        `json:"slug,omitempty"`
	DisplayDate    string        `json:"displayDate,omitempty"`
	SortOrder      int           `json:"sortOrder,omitempty"`
	YouTubeVideoID string        `json:"youtubeVideoId,omitempty"`
	CoverAssetID   string        `json:"coverAssetId,omitempty"`
	Featured       bool          `json:"featured,omitempty"`
	HomeEligible   bool          `json:"homeEligible,omitempty"`
	Translations   []Translation `json:"translations"`
}

type Item struct {
	ID               string        `json:"id"`
	Module           Module        `json:"module"`
	Status           string        `json:"status"`
	Version          int64         `json:"version"`
	Slug             string        `json:"slug,omitempty"`
	DisplayDate      string        `json:"displayDate,omitempty"`
	SortOrder        int           `json:"sortOrder,omitempty"`
	YouTubeVideoID   string        `json:"youtubeVideoId,omitempty"`
	CoverAssetID     string        `json:"coverAssetId,omitempty"`
	CoverURL         string        `json:"coverUrl,omitempty"`
	PublicGrantID    string        `json:"-"`
	PublishedCoverID string        `json:"-"`
	IsPublished      bool          `json:"isPublished"`
	PublishedVersion int64         `json:"publishedVersion,omitempty"`
	Featured         bool          `json:"featured,omitempty"`
	HomeEligible     bool          `json:"homeEligible,omitempty"`
	Translations     []Translation `json:"translations"`
	CreatedBy        string        `json:"createdBy"`
	UpdatedBy        string        `json:"updatedBy"`
	PublishedAt      *time.Time    `json:"publishedAt,omitempty"`
	CreatedAt        time.Time     `json:"createdAt"`
	UpdatedAt        time.Time     `json:"updatedAt"`
}

type Revision struct {
	Version   int64     `json:"version"`
	Snapshot  Item      `json:"snapshot"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}

type PublicItem struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Summary        string `json:"summary,omitempty"`
	Body           string `json:"body,omitempty"`
	DateLabel      string `json:"dateLabel,omitempty"`
	DisplayDate    string `json:"displayDate,omitempty"`
	ImageAlt       string `json:"imageAlt,omitempty"`
	ImageURL       string `json:"imageUrl,omitempty"`
	Href           string `json:"href,omitempty"`
	YouTubeVideoID string `json:"youtubeVideoId,omitempty"`
	SortOrder      int    `json:"sortOrder,omitempty"`
	Featured       bool   `json:"featured,omitempty"`
	HomeEligible   bool   `json:"homeEligible,omitempty"`
}

type Page struct {
	Items    []Item
	Page     int
	PageSize int
	Total    int64
}

type ListOptions struct {
	Query     string
	Status    string
	Sort      string
	Direction string
	Page      int
	PageSize  int
}

type Repository interface {
	CreateContent(context.Context, Module, WriteInput, string, string, time.Time) (Item, error)
	ListContent(context.Context, Module, ListOptions) (Page, error)
	GetContent(context.Context, Module, string) (Item, error)
	UpdateContent(context.Context, Module, string, int64, WriteInput, string, time.Time) (Item, error)
	PublishContent(context.Context, Module, string, int64, string, time.Time) (Item, error)
	UnpublishContent(context.Context, Module, string, int64, string, time.Time) (Item, error)
	ContentRevisions(context.Context, Module, string) ([]Revision, error)
	RestoreContent(context.Context, Module, string, int64, int64, string, time.Time) (Item, error)
	ArchiveContent(context.Context, Module, string, int64, string, time.Time) (Item, error)
	RestoreArchivedContent(context.Context, Module, string, int64, string, time.Time) (Item, error)
	PublicContent(context.Context, Module, string, int) ([]PublicItem, error)
	PublicNews(context.Context, string, string) (PublicItem, string, error)
}
