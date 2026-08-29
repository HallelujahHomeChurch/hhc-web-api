package content

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalid           = errors.New("invalid content input")
	ErrNotFound          = errors.New("content not found")
	ErrConflict          = errors.New("content conflict")
	ErrPrecondition      = errors.New("content precondition failed")
	ErrLocaleSetMismatch = errors.New("locale set mismatch")
	ErrNotPublishable    = errors.New("content not publishable")
	ErrMethodNotAllowed  = errors.New("content method not allowed")
)

type Module string

const (
	ModuleNews      Module = "news"
	ModuleHistory   Module = "history"
	ModuleVideos    Module = "videos"
	ModuleLocations Module = "locations"
	ModulePages     Module = "pages"
)

const (
	StatusDraft           = "draft"
	StatusPublishing      = "publishing"
	StatusPublished       = "published"
	StatusUnpublishing    = "unpublishing"
	StatusPublishFailed   = "publish_failed"
	StatusUnpublishFailed = "unpublish_failed"
	StatusUnpublished     = "unpublished"
)

type Translation struct {
	Locale    string          `json:"locale"`
	Title     string          `json:"title"`
	Summary   string          `json:"summary,omitempty"`
	Body      string          `json:"body,omitempty"`
	DateLabel string          `json:"dateLabel,omitempty"`
	ImageAlt  string          `json:"imageAlt,omitempty"`
	BodyJSON  json.RawMessage `json:"bodyJson,omitempty"`
}

type HomeLinks struct {
	ChurchYouTube  string `json:"churchYoutube"`
	ChurchFacebook string `json:"churchFacebook"`
	MusicYouTube   string `json:"musicYoutube"`
}

type HomeLocationTranslation struct {
	Locale  string `json:"locale"`
	Name    string `json:"name"`
	Address string `json:"address"`
}

type HomeLocation struct {
	Key          string                    `json:"key"`
	MapHref      string                    `json:"mapHref"`
	SortOrder    int                       `json:"sortOrder"`
	Translations []HomeLocationTranslation `json:"translations"`
}

type WriteInput struct {
	AuthorName       string         `json:"authorName,omitempty"`
	Slug             string         `json:"slug,omitempty"`
	DisplayDate      string         `json:"displayDate,omitempty"`
	EventDate        string         `json:"eventDate,omitempty"`
	YouTubeVideoID   string         `json:"youtubeVideoId,omitempty"`
	CoverAssetID     string         `json:"coverAssetId,omitempty"`
	HomeCoverAssetID string         `json:"homeCoverAssetId,omitempty"`
	DetailLayout     string         `json:"detailLayout,omitempty"`
	Featured         bool           `json:"featured,omitempty"`
	HomeEligible     bool           `json:"homeEligible,omitempty"`
	LocationKey      string         `json:"locationKey,omitempty"`
	MapHref          string         `json:"mapHref,omitempty"`
	SortOrder        int            `json:"sortOrder,omitempty"`
	PageKey          string         `json:"pageKey,omitempty"`
	PageTemplate     string         `json:"pageTemplate,omitempty"`
	RoutePath        string         `json:"routePath,omitempty"`
	Indexable        bool           `json:"indexable,omitempty"`
	BannerAssetID    string         `json:"bannerAssetId,omitempty"`
	Links            HomeLinks      `json:"links,omitzero"`
	Locations        []HomeLocation `json:"locations,omitempty"`
	Translations     []Translation  `json:"translations"`
	DeleteLocales    []string       `json:"deleteLocales,omitempty"`
}

