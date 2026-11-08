package bulletins

import (
	"fmt"
	"strings"
	"time"
)

const englishNotificationFallback = "The latest weekly bulletin is now available."

type NotificationTranslation struct {
	Subject       string `json:"subject"`
	Body          string `json:"body"`
	ClickBehavior string `json:"clickBehavior"`
	ActionURL     string `json:"actionUrl"`
}

type NotificationCommand struct {
	Channel              string                             `json:"channel"`
	AudienceType         string                             `json:"audienceType"`
	Translations         map[string]NotificationTranslation `json:"translations"`
	RequiredEntitlements map[string]string                  `json:"requiredEntitlements"`
}

type NotificationPreview struct {
	Name                    string                `json:"name"`
	Series                  string                `json:"series"`
	Notifications           []NotificationCommand `json:"notifications"`
	HasPreviousNotification bool                  `json:"hasPreviousNotification"`
	LatestGeneration        *int                  `json:"latestGeneration,omitempty"`
	LatestAt                *time.Time            `json:"latestAt,omitempty"`
}

type NotificationSubmissionResult struct {
	IssueID                string `json:"issueId"`
	Series                 string `json:"series"`
	NotificationGeneration int    `json:"notificationGeneration"`
}

func BuildNotification(issue Issue, series string) (NotificationPreview, error) {
	series = stringOrGeneral(series)
	if series != string(BulletinSeriesGeneral) && series != string(BulletinSeriesChildren) {
		return NotificationPreview{}, ErrInvalid
	}
	copyByLocale := map[string]string{}
	for _, version := range issue.Versions {
		versionSeries := stringOrGeneral(version.Series)
		if version.Status != "published" || versionSeries != series {
			continue
		}
		body := strings.TrimSpace(version.Title)
		if subtitle := strings.TrimSpace(version.Subtitle); subtitle != "" && subtitle != body {
			if body != "" {
				body += "\n"
			}
			body += subtitle
		}
		copyByLocale[version.Locale] = body
	}
	if len(copyByLocale) == 0 {
		return NotificationPreview{}, ErrNotPublishable
	}
	fallback := copyByLocale["en"]
	if fallback == "" {
		fallback = englishNotificationFallback
	}
	locales := []string{"zh-Hant", "zh-Hans", "en"}
	if series == string(BulletinSeriesChildren) {
		locales = []string{"zh-Hant", "en"}
	}
	translations := make(map[string]NotificationTranslation, len(locales))
	entitlements := make(map[string]string, len(locales))
	for _, locale := range locales {
		body := copyByLocale[locale]
		if body == "" {
			body = fallback
		}
		translations[locale] = NotificationTranslation{
			Subject: notificationSubject(issue.IssueNumber, series, locale), Body: body,
			ClickBehavior: "url", ActionURL: "/" + locale + "/literature-ministry?series=" + series,
		}
		entitlements[locale] = "bulletin." + series + "." + locale + ".access"
	}
	name := "Weekly bulletin"
	if series == string(BulletinSeriesChildren) {
		name = "Children's weekly bulletin"
	}
	if issue.IssueNumber != nil {
		name += fmt.Sprintf(" %d", *issue.IssueNumber)
	}
	return NotificationPreview{Name: name, Series: series, Notifications: []NotificationCommand{
		{Channel: "email", AudienceType: "bulletin_members", Translations: translations, RequiredEntitlements: entitlements},
		{Channel: "web_push", AudienceType: "bulletin_members", Translations: translations, RequiredEntitlements: entitlements},
	}}, nil
}

func notificationSubject(number *int, series, locale string) string {
	general := map[string]string{"zh-Hant": "本週週報已發布", "zh-Hans": "本周周报已发布", "en": "This week's bulletin is now available"}
	children := map[string]string{"zh-Hant": "本週兒童週報已發布", "en": "This week's children's bulletin is now available"}
	subject := general[locale]
	if series == string(BulletinSeriesChildren) {
		subject = children[locale]
	}
	if number == nil {
		return subject
	}
	if series == string(BulletinSeriesChildren) {
		return map[string]string{"zh-Hant": fmt.Sprintf("第 %d 期兒童週報已發布", *number), "en": fmt.Sprintf("Children's weekly bulletin %d is now available", *number)}[locale]
	}
	return map[string]string{"zh-Hant": fmt.Sprintf("第 %d 期週報已發布", *number), "zh-Hans": fmt.Sprintf("第 %d 期周报已发布", *number), "en": fmt.Sprintf("Weekly bulletin %d is now available", *number)}[locale]
}

func stringOrGeneral(series string) string {
	if strings.TrimSpace(series) == "" {
		return string(BulletinSeriesGeneral)
	}
	return strings.TrimSpace(series)
}
