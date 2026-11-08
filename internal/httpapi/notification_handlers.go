package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/publication"
	"github.com/google/uuid"
)

type notificationSubmissionProxy interface {
	ForwardSubmission(context.Context, io.Reader, string, string, string) (*http.Response, error)
}

type customNotificationSubmission struct {
	Name          string                      `json:"name"`
	Source        json.RawMessage             `json:"source,omitempty"`
	Notifications []customNotificationCommand `json:"notifications"`
}

type customNotificationCommand struct {
	Channel              string                                         `json:"channel"`
	AudienceType         string                                         `json:"audienceType"`
	RoleID               string                                         `json:"roleId,omitempty"`
	UserIDs              []string                                       `json:"userIds,omitempty"`
	Translations         map[string]publication.NotificationTranslation `json:"translations"`
	RequiredEntitlements map[string]string                              `json:"requiredEntitlements,omitempty"`
}

func (h *Handler) submitCustomNotifications(w http.ResponseWriter, r *http.Request) {
	proxy, ok := h.engagement.(notificationSubmissionProxy)
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "engagement_service_unavailable", "Campaign service is unavailable.")
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(r.Header.Values("Idempotency-Key")) != 1 || uuid.Validate(key) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "A UUID Idempotency-Key is required.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAdminProxyBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Request body is too large.")
		return
	}
	var input customNotificationSubmission
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || !validCustomNotificationSubmission(input) {
		writeError(w, http.StatusBadRequest, "invalid_request", "The notification submission is invalid.")
		return
	}
	response, err := proxy.ForwardSubmission(r.Context(), bytes.NewReader(body), actor(r), r.Header.Get("X-HHC-Request-ID"), key)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "engagement_service_unavailable", "Campaign service is unavailable.")
		return
	}
	defer response.Body.Close()
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(response.Body, 2<<20))
}

func validCustomNotificationSubmission(input customNotificationSubmission) bool {
	if strings.TrimSpace(input.Name) == "" || len(input.Source) > 0 || len(input.Notifications) < 1 || len(input.Notifications) > 2 {
		return false
	}
	channels := map[string]bool{}
	for _, item := range input.Notifications {
		if channels[item.Channel] || (item.Channel != "email" && item.Channel != "web_push") || len(item.Translations) == 0 || len(item.RequiredEntitlements) > 0 {
			return false
		}
		channels[item.Channel] = true
		switch item.AudienceType {
		case "newsletter":
			if item.Channel != "email" || item.RoleID != "" || len(item.UserIDs) > 0 {
				return false
			}
		case "all":
			if item.Channel != "web_push" || item.RoleID != "" || len(item.UserIDs) > 0 {
				return false
			}
		case "role":
			if uuid.Validate(item.RoleID) != nil || len(item.UserIDs) > 0 {
				return false
			}
		case "users":
			if item.RoleID != "" || len(item.UserIDs) < 1 || len(item.UserIDs) > 500 {
				return false
			}
			seen := map[string]bool{}
			for _, id := range item.UserIDs {
				if uuid.Validate(id) != nil || seen[id] {
					return false
				}
				seen[id] = true
			}
		default:
			return false
		}
	}
	return true
}

func (h *Handler) previewBulletinNotification(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		writeError(w, http.StatusServiceUnavailable, "bulletin_unavailable", "The bulletin is unavailable.")
		return
	}
	preview, err := h.service.PreviewNotification(r.Context(), r.PathValue("issueID"), r.URL.Query().Get("series"))
	if err != nil {
		handleNotificationError(w, err)
		return
	}
	writeData(w, http.StatusOK, preview, nil)
}

func (h *Handler) submitBulletinNotification(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		writeError(w, http.StatusServiceUnavailable, "bulletin_unavailable", "The bulletin is unavailable.")
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(r.Header.Values("Idempotency-Key")) != 1 || uuid.Validate(key) != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "A UUID Idempotency-Key is required.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "Request body is too large.")
		return
	}
	if trimmed := strings.TrimSpace(string(body)); trimmed != "" && trimmed != "{}" {
		writeError(w, http.StatusBadRequest, "invalid_request", "Weekly notification content is server generated.")
		return
	}
	result, err := h.service.SubmitNotification(r.Context(), r.PathValue("issueID"), r.URL.Query().Get("series"), actor(r), key)
	if err != nil {
		handleNotificationError(w, err)
		return
	}
	writeData(w, http.StatusAccepted, result, nil)
}

func handleNotificationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, bulletins.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", "The notification request is invalid.")
	case errors.Is(err, bulletins.ErrNotFound):
		writeError(w, http.StatusNotFound, "bulletin_not_found", "The bulletin was not found.")
	case errors.Is(err, bulletins.ErrNotPublishable), errors.Is(err, bulletins.ErrConflict):
		writeError(w, http.StatusConflict, "bulletin_not_publishable", "This bulletin series has no published edition.")
	default:
		writeError(w, http.StatusServiceUnavailable, "notification_unavailable", "Notification submission is unavailable.")
	}
}
