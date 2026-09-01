package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/operations"
)

type operationsHTTPService interface {
	ListChurchUnits(context.Context, bool) ([]operations.ChurchUnit, error)
	GetChurchUnit(context.Context, string) (operations.ChurchUnit, error)
	CreateChurchUnit(context.Context, operations.ChurchUnitInput, string, string, string) (operations.ChurchUnit, error)
	SaveChurchUnit(context.Context, string, int64, operations.ChurchUnitInput, string, string) (operations.ChurchUnit, error)
	SetChurchUnitStatus(context.Context, string, int64, operations.Status, string, string) (operations.ChurchUnit, error)
	ListResources(context.Context, bool) ([]operations.Resource, error)
	GetResource(context.Context, string) (operations.Resource, error)
	CreateResource(context.Context, operations.ResourceInput, string, string, string) (operations.Resource, error)
	SaveResource(context.Context, string, int64, operations.ResourceInput, string, string) (operations.Resource, error)
	SetResourceStatus(context.Context, string, int64, operations.Status, string, string) (operations.Resource, error)
	ListMeetings(context.Context, bool) ([]operations.Meeting, error)
	GetMeeting(context.Context, string) (operations.Meeting, error)
	CreateMeeting(context.Context, operations.MeetingInput, string, string, string) (operations.MeetingMutation, error)
	SaveMeeting(context.Context, string, int64, operations.MeetingInput, string, string) (operations.MeetingMutation, error)
	SetMeetingStatus(context.Context, string, int64, operations.Status, string, string) (operations.Meeting, error)
	PutOverride(context.Context, string, int64, operations.OccurrenceOverrideInput, string, string) (operations.OccurrenceOverride, error)
	DeleteOverride(context.Context, string, int64, string, string, string) (operations.Meeting, error)
	ListOverrides(context.Context, string) ([]operations.OccurrenceOverride, error)
	ReplaceMeetingBindings(context.Context, string, int64, []string, string, string) (operations.Meeting, error)
	ListMeetingBindings(context.Context, string) ([]string, error)
	ListOccurrences(context.Context, operations.OccurrenceQuery) ([]operations.Occurrence, error)
	ListMediaSyncWindows(context.Context, time.Time, time.Time) ([]operations.MediaSyncWindow, error)
}

type publicMeeting struct {
	Key             string              `json:"key"`
	Name            string              `json:"name"`
	Description     string              `json:"description,omitempty"`
	Timezone        string              `json:"timezone"`
	Schedule        operations.Schedule `json:"schedule"`
	DurationMinutes int                 `json:"durationMinutes"`
	NextOccurrence  *publicOccurrence   `json:"nextOccurrence,omitempty"`
}

type publicOccurrence struct {
	OccurrenceID string                      `json:"occurrenceId"`
	MeetingKey   string                      `json:"meetingKey"`
	StartsAt     time.Time                   `json:"startsAt"`
	EndsAt       time.Time                   `json:"endsAt"`
	Status       operations.OccurrenceStatus `json:"status"`
}

type meetingDetail struct {
	operations.Meeting
	Overrides     []operations.OccurrenceOverride `json:"overrides"`
	CollectionIDs []string                        `json:"collectionIds"`
}

func (h *Handler) operationsRoutes(mux, admin *http.ServeMux) {
	mux.HandleFunc("GET /api/meetings", h.publicMeetings)
	mux.HandleFunc("GET /api/meetings/{meetingKey}", h.publicMeeting)
	mux.HandleFunc("GET /api/meeting-occurrences", h.publicMeetingOccurrences)
	mux.Handle("GET /api/meeting-sync-windows", privateNoStore(requireTrusted(h.trustedCaller, h.daprAPIToken, h.allowDevCaller, http.HandlerFunc(requireScope("assets:read", h.authenticatedMeetingSyncWindows)))))

	admin.HandleFunc("GET /api/admin/operations/church-units", requireScope("cms:read", h.adminListChurchUnits))
	admin.HandleFunc("POST /api/admin/operations/church-units", requireScope("cms:write", h.adminCreateChurchUnit))
	admin.HandleFunc("GET /api/admin/operations/church-units/{id}", requireScope("cms:read", h.adminGetChurchUnit))
	admin.HandleFunc("PUT /api/admin/operations/church-units/{id}", requireScope("cms:write", h.adminSaveChurchUnit))
	adminStatusRoutes(admin, "/api/admin/operations/church-units/{id}/", h.adminChurchUnitStatus)

	admin.HandleFunc("GET /api/admin/operations/resources", requireScope("cms:read", h.adminListResources))
	admin.HandleFunc("POST /api/admin/operations/resources", requireScope("cms:write", h.adminCreateResource))
	admin.HandleFunc("GET /api/admin/operations/resources/{id}", requireScope("cms:read", h.adminGetResource))
	admin.HandleFunc("PUT /api/admin/operations/resources/{id}", requireScope("cms:write", h.adminSaveResource))
	adminStatusRoutes(admin, "/api/admin/operations/resources/{id}/", h.adminResourceStatus)

	admin.HandleFunc("GET /api/admin/operations/meetings", requireScope("cms:read", h.adminListMeetings))
	admin.HandleFunc("POST /api/admin/operations/meetings", requireScope("cms:write", h.adminCreateMeeting))
	admin.HandleFunc("GET /api/admin/operations/meetings/{id}", requireScope("cms:read", h.adminGetMeeting))
	admin.HandleFunc("PUT /api/admin/operations/meetings/{id}", requireScope("cms:write", h.adminSaveMeeting))
	adminStatusRoutes(admin, "/api/admin/operations/meetings/{id}/", h.adminMeetingStatus)
	admin.HandleFunc("PUT /api/admin/operations/meetings/{id}/overrides/{occurrenceDate}", requireScope("cms:write", h.adminPutOverride))
	admin.HandleFunc("DELETE /api/admin/operations/meetings/{id}/overrides/{occurrenceDate}", requireScope("cms:write", h.adminDeleteOverride))
	admin.HandleFunc("PUT /api/admin/operations/meetings/{id}/collections", requireScope("cms:write", h.adminReplaceBindings))
}

