package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/accountclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/google/uuid"
)

type investigationRepository interface {
	ListWatermarkVersions(context.Context, string, time.Time) ([]bulletinwatermark.CandidateVersion, error)
	CreateWatermarkInvestigation(context.Context, bulletinwatermark.Investigation) error
	GetWatermarkInvestigation(context.Context, string, string, time.Time) (bulletinwatermark.InvestigationView, error)
}

type investigationHistoryRepository interface {
	ListWatermarkInvestigations(context.Context, string, string, string, int, time.Time) (bulletinwatermark.InvestigationHistoryPage, error)
}

type investigationGlobalHistoryRepository interface {
	ListGlobalWatermarkInvestigations(context.Context, string, string, int, string, string, time.Time) (bulletinwatermark.InvestigationHistoryPage, error)
}

type investigationIdentityResolver interface {
	ResolveIdentities(context.Context, []string) ([]accountclient.Identity, error)
}

func (h *Handler) authorizeInvestigation(w http.ResponseWriter, r *http.Request) (investigationRepository, string, bool) {
	w.Header().Set("Cache-Control", "private, no-store")
	repo, ok := h.watermarkRepo.(investigationRepository)
	if !ok {
		writeError(w, 503, "watermark_investigation_unavailable", "Document investigation is unavailable.")
		return nil, "", false
	}
	p, _ := r.Context().Value(principalKey{}).(principal)
	actor, err := uuid.Parse(p.UserID)
	if err != nil {
		writeError(w, 403, "permission_denied", "Permission denied.")
		return nil, "", false
	}
	return repo, actor.String(), true
}

func investigationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, bulletinwatermark.ErrInvalidInvestigation), errors.Is(err, bulletinwatermark.ErrInvalidFingerprint):
		writeError(w, 422, "invalid_request", "Investigation metadata is invalid.")
	case errors.Is(err, bulletinwatermark.ErrInvestigationConflict):
		writeError(w, 409, "watermark_investigation_conflict", "The request conflicts with an existing investigation.")
	case errors.Is(err, bulletinwatermark.ErrNotFound):
		writeError(w, 404, "watermark_investigation_not_found", "Investigation not found.")
	case errors.Is(err, bulletinwatermark.ErrRestricted):
		writeError(w, 403, "bulletin_processing_restricted", "Document processing is restricted.")
	case errors.Is(err, bulletinwatermark.ErrRateLimited):
		w.Header().Set("Retry-After", "60")
		writeError(w, 429, "watermark_rate_limited", "Too many investigations. Please retry later.")
	default:
		writeError(w, 503, "watermark_investigation_unavailable", "Document investigation is unavailable.")
	}
}

func (h *Handler) listBulletinWatermarkVersions(w http.ResponseWriter, r *http.Request) {
	repo, _, ok := h.authorizeInvestigation(w, r)
	if !ok {
		return
	}
	issue, err := uuid.Parse(r.PathValue("issueId"))
	if err != nil {
		writeError(w, 422, "invalid_request", "A valid issue ID is required.")
		return
	}
	versions, err := repo.ListWatermarkVersions(r.Context(), issue.String(), time.Now().UTC())
	if err != nil {
		investigationError(w, err)
		return
	}
	if versions == nil {
		versions = []bulletinwatermark.CandidateVersion{}
	}
	locales := []string{}
	seen := map[string]bool{}
	for _, version := range versions {
		if !seen[version.Locale] {
			seen[version.Locale] = true
			locales = append(locales, version.Locale)
		}
	}
	writeData(w, 200, map[string]any{"locales": locales, "versions": versions}, nil)
}

