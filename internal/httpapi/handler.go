package httpapi

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
)

type Handler struct {
	service *bulletins.Service
	db      *sql.DB
}

func New(service *bulletins.Service, db *sql.DB) *Handler { return &Handler{service: service, db: db} }
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeData(w, http.StatusOK, map[string]string{"status": "healthy"}, nil)
	})
	mux.HandleFunc("GET /ready", h.ready)
	mux.HandleFunc("GET /api/bulletins/latest", h.publicLatest)
	mux.HandleFunc("GET /api/bulletins/{issueDate}", h.publicByDate)
	mux.HandleFunc("GET /api/bulletins", h.publicList)
	admin := http.NewServeMux()
	admin.HandleFunc("GET /api/admin/bulletins", requireScope("cms:read", h.adminList))
	admin.HandleFunc("POST /api/admin/bulletins", requireScope("cms:write", h.adminCreate))
	admin.HandleFunc("GET /api/admin/bulletins/{issueID}", requireScope("cms:read", h.adminGet))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/versions", requireScope("cms:write", h.adminPutVersion))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/publish", requireScope("cms:publish", h.adminPublish))
	admin.HandleFunc("POST /api/admin/bulletins/{issueID}/unpublish", requireScope("cms:publish", h.adminUnpublish))
	mux.Handle("/api/admin/", requireTrusted(admin))
	return requestID(mux)
}
func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := h.db.PingContext(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "Database is unavailable.")
		return
	}
	writeData(w, http.StatusOK, map[string]string{"status": "ready"}, nil)
}
func (h *Handler) publicLatest(w http.ResponseWriter, r *http.Request) {
	value, err := h.service.GetPublicLatest(r.Context(), locale(r))
	if err != nil {
		handleError(w, err)
		return
	}
	publicResponse(w, value)
}
func (h *Handler) publicByDate(w http.ResponseWriter, r *http.Request) {
	value, err := h.service.GetPublicByDate(r.Context(), r.PathValue("issueDate"), locale(r))
	if err != nil {
		handleError(w, err)
		return
	}
	publicResponse(w, value)
}
func (h *Handler) publicList(w http.ResponseWriter, r *http.Request) {
	page, size := pagination(r)
	value, err := h.service.ListPublic(r.Context(), locale(r), page, size)
	if err != nil {
		handleError(w, err)
		return
	}
	writeData(w, http.StatusOK, value.Items, map[string]any{"page": value.Page, "pageSize": value.PageSize, "total": value.Total})
}
func (h *Handler) adminList(w http.ResponseWriter, r *http.Request) {
	page, size := pagination(r)
	value, err := h.service.ListIssues(r.Context(), page, size, r.URL.Query().Get("status"))
	if err != nil {
		handleError(w, err)
		return
	}
	writeData(w, http.StatusOK, value.Items, map[string]any{"page": value.Page, "pageSize": value.PageSize, "total": value.Total})
}
func (h *Handler) adminGet(w http.ResponseWriter, r *http.Request) {
	value, err := h.service.GetIssue(r.Context(), r.PathValue("issueID"))
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}
func (h *Handler) adminCreate(w http.ResponseWriter, r *http.Request) {
	var input bulletins.CreateIssueInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.CreateIssue(r.Context(), input, actor(r), r.Header.Get("Idempotency-Key"))
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusCreated, value, nil)
}
func (h *Handler) adminPutVersion(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input bulletins.PutVersionInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.PutVersion(r.Context(), r.PathValue("issueID"), expected, input, actor(r))
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}
func (h *Handler) adminPublish(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input bulletins.PublishInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.Publish(r.Context(), r.PathValue("issueID"), input.Locale, expected, actor(r))
	if err != nil {
		handleError(w, err)
		return
	}
	writeData(w, http.StatusAccepted, value, nil)
}
func (h *Handler) adminUnpublish(w http.ResponseWriter, r *http.Request) {
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input bulletins.PublishInput
	if !decode(w, r, &input) {
		return
	}
	value, err := h.service.Unpublish(r.Context(), r.PathValue("issueID"), input.Locale, expected, actor(r))
	if err != nil {
		handleError(w, err)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}
func publicResponse(w http.ResponseWriter, value bulletins.PublicBulletin) {
	w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=300")
	w.Header().Set("ETag", fmt.Sprintf(`"bulletin-%d"`, value.Version))
	writeData(w, http.StatusOK, value, nil)
}
func locale(r *http.Request) string {
	value := r.URL.Query().Get("locale")
	if value == "" {
		value = "zh-Hant"
	}
	return value
}
func pagination(r *http.Request) (int, int) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	size, _ := strconv.Atoi(r.URL.Query().Get("pageSize"))
	return page, size
}
func ifMatch(w http.ResponseWriter, r *http.Request) (int64, bool) {
	raw := strings.Trim(strings.TrimSpace(r.Header.Get("If-Match")), `"`)
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		writeError(w, http.StatusPreconditionRequired, "precondition_required", "A valid If-Match version is required.")
		return 0, false
	}
	return value, true
}
func decode(w http.ResponseWriter, r *http.Request, destination any) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return false
	}
	return true
}
func handleError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, bulletins.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", "The request is invalid.")
	case errors.Is(err, bulletins.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "The bulletin was not found.")
	case errors.Is(err, bulletins.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "The request conflicts with an existing resource.")
	case errors.Is(err, bulletins.ErrPrecondition):
		writeError(w, http.StatusPreconditionFailed, "version_conflict", "The resource changed. Reload and try again.")
	case errors.Is(err, bulletins.ErrNotPublishable):
		writeError(w, http.StatusUnprocessableEntity, "not_publishable", "The bulletin is not ready for this operation.")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "The service could not complete the request.")
	}
}

type envelope struct {
	Data  any `json:"data"`
	Meta  any `json:"meta"`
	Error any `json:"error"`
}

func writeData(w http.ResponseWriter, status int, data, meta any) {
	if meta == nil {
		meta = map[string]any{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{Data: data, Meta: meta, Error: nil})
}
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{Data: nil, Meta: map[string]any{}, Error: map[string]string{"code": code, "message": message}})
}
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-HHC-Request-ID"))
		if id == "" {
			id = strconv.FormatInt(time.Now().UnixNano(), 36)
		}
		w.Header().Set("X-HHC-Request-ID", id)
		next.ServeHTTP(w, r)
	})
}