func adminStatusRoutes(mux *http.ServeMux, prefix string, handler func(http.ResponseWriter, *http.Request, operations.Status)) {
	for action, status := range map[string]operations.Status{"pause": operations.StatusPaused, "resume": operations.StatusActive, "archive": operations.StatusArchived, "restore": operations.StatusActive} {
		status := status
		mux.HandleFunc("POST "+prefix+action, requireScope("cms:write", func(w http.ResponseWriter, r *http.Request) { handler(w, r, status) }))
	}
}

func (h *Handler) publicMeetings(w http.ResponseWriter, r *http.Request) {
	values, err := h.operations.ListMeetings(r.Context(), false)
	if err != nil {
		handleOperationsError(w, err)
		return
	}
	from := h.operationsNow().UTC()
	occurrences, err := h.operations.ListOccurrences(r.Context(), operations.OccurrenceQuery{From: from, To: from.AddDate(0, 0, 90), PublicOnly: true})
	if err != nil {
		handleOperationsError(w, err)
		return
	}
	next := map[string]publicOccurrence{}
	for _, value := range occurrences {
		if _, exists := next[value.MeetingKey]; !exists && value.Status == operations.OccurrenceScheduled {
			next[value.MeetingKey] = publicOccurrenceFrom(value)
		}
	}
	result := []publicMeeting{}
	for _, value := range values {
		if value.Status != operations.StatusActive || value.Visibility != operations.VisibilityPublic {
			continue
		}
		item := publicMeetingFrom(value)
		if occurrence, ok := next[value.Key]; ok {
			item.NextOccurrence = &occurrence
		}
		result = append(result, item)
	}
	writePublicOperations(w, r, result)
}

func (h *Handler) publicMeeting(w http.ResponseWriter, r *http.Request) {
	values, err := h.operations.ListMeetings(r.Context(), false)
	if err != nil {
		handleOperationsError(w, err)
		return
	}
	for _, value := range values {
		if value.Key == r.PathValue("meetingKey") && value.Status == operations.StatusActive && value.Visibility == operations.VisibilityPublic {
			writePublicOperations(w, r, publicMeetingFrom(value))
			return
		}
	}
	handleOperationsError(w, operations.ErrNotFound)
}

func (h *Handler) publicMeetingOccurrences(w http.ResponseWriter, r *http.Request) {
	from, to, ok := h.operationsRange(w, r, true)
	if !ok {
		return
	}
	values, err := h.operations.ListOccurrences(r.Context(), operations.OccurrenceQuery{From: from, To: to, PublicOnly: true})
	if err != nil {
		handleOperationsError(w, err)
		return
	}
	result := make([]publicOccurrence, len(values))
	for index, value := range values {
		result[index] = publicOccurrenceFrom(value)
	}
	writePublicOperations(w, r, result)
}

func (h *Handler) authenticatedMeetingSyncWindows(w http.ResponseWriter, r *http.Request) {
	from, to, ok := h.operationsRange(w, r, false)
	if !ok {
		return
	}
	values, err := h.operations.ListMediaSyncWindows(r.Context(), from, to)
	if err != nil {
		handleOperationsError(w, err)
		return
	}
	writeData(w, http.StatusOK, values, nil)
}