func (h *Handler) createBulletinWatermarkInvestigation(w http.ResponseWriter, r *http.Request) {
	repo, actor, ok := h.authorizeInvestigation(w, r)
	if !ok {
		return
	}
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	id, err := uuid.Parse(key)
	if len(r.Header.Values("Idempotency-Key")) != 1 || len(key) != 36 || err != nil {
		writeError(w, 422, "invalid_request", "A UUID Idempotency-Key is required.")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, (10<<20)+(32<<10))
	reader, err := r.MultipartReader()
	if err != nil {
		writeError(w, 422, "invalid_request", "Multipart metadata and files are required.")
		return
	}
	var metadata []byte
	var files []bulletinwatermark.EvidenceFile
	seen := map[string]bool{}
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			writeError(w, 413, "invalid_upload", "The upload is invalid or too large.")
			return
		}
		name := part.FormName()
		limit := int64(10 << 20)
		if name == "metadata" {
			limit = 8 << 10
		} else if name != "files" {
			part.Close()
			writeError(w, 422, "invalid_request", "Only metadata and files parts are accepted.")
			return
		}
		if name == "metadata" && seen[name] {
			part.Close()
			writeError(w, 422, "invalid_request", "Duplicate multipart fields are not accepted.")
			return
		}
		seen[name] = true
		value, err := io.ReadAll(io.LimitReader(part, limit+1))
		part.Close()
		if err != nil || int64(len(value)) > limit {
			writeError(w, 413, "invalid_upload", "The upload is invalid or too large.")
			return
		}
		if name == "metadata" {
			metadata = value
		} else {
			files = append(files, bulletinwatermark.EvidenceFile{Data: value})
		}
	}
	if len(metadata) == 0 || len(files) == 0 {
		writeError(w, 422, "invalid_request", "Metadata and files are required.")
		return
	}
	var input struct {
		IssueID string `json:"issueId"`
		Series  string `json:"series"`
		Locale  string `json:"locale"`
	}
	decoder := json.NewDecoder(bytes.NewReader(metadata))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&input); err != nil {
		writeError(w, 422, "invalid_request", "Investigation metadata is invalid.")
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeError(w, 422, "invalid_request", "Investigation metadata is invalid.")
		return
	}
	issue, issueErr := uuid.Parse(input.IssueID)
	series := bulletinwatermark.NormalizeSeries(strings.TrimSpace(input.Series))
	if issueErr != nil || series != "general" && series != "children" || input.Locale != "" && !bulletinwatermark.ValidEdition(series, input.Locale) {
		writeError(w, 422, "invalid_request", "A valid issue, series, and optional locale are required.")
		return
	}
	evidence, err := bulletinwatermark.PackEvidence(files)
	if err != nil {
		writeError(w, 422, "invalid_upload", "Upload PNG, JPEG, or PDF files.")
		return
	}
	normalized, _ := bulletinwatermark.UnpackEvidence(evidence)
	for _, file := range normalized {
		if file.Kind == bulletinwatermark.EvidencePDF {
			continue
		}
		config, format, decodeErr := image.DecodeConfig(bytes.NewReader(file.Data))
		if decodeErr != nil || (format != "png" && format != "jpeg") || config.Width < 1 || config.Height < 1 || int64(config.Width)*int64(config.Height) > bulletinwatermark.MaxImagePixels {
			writeError(w, 422, "invalid_image", "A valid PNG or JPEG up to 20 million pixels is required.")
			return
		}
		if _, _, decodeErr = image.Decode(bytes.NewReader(file.Data)); decodeErr != nil {
			writeError(w, 422, "invalid_image", "An image could not be decoded.")
			return
		}
	}
	versions, err := repo.ListWatermarkVersions(r.Context(), issue.String(), time.Now().UTC())
	if err != nil {
		investigationError(w, err)
		return
	}
	scopes := make([]bulletinwatermark.CandidateScope, 0, len(versions))
	for _, version := range versions {
		if bulletinwatermark.NormalizeSeries(version.Series) == series && (input.Locale == "" || version.Locale == input.Locale) {
			scopes = append(scopes, version.CandidateScope)
		}
	}
	if len(scopes) == 0 {
		writeError(w, 422, "invalid_request", "The selected issue has no eligible issuance.")
		return
	}
	job := bulletinwatermark.Investigation{ID: id.String(), ActorID: actor, Scope: scopes[0], Scopes: scopes, Evidence: evidence, CreatedAt: time.Now().UTC()}
	if err = repo.CreateWatermarkInvestigation(r.Context(), job); err != nil {
		investigationError(w, err)
		return
	}
	w.Header().Set("Location", "/api/admin/bulletin-watermark-investigations/"+job.ID)
	writeData(w, 202, map[string]string{"id": job.ID}, nil)
}

