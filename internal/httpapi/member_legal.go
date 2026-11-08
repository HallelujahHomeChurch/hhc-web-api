package httpapi

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/legal"
	"net/http"
)

type legalAcceptanceChecker interface {
	CheckLegalAcceptance(context.Context, string, string, string, string) (bool, error)
}

func (h *Handler) WithMemberLegalEnforcement(enabled bool, checker legalAcceptanceChecker) *Handler {
	h.memberLegalEnforcement = enabled
	h.legalAcceptance = checker
	return h
}
func (h *Handler) requireCurrentMemberLegal(w http.ResponseWriter, r *http.Request) bool {
	if !h.memberLegalEnforcement {
		return true
	}
	w.Header().Set("Cache-Control", "private, no-store")
	unavailable := func() bool {
		writeError(w, 503, "authorization_unavailable", "Authorization is unavailable.")
		return false
	}
	if h.legal == nil || h.memberQualification == nil || h.legalAcceptance == nil {
		return unavailable()
	}
	subject := actor(r)
	q, err := h.memberQualification.CheckMemberQualification(r.Context(), subject)
	if err != nil {
		return unavailable()
	}
	if q.SubjectID != subject || !q.Qualified {
		writeError(w, 403, "access_required", "Access is required.")
		return false
	}
	accepted, err := h.memberLegalAccepted(r.Context(), subject, locale(r))
	if err != nil {
		return unavailable()
	}
	if !accepted {
		writeError(w, 428, "policy_acceptance_required", "Review and confirm the current policies.")
		return false
	}
	return true
}

func (h *Handler) memberLegalAccepted(ctx context.Context, subject, language string) (bool, error) {
	if !h.memberLegalEnforcement {
		return true, nil
	}
	if h.legal == nil || h.legalAcceptance == nil {
		return false, legal.ErrNotFound
	}
	member, err := h.legal.Current(ctx, legal.VerifiedMember, language)
	if err != nil {
		return false, err
	}
	common, err := h.legal.Current(ctx, legal.Common, language)
	if err != nil {
		return false, err
	}
	if member.Manifest.CommonTermsVersion != common.Manifest.TermsVersion || member.Manifest.CommonPrivacyNoticeVersion != common.Manifest.PrivacyNoticeVersion {
		return false, legal.ErrConflict
	}
	for _, manifest := range []legal.Manifest{common.Manifest, member.Manifest} {
		accepted, err := h.legalAcceptance.CheckLegalAcceptance(ctx, subject, string(manifest.Scope), manifest.TermsVersion, manifest.PrivacyNoticeVersion)
		if err != nil || !accepted {
			return false, err
		}
	}
	return true, nil
}