func (h *Handler) privateMeetingOccurrences(w http.ResponseWriter, r *http.Request) {
	from, to, ok := h.operationsRange(w, r, false)
	if !ok {
		return
	}
	values, err := h.operations.ListOccurrences(r.Context(), operations.OccurrenceQuery{From: from, To: to})
	if err != nil {
		handleOperationsError(w, err)
		return
	}
	writeData(w, http.StatusOK, values, nil)
}

func (h *Handler) privateMeetingSyncWindows(w http.ResponseWriter, r *http.Request) {
	h.authenticatedMeetingSyncWindows(w, r)
}

func (h *Handler) operationsRange(w http.ResponseWriter, r *http.Request, public bool) (time.Time, time.Time, bool) {
	from := h.operationsNow().UTC()
	to := from.AddDate(0, 0, 30)
	var err error
	if raw := strings.TrimSpace(r.URL.Query().Get("from")); raw != "" {
		from, err = time.Parse(time.RFC3339, raw)
	}
	if err == nil {
		if raw := strings.TrimSpace(r.URL.Query().Get("to")); raw != "" {
			to, err = time.Parse(time.RFC3339, raw)
		}
	}
	if err != nil || !from.Before(to) || to.Sub(from) > 90*24*time.Hour {
		if public {
			w.Header().Set("Cache-Control", "private, no-store")
		}
		handleOperationsError(w, operations.ErrInvalid)
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

func (h *Handler) adminListChurchUnits(w http.ResponseWriter, r *http.Request) {
	values, err := h.operations.ListChurchUnits(r.Context(), includeArchived(r))
	writeOperationsResult(w, values, err)
}

func (h *Handler) adminGetChurchUnit(w http.ResponseWriter, r *http.Request) {
	value, err := h.operations.GetChurchUnit(r.Context(), r.PathValue("id"))
	writeOperationsEntity(w, value, value.Version, err, http.StatusOK)
}

func (h *Handler) adminCreateChurchUnit(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		handleOperationsError(w, operations.ErrInvalid)
		return
	}
	var input operations.ChurchUnitInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.operations.CreateChurchUnit(r.Context(), input, actor(r), operationRequestID(r), r.Header.Get("Idempotency-Key"))
	writeOperationsEntity(w, value, value.Version, err, http.StatusCreated)
}

func (h *Handler) adminSaveChurchUnit(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input operations.ChurchUnitInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.operations.SaveChurchUnit(r.Context(), r.PathValue("id"), expected, input, actor(r), operationRequestID(r))
	writeOperationsEntity(w, value, value.Version, err, http.StatusOK)
}

func (h *Handler) adminChurchUnitStatus(w http.ResponseWriter, r *http.Request, status operations.Status) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	value, err := h.operations.SetChurchUnitStatus(r.Context(), r.PathValue("id"), expected, status, actor(r), operationRequestID(r))
	writeOperationsEntity(w, value, value.Version, err, http.StatusOK)
}

func (h *Handler) adminListResources(w http.ResponseWriter, r *http.Request) {
	values, err := h.operations.ListResources(r.Context(), includeArchived(r))
	writeOperationsResult(w, values, err)
}

func (h *Handler) adminGetResource(w http.ResponseWriter, r *http.Request) {
	value, err := h.operations.GetResource(r.Context(), r.PathValue("id"))
	writeOperationsEntity(w, value, value.Version, err, http.StatusOK)
}

func (h *Handler) adminCreateResource(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		handleOperationsError(w, operations.ErrInvalid)
		return
	}
	var input operations.ResourceInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.operations.CreateResource(r.Context(), input, actor(r), operationRequestID(r), r.Header.Get("Idempotency-Key"))
	writeOperationsEntity(w, value, value.Version, err, http.StatusCreated)
}

func (h *Handler) adminSaveResource(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input operations.ResourceInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.operations.SaveResource(r.Context(), r.PathValue("id"), expected, input, actor(r), operationRequestID(r))
	writeOperationsEntity(w, value, value.Version, err, http.StatusOK)
}

func (h *Handler) adminResourceStatus(w http.ResponseWriter, r *http.Request, status operations.Status) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	value, err := h.operations.SetResourceStatus(r.Context(), r.PathValue("id"), expected, status, actor(r), operationRequestID(r))
	writeOperationsEntity(w, value, value.Version, err, http.StatusOK)
}

func (h *Handler) adminListMeetings(w http.ResponseWriter, r *http.Request) {
	values, err := h.operations.ListMeetings(r.Context(), includeArchived(r))
	writeOperationsResult(w, values, err)
}

