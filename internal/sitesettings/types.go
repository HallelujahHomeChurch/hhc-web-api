package sitesettings

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalid        = errors.New("invalid site settings")
	ErrNotFound       = errors.New("site settings not found")
	ErrPrecondition   = errors.New("site settings precondition failed")
	ErrNotPublishable = errors.New("site settings not publishable")
)

const (
	SingletonID       = "default"
	StatusDraft       = "draft"
	StatusPublished   = "published"
	StatusUnpublished = "unpublished"
)

var SupportedLocales = []string{"zh-Hant", "zh-Hans", "en", "ja", "ko"}

type NavItem struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Href    string `json:"href"`
	Visible bool   `json:"visible"`
}

type LocaleSettings struct {
	Locale                 string    `json:"locale"`
	SiteName               string    `json:"siteName"`
	EnglishName            string    `json:"englishName"`
	CopyrightHolder        string    `json:"copyrightHolder"`
	AllRightsReserved      string    `json:"allRightsReserved"`
	SEOTitleSuffix         string    `json:"seoTitleSuffix"`
	SEODescriptionFallback string    `json:"seoDescriptionFallback"`
	Header                 []NavItem `json:"header"`
	Legal                  []NavItem `json:"legal"`
}

type ExternalLinks struct {
	ChurchYouTube  string `json:"churchYoutube"`
	ChurchFacebook string `json:"churchFacebook"`
	MusicYouTube   string `json:"musicYoutube"`
}

type WriteInput struct {
	Locales []LocaleSettings `json:"locales"`
	Links   ExternalLinks    `json:"links"`
}

type Settings struct {
	ID          string           `json:"id"`
	Status      string           `json:"status"`
	Version     int64            `json:"version"`
	Locales     []LocaleSettings `json:"locales"`
	Links       ExternalLinks    `json:"links"`
	CreatedBy   string           `json:"createdBy"`
	UpdatedBy   string           `json:"updatedBy"`
	PublishedBy string           `json:"publishedBy,omitempty"`
	PublishedAt *time.Time       `json:"publishedAt,omitempty"`
	CreatedAt   time.Time        `json:"createdAt"`
	UpdatedAt   time.Time        `json:"updatedAt"`
}

type Revision struct {
	Revision     int64     `json:"revision"`
	RevisionType string    `json:"revisionType"`
	Snapshot     Settings  `json:"snapshot"`
	CreatedBy    string    `json:"createdBy"`
	CreatedAt    time.Time `json:"createdAt"`
}

type Repository interface {
	Get(context.Context) (Settings, error)
	Save(context.Context, WriteInput, int64, string, time.Time) (Settings, error)
	Publish(context.Context, int64, string, time.Time) (Settings, error)
	Unpublish(context.Context, int64, string, time.Time) (Settings, error)
	Revisions(context.Context) ([]Revision, error)
	Restore(context.Context, int64, int64, string, time.Time) (Settings, error)
}
