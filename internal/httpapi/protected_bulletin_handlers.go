package httpapi

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
)

type protectedBulletinRequest struct{ series, locale string }
type protectedBulletinKey struct{}

func (h *Handler) requireBulletinEntitlement(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		request := protectedBulletinRequest{series: strings.TrimSpace(r.URL.Query().Get("series")), locale: strings.TrimSpace(r.URL.Query().Get("locale"))}
		if request.series == "" {
			request.series = "general"
		}
		if pathLocale := strings.TrimSpace(r.PathValue("locale")); pathLocale != "" {
			request.locale = pathLocale
		}
		if request.locale == "" {
			request.locale = "zh-Hant"
		}
		if h.service == nil || h.bulletinEntitlements == nil {
			writeError(w, http.StatusServiceUnavailable, "authorization_unavailable", "Bulletin authorization is unavailable.")
			return
		}
		code, err := h.service.BulletinEntitlement(r.Context(), request.series, request.locale)
		if errors.Is(err, bulletins.ErrNotFound) || errors.Is(err, bulletins.ErrInvalid) {
			bulletinNotFound(w, r)
			return
		}
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "authorization_unavailable", "Bulletin authorization is unavailable.")
			return
		}
		principal, ok := r.Context().Value(principalKey{}).(principal)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Trusted gateway identity is required.")
			return
		}
		decisions, err := h.bulletinEntitlements.CheckEntitlements(r.Context(), principal.UserID, []string{code})
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "authorization_unavailable", "Bulletin authorization is unavailable.")
			return
		}
		if len(decisions) != 1 || decisions[0].EntitlementCode != code || decisions[0].Decision != "allow" {
			bulletinNotFound(w, r)
			return
		}
		if !h.requireCurrentMemberLegal(w, r) {
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), protectedBulletinKey{}, request)))
	}
}

func protectedBulletinFrom(r *http.Request) protectedBulletinRequest {
	value, _ := r.Context().Value(protectedBulletinKey{}).(protectedBulletinRequest)
	return value
}

func (h *Handler) listProtectedBulletins(w http.ResponseWriter, r *http.Request) {
	request := protectedBulletinFrom(r)
	page, size := pagination(r)
	var issueNumber *int
	if raw := strings.TrimSpace(r.URL.Query().Get("issueNumber")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || parsed < 1 || strconv.FormatInt(parsed, 10) != raw {
			writeError(w, http.StatusBadRequest, "invalid_request", "The request is invalid.")
			return
		}
		value := int(parsed)
		issueNumber = &value
	}
	value, err := h.service.ListProtected(r.Context(), request.series, request.locale, page, size, issueNumber)
	if err != nil {
		handleProtectedBulletinError(w, err)
		return
	}
	for _, item := range value.Items {
		if !h.protectedAssetReady(r, item) {
			writeError(w, http.StatusNotFound, "not_found", "The bulletin was not found.")
			return
		}
	}
	writeData(w, http.StatusOK, value.Items, map[string]any{"page": value.Page, "pageSize": value.PageSize, "total": value.Total})
}

func (h *Handler) latestProtectedBulletin(w http.ResponseWriter, r *http.Request) {
	request := protectedBulletinFrom(r)
	value, err := h.service.GetProtectedLatest(r.Context(), request.series, request.locale)
	if err != nil {
		handleProtectedBulletinError(w, err)
		return
	}
	h.writeProtectedBulletin(w, r, value)
}

func (h *Handler) getProtectedBulletin(w http.ResponseWriter, r *http.Request) {
	value, ok := h.protectedBulletin(w, r)
	if ok {
		h.writeProtectedBulletin(w, r, value)
	}
}

func (h *Handler) protectedBulletin(w http.ResponseWriter, r *http.Request) (bulletins.ProtectedBulletin, bool) {
	request := protectedBulletinFrom(r)
	value, err := h.service.GetProtectedVersion(r.Context(), r.PathValue("issueID"), request.series, request.locale)
	if err != nil {
		handleProtectedBulletinError(w, err)
		return value, false
	}
	return value, true
}