func (h *Handler) adminGetMeeting(w http.ResponseWriter, r *http.Request) {
	value, err := h.operations.GetMeeting(r.Context(), r.PathValue("id"))
	if err != nil {
		handleOperationsError(w, err)
		return
	}
	overrides, err := h.operations.ListOverrides(r.Context(), value.ID)
	if err != nil {
		handleOperationsError(w, err)
		return
	}
	bindings, err := h.operations.ListMeetingBindings(r.Context(), value.ID)
	writeOperationsEntity(w, meetingDetail{Meeting: value, Overrides: overrides, CollectionIDs: bindings}, value.Version, err, http.StatusOK)
}

func (h *Handler) adminCreateMeeting(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(r.Header.Get("Idempotency-Key")) == "" {
		handleOperationsError(w, operations.ErrInvalid)
		return
	}
	var input operations.MeetingInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.operations.CreateMeeting(r.Context(), input, actor(r), operationRequestID(r), r.Header.Get("Idempotency-Key"))
	writeOperationsEntity(w, value, value.Version, err, http.StatusCreated)
}

func (h *Handler) adminSaveMeeting(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input operations.MeetingInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.operations.SaveMeeting(r.Context(), r.PathValue("id"), expected, input, actor(r), operationRequestID(r))
	writeOperationsEntity(w, value, value.Version, err, http.StatusOK)
}

func (h *Handler) adminMeetingStatus(w http.ResponseWriter, r *http.Request, status operations.Status) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	value, err := h.operations.SetMeetingStatus(r.Context(), r.PathValue("id"), expected, status, actor(r), operationRequestID(r))
	writeOperationsEntity(w, value, value.Version, err, http.StatusOK)
}

func (h *Handler) adminPutOverride(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input operations.OccurrenceOverrideInput
	if !decode(w, r, &input) {
		return
	}
	input.OccurrenceDate = r.PathValue("occurrenceDate")
	value, err := h.operations.PutOverride(r.Context(), r.PathValue("id"), expected, input, actor(r), operationRequestID(r))
	writeOperationsEntity(w, value, value.Version, err, http.StatusOK)
}

func (h *Handler) adminDeleteOverride(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	value, err := h.operations.DeleteOverride(r.Context(), r.PathValue("id"), expected, r.PathValue("occurrenceDate"), actor(r), operationRequestID(r))
	writeOperationsEntity(w, value, value.Version, err, http.StatusOK)
}

func (h *Handler) adminReplaceBindings(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input struct {
		CollectionIDs []string `json:"collectionIds"`
	}
	if !decode(w, r, &input) {
		return
	}
	value, err := h.operations.ReplaceMeetingBindings(r.Context(), r.PathValue("id"), expected, input.CollectionIDs, actor(r), operationRequestID(r))
	writeOperationsEntity(w, value, value.Version, err, http.StatusOK)
}

func publicMeetingFrom(value operations.Meeting) publicMeeting {
	return publicMeeting{Key: value.Key, Name: value.Name, Description: value.Description, Timezone: value.Timezone, Schedule: value.Schedule, DurationMinutes: value.DurationMinutes}
}

func publicOccurrenceFrom(value operations.Occurrence) publicOccurrence {
	return publicOccurrence{OccurrenceID: value.ID, MeetingKey: value.MeetingKey, StartsAt: value.StartsAt, EndsAt: value.EndsAt, Status: value.Status}
}

func writePublicOperations(w http.ResponseWriter, r *http.Request, value any) {
	payload, _ := json.Marshal(value)
	etag := fmt.Sprintf(`"%x"`, sha256.Sum256(payload))
	w.Header().Set("Cache-Control", "public, max-age=30")
	w.Header().Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeData(w, http.StatusOK, value, nil)
}

func writeOperationsResult(w http.ResponseWriter, value any, err error) {
	if err != nil {
		handleOperationsError(w, err)
		return
	}
	writeData(w, http.StatusOK, value, nil)
}

func writeOperationsEntity(w http.ResponseWriter, value any, version int64, err error, status int) {
	if err != nil {
		handleOperationsError(w, err)
		return
	}
	if version > 0 {
		w.Header().Set("ETag", fmt.Sprintf(`"%d"`, version))
	}
	writeData(w, status, value, nil)
}

func handleOperationsError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, operations.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", "The operations request is invalid.")
	case errors.Is(err, operations.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "The operations record was not found.")
	case errors.Is(err, operations.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "The operations request conflicts with existing data.")
	case errors.Is(err, operations.ErrPrecondition):
		writeError(w, http.StatusPreconditionFailed, "version_mismatch", "The operations record changed. Reload and retry.")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "The operations request failed.")
	}
}

func operationRequestID(r *http.Request) string {
	if principal, ok := r.Context().Value(principalKey{}).(principal); ok && strings.TrimSpace(principal.RequestID) != "" {
		return principal.RequestID
	}
	return r.Header.Get("X-HHC-Request-ID")
}

func includeArchived(r *http.Request) bool { return r.URL.Query().Get("includeArchived") == "true" }