func (h *Handler) getBulletinWatermarkInvestigation(w http.ResponseWriter, r *http.Request) {
	repo, actor, ok := h.authorizeInvestigation(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		investigationError(w, bulletinwatermark.ErrNotFound)
		return
	}
	view, err := repo.GetWatermarkInvestigation(r.Context(), id.String(), actor, time.Now().UTC())
	if err != nil {
		investigationError(w, err)
		return
	}
	data := map[string]any{"id": view.ID, "scope": view.Scope, "status": view.Status, "createdAt": view.CreatedAt, "expiresAt": view.ExpiresAt}
	if view.Result != "" {
		data["result"] = view.Result
	}
	if view.ErrorCode != "" {
		data["errorCode"] = view.ErrorCode
		data["reasonCode"] = view.ErrorCode
	}
	if view.MatchMethod != "" {
		data["matchMethod"] = view.MatchMethod
	}
	if view.Receipt != nil && view.Status == "completed" && view.Result == "matched" {
		v := view.Receipt
		data["receipt"] = map[string]any{"receiptId": v.ID, "userId": v.UserID, "issueId": v.IssueID, "locale": v.Locale, "revision": v.Revision, "issuedAt": v.IssuedAt, "rendererVersion": v.RendererVersion}
	}
	writeData(w, 200, data, nil)
}

func (h *Handler) listBulletinWatermarkInvestigations(w http.ResponseWriter, r *http.Request) {
	repo, actor, ok := h.authorizeInvestigation(w, r)
	if !ok {
		return
	}
	issue, err := uuid.Parse(r.PathValue("issueId"))
	if err != nil {
		investigationError(w, bulletinwatermark.ErrInvalidInvestigation)
		return
	}
	limit, ok := investigationHistoryLimit(w, r)
	if !ok {
		return
	}
	history, ok := repo.(investigationHistoryRepository)
	if !ok {
		investigationError(w, bulletinwatermark.ErrUnavailable)
		return
	}
	page, err := history.ListWatermarkInvestigations(r.Context(), issue.String(), actor, r.URL.Query().Get("cursor"), limit, time.Now().UTC())
	if err != nil {
		investigationError(w, err)
		return
	}
	h.writeInvestigationHistory(w, r, page, false)
}

func (h *Handler) listGlobalBulletinWatermarkInvestigations(w http.ResponseWriter, r *http.Request) {
	repo, actor, ok := h.authorizeInvestigation(w, r)
	if !ok {
		return
	}
	limit, ok := investigationHistoryLimit(w, r)
	if !ok {
		return
	}
	history, ok := repo.(investigationGlobalHistoryRepository)
	if !ok {
		investigationError(w, bulletinwatermark.ErrUnavailable)
		return
	}
	sortKey, direction := r.URL.Query().Get("sort"), r.URL.Query().Get("direction")
	if sortKey == "" && direction == "" {
		sortKey, direction = "submitted", "desc"
	}
	if !slices.Contains([]string{"submitted", "requested", "actual", "status", "operator", "account", "issued"}, sortKey) || (direction != "asc" && direction != "desc") {
		writeError(w, 422, "invalid_request", "Invalid sort order.")
		return
	}
	page, err := history.ListGlobalWatermarkInvestigations(r.Context(), actor, r.URL.Query().Get("cursor"), limit, sortKey, direction, time.Now().UTC())
	if err != nil {
		investigationError(w, err)
		return
	}
	h.writeInvestigationHistory(w, r, page, true)
}

func investigationHistoryLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, 422, "invalid_request", "Limit must be between 1 and 100.")
			return 0, false
		}
		limit = parsed
	}
	return limit, true
}

func (h *Handler) writeInvestigationHistory(w http.ResponseWriter, r *http.Request, page bulletinwatermark.InvestigationHistoryPage, global bool) {
	identities := map[string]accountclient.Identity{}
	if global {
		for _, row := range page.Items {
			if row.ActorAvailable {
				identities[row.ActorID] = accountclient.Identity{ID: row.ActorID, DisplayName: row.ActorDisplayName, Email: row.ActorEmail}
			}
			if row.MatchedAvailable {
				identities[row.MatchedAccountID] = accountclient.Identity{ID: row.MatchedAccountID, DisplayName: row.MatchedDisplayName, Email: row.MatchedEmail}
			}
		}
	} else {
		ids, seen := []string{}, map[string]bool{}
		for _, row := range page.Items {
			for _, id := range []string{row.ActorID, row.MatchedAccountID} {
				if id != "" && !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
		resolver := h.watermarkIdentities
		if resolver == nil {
			investigationError(w, bulletinwatermark.ErrUnavailable)
			return
		}
		resolved, err := resolver.ResolveIdentities(r.Context(), ids)
		if err != nil {
			investigationError(w, err)
			return
		}
		for _, identity := range resolved {
			identities[identity.ID] = identity
		}
	}
	p, _ := r.Context().Value(principalKey{}).(principal)
	items := make([]map[string]any, 0, len(page.Items))
	for _, row := range page.Items {
		item := map[string]any{"id": row.ID, "status": row.Status, "createdAt": row.CreatedAt, "submittedBy": identityView(row.ActorID, identities)}
		if global || row.IssueDate != "" {
			requested := map[string]any{"issueId": row.IssueID, "issueDate": row.IssueDate}
			if row.IssueNumber > 0 {
				requested["issueNumber"] = row.IssueNumber
			}
			item["requestedIssue"] = requested
		}
		item["resolvedLocale"], item["resolvedRevision"] = row.Locale, row.Revision
		if !global {
			item["locale"], item["revision"] = row.Locale, row.Revision
		}
		if row.ActualIssueID != "" && row.ActualIssueDate != "" {
			actual := map[string]any{"issueId": row.ActualIssueID, "issueDate": row.ActualIssueDate}
			if row.ActualIssueNumber > 0 {
				actual["issueNumber"] = row.ActualIssueNumber
			}
			item["actualIssue"] = actual
		}
		if row.Result != "" {
			item["result"] = row.Result
		}
		if row.ErrorCode != "" {
			if !global {
				item["errorCode"] = row.ErrorCode
			}
			item["reasonCode"] = row.ErrorCode
		}
		if row.MatchMethod != "" {
			item["matchMethod"] = row.MatchMethod
		}
		if row.MatchedAccountID != "" {
			matched := identityView(row.MatchedAccountID, identities)
			matched["canNavigate"] = p.Scopes["users:read"] || p.Scopes["*"]
			item["matchedAccount"] = matched
			item["issuedAt"] = row.IssuedAt
		}
		items = append(items, item)
	}
	meta := map[string]any{}
	if page.NextCursor != "" {
		meta["nextCursor"] = page.NextCursor
	}
	writeData(w, 200, map[string]any{"items": items}, meta)
}

func identityView(id string, identities map[string]accountclient.Identity) map[string]any {
	value, ok := identities[id]
	if !ok {
		return map[string]any{"id": id, "available": false}
	}
	return map[string]any{"id": id, "displayName": value.DisplayName, "email": value.Email, "available": true}
}