func (h *Handler) writeProtectedBulletin(w http.ResponseWriter, r *http.Request, value bulletins.ProtectedBulletin) {
	if !h.protectedAssetReady(r, value) {
		writeError(w, http.StatusNotFound, "not_found", "The bulletin was not found.")
		return
	}
	w.Header().Set("ETag", `"bulletin-`+strconv.FormatInt(value.Version, 10)+`"`)
	writeData(w, http.StatusOK, value, nil)
}

func (h *Handler) protectedAssetReady(r *http.Request, value bulletins.ProtectedBulletin) bool {
	if h.uploads == nil {
		return false
	}
	asset, err := h.uploads.Get(r.Context(), value.ProtectedAssetID)
	return err == nil && protectedAssetMatches(asset, value)
}

func protectedAssetMatches(asset assetclient.Asset, value bulletins.ProtectedBulletin) bool {
	return asset.ID == value.ProtectedAssetID && asset.Namespace == "cms.weekly.pdf" && asset.OwnerService == "hhc-web-api" && asset.OwnerType == "bulletin_issue" && asset.OwnerID == value.IssueID && asset.Locale == value.Locale && asset.UploadStatus == "completed" && asset.ScanStatus == "clean" && (asset.ProcessingStatus == "ready" || asset.ProcessingStatus == "not_required") && asset.Visibility == "private"
}

func (h *Handler) downloadProtectedBulletin(w http.ResponseWriter, r *http.Request) {
	value, ok := h.protectedBulletin(w, r)
	if !ok {
		return
	}
	if !h.protectedAssetReady(r, value) || h.downloads == nil {
		writeError(w, http.StatusNotFound, "not_found", "The bulletin was not found.")
		return
	}
	response, err := h.downloads.AuthorizedDownload(r.Context(), http.MethodGet, value.ProtectedAssetID, r.Header.Get("Range"), r.Header.Get("If-Range"))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "bulletin_unavailable", "The bulletin is unavailable.")
		return
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusPartialContent && response.StatusCode != http.StatusRequestedRangeNotSatisfiable {
		writeError(w, http.StatusServiceUnavailable, "bulletin_unavailable", "The bulletin is unavailable.")
		return
	}
	for _, header := range []string{"Accept-Ranges", "Content-Length", "Content-Range", "Content-Type", "ETag", "Last-Modified"} {
		if value := response.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	name := filepath.Base(value.DownloadName)
	if name == "." || name == "" {
		name = "bulletin.pdf"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func handleProtectedBulletinError(w http.ResponseWriter, err error) {
	if errors.Is(err, bulletins.ErrNotFound) || errors.Is(err, bulletins.ErrInvalid) {
		writeError(w, http.StatusNotFound, "not_found", "The bulletin was not found.")
		return
	}
	if errors.Is(err, assetclient.ErrUnavailable) {
		writeError(w, http.StatusServiceUnavailable, "bulletin_unavailable", "The bulletin is unavailable.")
		return
	}
	writeError(w, http.StatusServiceUnavailable, "bulletin_unavailable", "The bulletin is unavailable.")
}

func (h *Handler) bulletinNotificationPolicy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if h.service == nil {
		writeError(w, http.StatusServiceUnavailable, "bulletin_unavailable", "The bulletin is unavailable.")
		return
	}
	issue, err := h.service.GetIssue(r.Context(), r.PathValue("issueID"))
	if err != nil {
		handleProtectedBulletinError(w, err)
		return
	}
	type entitlement struct {
		Locale          string `json:"locale"`
		EntitlementCode string `json:"entitlementCode"`
	}
	published := make(map[string]bool, len(issue.Versions))
	for _, version := range issue.Versions {
		published[version.Locale] = version.Status == "published"
	}
	entitlements := make([]entitlement, 0, len(published))
	if issue.Status == "published" {
		for _, edition := range bulletins.BulletinEditionOrder {
			if edition.Series != bulletins.BulletinSeriesGeneral {
				continue
			}
			locale := string(edition.Locale)
			if !published[locale] {
				continue
			}
			code, err := h.service.BulletinEntitlement(r.Context(), "general", locale)
			if err != nil {
				writeError(w, http.StatusServiceUnavailable, "bulletin_unavailable", "The bulletin is unavailable.")
				return
			}
			entitlements = append(entitlements, entitlement{Locale: locale, EntitlementCode: code})
		}
	}
	writeData(w, http.StatusOK, map[string]any{"published": len(entitlements) > 0, "entitlements": entitlements}, nil)
}
