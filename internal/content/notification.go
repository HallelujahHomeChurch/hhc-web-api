package content

import (
	"fmt"
	"github.com/google/uuid"
	"strings"
	"time"
	"unicode/utf8"
)

type StatementNotificationRequest struct {
	RequestID string   `json:"requestId"`
	Channels  []string `json:"channels"`
	Subject   string   `json:"subject"`
	Body      string   `json:"body"`
}

func (r StatementNotificationRequest) Valid() bool {
	if _, err := uuid.Parse(r.RequestID); err != nil {
		return false
	}
	if len(r.Channels) < 1 || len(r.Channels) > 2 || strings.TrimSpace(r.Subject) == "" || !utf8.ValidString(r.Subject) || utf8.RuneCountInString(r.Subject) > 200 || strings.TrimSpace(r.Body) == "" || len(r.Body) > 20000 {
		return false
	}
	seen := map[string]bool{}
	for _, channel := range r.Channels {
		if (channel != "email" && channel != "web_push") || seen[channel] {
			return false
		}
		seen[channel] = true
	}
	return true
}

type StatementNotificationBatch struct {
	ID               string     `json:"id"`
	ContentID        string     `json:"contentId"`
	PublishedVersion int64      `json:"publishedVersion"`
	RequestID        string     `json:"requestId"`
	Channel          string     `json:"channel"`
	CampaignID       string     `json:"campaignId,omitempty"`
	Status           string     `json:"status"`
	ActorID          string     `json:"-"`
	Subject          string     `json:"subject"`
	Body             string     `json:"body"`
	Href             string     `json:"href"`
	CancelledAt      *time.Time `json:"cancelledAt"`
	CreatedAt        time.Time  `json:"createdAt"`
}

func (b StatementNotificationBatch) IdempotencyKey() string {
	return fmt.Sprintf("statement:%s:%d:%s:%s", b.ContentID, b.PublishedVersion, b.RequestID, b.Channel)
}
