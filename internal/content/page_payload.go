package content

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

type HomePageData struct {
	HeroTitle      string `json:"heroTitle"`
	HeroSubtitle   string `json:"heroSubtitle"`
	NewsTitle      string `json:"newsTitle"`
	MoreNews       string `json:"moreNews"`
	WeeklyTitle    string `json:"weeklyTitle"`
	DownloadWeekly string `json:"downloadWeekly"`
	VideosTitle    string `json:"videosTitle"`
	VideosSubtitle string `json:"videosSubtitle"`
	WatchMore      string `json:"watchMore"`
	AboutTitle     string `json:"aboutTitle"`
	AboutBody      string `json:"aboutBody"`
	AboutCTA       string `json:"aboutCta"`
	LocationsTitle string `json:"locationsTitle"`
	MapLink        string `json:"mapLink"`
}

type AboutCard struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

type AboutVisionSection struct {
	Eyebrow string      `json:"eyebrow"`
	Title   string      `json:"title"`
	Body    string      `json:"body,omitempty"`
	Cards   []AboutCard `json:"cards,omitempty"`
}

type AboutVision struct {
	Intro           string               `json:"intro"`
	ImageAlt        string               `json:"imageAlt"`
	ActionsImageAlt string               `json:"actionsImageAlt"`
	Sections        []AboutVisionSection `json:"sections"`
}

type ScriptureQuote struct {
	Lines []string `json:"lines"`
	Cite  string   `json:"cite"`
}

type AboutHistory struct {
	Scripture []ScriptureQuote `json:"scripture"`
	ImageAlt  string           `json:"imageAlt"`
	Intro     string           `json:"intro"`
	Title     string           `json:"title"`
}

type AboutPageData struct {
	HeroTitle    string       `json:"heroTitle"`
	HeroSubtitle string       `json:"heroSubtitle"`
	Vision       AboutVision  `json:"vision"`
	History      AboutHistory `json:"history"`
}

type LegalSection struct {
	Title string   `json:"title"`
	Body  []string `json:"body"`
}

type LegalPageData struct {
	HeroTitle      string         `json:"heroTitle"`
	HeroSubtitle   string         `json:"heroSubtitle"`
	UpdatedAtLabel string         `json:"updatedAtLabel"`
	UpdatedAt      string         `json:"updatedAt"`
	Intro          string         `json:"intro"`
	Sections       []LegalSection `json:"sections"`
}

type pageEnvelope struct {
	SchemaVersion int             `json:"schemaVersion"`
	Template      string          `json:"template"`
	Data          json.RawMessage `json:"data"`
}

var cjkDate = regexp.MustCompile(`^(\d{4})年(\d{1,2})月(\d{1,2})日$`)
var koreanDate = regexp.MustCompile(`^(\d{4})년\s*(\d{1,2})월\s*(\d{1,2})일$`)

func ValidatePageDefinition(key, template, route string) error {
	wantTemplate, wantRoute, ok := PageDefinition(key)
	if !ok || template != wantTemplate || route != wantRoute {
		return ErrInvalid
	}
	return nil
}

func PageDefinition(key string) (template, route string, ok bool) {
	switch key {
	case "home":
		return "home.v1", "/", true
	case "about":
		return "about.v1", "/about", true
	case "privacy-policy":
		return "legal.v1", "/privacy-policy", true
	case "terms-of-use":
		return "legal.v1", "/terms-of-use", true
	default:
		return "", "", false
	}
}

func ValidatePagePayload(key string, raw json.RawMessage) error {
	if len(raw) == 0 || len(raw) > 200_000 {
		return ErrInvalid
	}
	var envelope pageEnvelope
	if err := decodeStrict(raw, &envelope); err != nil || envelope.SchemaVersion != 1 {
		return ErrInvalid
	}
	template, _, ok := PageDefinition(key)
	if !ok || envelope.Template != template {
		return ErrInvalid
	}
	var valid bool
	switch template {
	case "home.v1":
		var data HomePageData
		valid = decodeStrict(envelope.Data, &data) == nil && validHomePage(data)
	case "about.v1":
		var data AboutPageData
		valid = decodeStrict(envelope.Data, &data) == nil && validAboutPage(data)
	case "legal.v1":
		var data LegalPageData
		valid = decodeStrict(envelope.Data, &data) == nil && validLegalPage(data)
	}
	if !valid {
		return ErrInvalid
	}
	return nil
}