type Item struct {
	ID                     string         `json:"id"`
	Module                 Module         `json:"module"`
	Status                 string         `json:"status"`
	Version                int64          `json:"version"`
	Slug                   string         `json:"slug,omitempty"`
	DisplayDate            string         `json:"displayDate,omitempty"`
	EventDate              string         `json:"eventDate,omitempty"`
	YouTubeVideoID         string         `json:"youtubeVideoId,omitempty"`
	CoverAssetID           string         `json:"coverAssetId,omitempty"`
	HomeCoverAssetID       string         `json:"homeCoverAssetId,omitempty"`
	DetailLayout           string         `json:"detailLayout,omitempty"`
	CoverURL               string         `json:"coverUrl,omitempty"`
	HomeCoverURL           string         `json:"homeCoverUrl,omitempty"`
	PublicGrantID          string         `json:"-"`
	HomePublicGrantID      string         `json:"-"`
	PublishedCoverID       string         `json:"-"`
	PublishedHomeCoverID   string         `json:"-"`
	IsPublished            bool           `json:"isPublished"`
	PublishedVersion       int64          `json:"publishedVersion,omitempty"`
	AuthorName             string         `json:"authorName,omitempty"`
	Featured               bool           `json:"featured,omitempty"`
	HomeEligible           bool           `json:"homeEligible,omitempty"`
	LocationKey            string         `json:"locationKey,omitempty"`
	MapHref                string         `json:"mapHref,omitempty"`
	SortOrder              int            `json:"sortOrder,omitempty"`
	PageKey                string         `json:"pageKey,omitempty"`
	PageTemplate           string         `json:"pageTemplate,omitempty"`
	RoutePath              string         `json:"routePath,omitempty"`
	Indexable              bool           `json:"indexable,omitempty"`
	BannerAssetID          string         `json:"bannerAssetId,omitempty"`
	Links                  HomeLinks      `json:"links,omitzero"`
	Locations              []HomeLocation `json:"locations,omitempty"`
	BannerPublicGrantID    string         `json:"-"`
	PublishedBannerAssetID string         `json:"-"`
	PublishedBannerVersion int64          `json:"-"`
	Translations           []Translation  `json:"translations"`
	CreatedBy              string         `json:"createdBy"`
	UpdatedBy              string         `json:"updatedBy"`
	PublishedAt            *time.Time     `json:"publishedAt,omitempty"`
	FirstPublishedAt       *time.Time     `json:"-"`
	CreatedAt              time.Time      `json:"createdAt"`
	UpdatedAt              time.Time      `json:"updatedAt"`
}

type Revision struct {
	Version   int64     `json:"version"`
	Snapshot  Item      `json:"snapshot"`
	CreatedBy string    `json:"createdBy"`
	CreatedAt time.Time `json:"createdAt"`
}

type PublicItem struct {
	ID               string     `json:"id"`
	Title            string     `json:"title"`
	Summary          string     `json:"summary,omitempty"`
	Body             string     `json:"body,omitempty"`
	DateLabel        string     `json:"dateLabel,omitempty"`
	DisplayDate      string     `json:"displayDate,omitempty"`
	EventDate        string     `json:"eventDate,omitempty"`
	ImageAlt         string     `json:"imageAlt,omitempty"`
	ImageURL         string     `json:"imageUrl,omitempty"`
	HomeImageURL     string     `json:"homeImageUrl,omitempty"`
	Href             string     `json:"href,omitempty"`
	YouTubeVideoID   string     `json:"youtubeVideoId,omitempty"`
	Featured         bool       `json:"featured,omitempty"`
	HomeEligible     bool       `json:"homeEligible,omitempty"`
	DetailLayout     string     `json:"detailLayout,omitempty"`
	AuthorName       string     `json:"authorName,omitempty"`
	FirstPublishedAt *time.Time `json:"firstPublishedAt,omitempty"`
	LastPublishedAt  *time.Time `json:"lastPublishedAt,omitempty"`
	ResolvedLocale   string     `json:"resolvedLocale"`
	AvailableLocales []string   `json:"availableLocales"`
}

type Page struct {
	Items    []Item
	Page     int
	PageSize int
	Total    int64
}

type PublicPage struct {
	Items    []PublicItem
	Page     int
	PageSize int
	Total    int64
}

type PublicLocation struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Address          string   `json:"address"`
	MapHref          string   `json:"mapHref"`
	SortOrder        int      `json:"sortOrder"`
	ResolvedLocale   string   `json:"resolvedLocale,omitempty"`
	AvailableLocales []string `json:"availableLocales,omitempty"`
}

type PublicEditorialPage struct {
	PageKey          string          `json:"pageKey"`
	Template         string          `json:"template"`
	RoutePath        string          `json:"routePath"`
	Indexable        bool            `json:"indexable"`
	Content          json.RawMessage `json:"content"`
	ResolvedLocale   string          `json:"resolvedLocale"`
	AvailableLocales []string        `json:"availableLocales"`
	Version          int64           `json:"version"`
	PublishedAt      time.Time       `json:"publishedAt"`
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
	RestoreContent(context.Context, Module, string, int64, WriteInput, string, time.Time) (Item, error)
	PublishContent(context.Context, Module, string, int64, string, time.Time) (Item, error)
	UnpublishContent(context.Context, Module, string, int64, string, time.Time) (Item, error)
	ContentRevisions(context.Context, Module, string) ([]Revision, error)
	ContentRevision(context.Context, Module, string, int64) (Revision, error)
	DeleteContent(context.Context, Module, string, int64, string, time.Time) error
	PublicContent(context.Context, Module, string, int, int) (PublicPage, error)
	PublicNews(context.Context, string, string) (PublicItem, string, error)
	PublicLocations(context.Context, string) ([]PublicLocation, error)
	PublicEditorialPage(context.Context, string, string) (PublicEditorialPage, string, error)
}
