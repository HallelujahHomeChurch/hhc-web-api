package httpapi

import (
	"context"
	"errors"
	"fmt"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/legal"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/operationsclient"
	"net/http"
	"strconv"
)

type memberQualifier interface {
	CheckMemberQualification(context.Context, string) (operationsclient.MembershipQualification, error)
}

func (h *Handler) WithLegal(service *legal.Service, qualification memberQualifier) *Handler {
	h.legal = service
	h.memberQualification = qualification
	return h
}
func (h *Handler) legalRoutes(public, admin, member, private *http.ServeMux) {
	public.HandleFunc("GET /api/legal/common", func(w http.ResponseWriter, r *http.Request) { h.writeCurrentLegal(w, r, legal.Common) })
	member.HandleFunc("GET /api/member/legal/current", h.memberCurrentLegal)
	admin.HandleFunc("GET /api/admin/legal/{scope}", requireScope("cms:pages:read", h.adminLegalDraft))
	admin.HandleFunc("PUT /api/admin/legal/{scope}", requireScope("cms:pages:write", h.adminAuditProvenance(h.adminLegalSave)))
	admin.HandleFunc("POST /api/admin/legal/{scope}/publish", requireScope("cms:pages:publish", h.adminAuditProvenance(h.adminLegalPublish)))
	private.Handle("POST /priv/legal/snapshots/resolve", h.requirePrivateService(http.MethodPost, map[string]bool{"account-api": true}, http.HandlerFunc(h.resolveLegalSnapshot)))
	private.Handle("POST /priv/legal/current", h.requirePrivateService(http.MethodPost, map[string]bool{"account-api": true, "operations-api": true}, http.HandlerFunc(h.privateCurrentLegal)))
}
func legalError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, legal.ErrInvalid):
		writeError(w, 400, "invalid_request", "The request is invalid.")
	case errors.Is(err, legal.ErrNotFound):
		writeError(w, 404, "not_found", "The content is unavailable.")
	case errors.Is(err, legal.ErrConflict):
		writeError(w, 409, "legal_version_changed", "The content changed. Reload and try again.")
	default:
		writeError(w, 503, "service_unavailable", "The service is unavailable.")
	}
}
func (h *Handler) writeCurrentLegal(w http.ResponseWriter, r *http.Request, scope legal.Scope) {
	if h.legal == nil {
		legalError(w, legal.ErrNotFound)
		return
	}
	value, err := h.legal.Current(r.Context(), scope, locale(r))
	if err != nil {
		legalError(w, err)
		return
	}
	writeData(w, 200, value, nil)
}
func (h *Handler) memberCurrentLegal(w http.ResponseWriter, r *http.Request) {
	if h.memberQualification == nil {
		writeError(w, 503, "service_unavailable", "The service is unavailable.")
		return
	}
	q, err := h.memberQualification.CheckMemberQualification(r.Context(), actor(r))
	if err != nil {
		writeError(w, 503, "service_unavailable", "The service is unavailable.")
		return
	}
	if q.SubjectID != actor(r) || !q.Qualified {
		writeError(w, 403, "access_required", "Access is required.")
		return
	}
	h.writeCurrentLegal(w, r, legal.VerifiedMember)
}
func (h *Handler) adminLegalDraft(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if h.legal == nil {
		legalError(w, legal.ErrNotFound)
		return
	}
	v, e := h.legal.Draft(r.Context(), legal.Scope(r.PathValue("scope")))
	if e != nil {
		legalError(w, e)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, v.Version))
	writeData(w, 200, v, nil)
}
func (h *Handler) adminLegalSave(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if h.legal == nil {
		legalError(w, legal.ErrNotFound)
		return
	}
	raw := r.Header.Get("If-Match")
	expected, err := strconv.ParseInt(trimLegalETag(raw), 10, 64)
	if err != nil || expected < 0 {
		writeError(w, 428, "precondition_required", "If-Match is required; use 0 for the initial draft.")
		return
	}
	var d legal.Draft
	if !decode(w, r, &d) {
		return
	}
	scope := legal.Scope(r.PathValue("scope"))
	if d.Scope != scope {
		legalError(w, legal.ErrInvalid)
		return
	}
	v, e := h.legal.SaveDraft(r.Context(), d, expected, actor(r))
	if e != nil {
		legalError(w, e)
		return
	}
	w.Header().Set("ETag", fmt.Sprintf(`"%d"`, v.Version))
	writeData(w, 200, v, nil)
}
func trimLegalETag(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}
func (h *Handler) adminLegalPublish(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if h.legal == nil {
		legalError(w, legal.ErrNotFound)
		return
	}
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	v, e := h.legal.Publish(r.Context(), legal.Scope(r.PathValue("scope")), expected, actor(r))
	if e != nil {
		legalError(w, e)
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) resolveLegalSnapshot(w http.ResponseWriter, r *http.Request) {
	if h.legal == nil {
		legalError(w, legal.ErrNotFound)
		return
	}
	var in struct {
		SnapshotID string `json:"snapshotId"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, e := h.legal.Resolve(r.Context(), in.SnapshotID)
	if e != nil {
		legalError(w, e)
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) privateCurrentLegal(w http.ResponseWriter, r *http.Request) {
	if h.legal == nil {
		legalError(w, legal.ErrNotFound)
		return
	}
	var in struct {
		Scope  legal.Scope `json:"scope"`
		Locale string      `json:"locale"`
	}
	if !decode(w, r, &in) {
		return
	}
	v, e := h.legal.Current(r.Context(), in.Scope, in.Locale)
	if e != nil {
		legalError(w, e)
		return
	}
	writeData(w, 200, v, nil)
}