func PagePayloadMetadata(key string, raw json.RawMessage) (title, summary string, err error) {
	if err = ValidatePagePayload(key, raw); err != nil {
		return "", "", err
	}
	var envelope pageEnvelope
	_ = json.Unmarshal(raw, &envelope)
	switch envelope.Template {
	case "home.v1":
		var data HomePageData
		_ = json.Unmarshal(envelope.Data, &data)
		return data.HeroTitle, data.HeroSubtitle, nil
	case "about.v1":
		var data AboutPageData
		_ = json.Unmarshal(envelope.Data, &data)
		return data.HeroTitle, data.HeroSubtitle, nil
	default:
		var data LegalPageData
		_ = json.Unmarshal(envelope.Data, &data)
		if data.HeroSubtitle != "" {
			return data.HeroTitle, data.HeroSubtitle, nil
		}
		return data.HeroTitle, data.Intro, nil
	}
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ErrInvalid
	}
	return nil
}

func validHomePage(data HomePageData) bool {
	return plainRequired(data.HeroTitle, 200) && plainRequired(data.HeroSubtitle, 500) &&
		plainRequired(data.NewsTitle, 200) && plainRequired(data.MoreNews, 200) &&
		plainRequired(data.WeeklyTitle, 200) && plainRequired(data.DownloadWeekly, 200) &&
		plainRequired(data.VideosTitle, 200) && plainRequired(data.VideosSubtitle, 500) && plainRequired(data.WatchMore, 200) &&
		plainRequired(data.AboutTitle, 200) && plainRequired(data.AboutBody, 5_000) && plainRequired(data.AboutCTA, 200) &&
		plainRequired(data.LocationsTitle, 200) && plainRequired(data.MapLink, 200)
}

func validAboutPage(data AboutPageData) bool {
	if !plainRequired(data.HeroTitle, 200) || !plainRequired(data.HeroSubtitle, 500) ||
		!plainRequired(data.Vision.Intro, 10_000) || !plainRequired(data.Vision.ImageAlt, 300) || !plainRequired(data.Vision.ActionsImageAlt, 300) || len(data.Vision.Sections) != 4 ||
		!plainRequired(data.History.ImageAlt, 300) || !plainRequired(data.History.Intro, 10_000) || !plainRequired(data.History.Title, 200) || len(data.History.Scripture) == 0 || len(data.History.Scripture) > 10 {
		return false
	}
	for index, section := range data.Vision.Sections {
		if !plainRequired(section.Eyebrow, 200) || !plainRequired(section.Title, 500) || len(section.Cards) > 20 {
			return false
		}
		if index < 2 {
			if !plainRequired(section.Body, 10_000) || len(section.Cards) != 0 {
				return false
			}
		} else if section.Body != "" || len(section.Cards) == 0 {
			return false
		}
		for _, card := range section.Cards {
			if !plainRequired(card.Title, 500) || !plainRequired(card.Body, 5_000) {
				return false
			}
		}
	}
	for _, quote := range data.History.Scripture {
		if !plainRequired(quote.Cite, 500) || len(quote.Lines) == 0 || len(quote.Lines) > 20 {
			return false
		}
		for _, line := range quote.Lines {
			if !plainRequired(line, 10_000) {
				return false
			}
		}
	}
	return true
}

func validLegalPage(data LegalPageData) bool {
	if !plainRequired(data.HeroTitle, 200) || !plainOptional(data.HeroSubtitle, 500) || !plainRequired(data.UpdatedAtLabel, 100) || !validLocalizedDate(data.UpdatedAt) || !plainRequired(data.Intro, 10_000) || len(data.Sections) == 0 || len(data.Sections) > 50 {
		return false
	}
	for _, section := range data.Sections {
		if !plainRequired(section.Title, 500) || len(section.Body) == 0 || len(section.Body) > 50 {
			return false
		}
		for _, paragraph := range section.Body {
			if !plainRequired(paragraph, 10_000) {
				return false
			}
		}
	}
	return true
}

func plainRequired(value string, max int) bool {
	return strings.TrimSpace(value) != "" && plainOptional(value, max)
}

func plainOptional(value string, max int) bool {
	if !utf8.ValidString(value) || utf8.RuneCountInString(value) > max || strings.ContainsAny(value, "<>") {
		return false
	}
	lower := strings.ToLower(value)
	if strings.Contains(lower, "javascript:") || strings.Contains(lower, "data:") || strings.Contains(lower, "http://") || strings.Contains(lower, "https://") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return false
		}
	}
	return true
}

func validLocalizedDate(value string) bool {
	if _, err := time.Parse("January 2, 2006", value); err == nil {
		return true
	}
	for _, pattern := range []*regexp.Regexp{cjkDate, koreanDate} {
		match := pattern.FindStringSubmatch(value)
		if len(match) != 4 {
			continue
		}
		year, _ := strconv.Atoi(match[1])
		month, _ := strconv.Atoi(match[2])
		day, _ := strconv.Atoi(match[3])
		parsed := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
		return parsed.Year() == year && int(parsed.Month()) == month && parsed.Day() == day
	}
	return false
}
