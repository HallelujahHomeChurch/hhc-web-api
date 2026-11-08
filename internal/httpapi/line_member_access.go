package httpapi

import (
	"errors"
	"net/http"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/legal"
	"github.com/google/uuid"
)

type lineMemberAccess struct {
	Qualified bool                        `json:"qualified"`
	Editions  []bulletins.BulletinEdition `json:"editions"`
	Legal     string                      `json:"legal"`
}

func (h *Handler) lineMemberAccess(w http.ResponseWriter, r *http.Request) {
	subject := actor(r)
	parsed, err := uuid.Parse(subject)
	if err != nil || parsed.String() != subject || (parsed.Version() != 4 && parsed.Version() != 7) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "Trusted Account subject is required.")
		return
	}
	for key, values := range r.URL.Query() {
		if key != "locale" || len(values) != 1 || !legal.ValidLocale(values[0]) {
			writeError(w, 400, "invalid_request", "Only the UI locale selector is supported.")
			return
		}
	}
	if !legal.ValidLocale(locale(r)) {
		writeError(w, 400, "invalid_request", "The UI locale is invalid.")
		return
	}
	unavailable := func() { writeError(w, 503, "authorization_unavailable", "Member authorization is unavailable.") }
	if h.memberQualification == nil || h.bulletinEntitlements == nil || h.service == nil {
		unavailable()
		return
	}
	qualification, err := h.memberQualification.CheckMemberQualification(r.Context(), subject)
	if err != nil || qualification.SubjectID != subject {
		unavailable()
		return
	}
	result := lineMemberAccess{Qualified: qualification.Qualified, Editions: []bulletins.BulletinEdition{}, Legal: "not_applicable"}
	if !qualification.Qualified {
		writeData(w, 200, result, nil)
		return
	}
	codes := []string{}
	codeIndex := map[string]int{}
	policies := []struct {
		edition bulletins.BulletinEdition
		code    string
	}{}
	for _, edition := range bulletins.BulletinEditionOrder {
		code, err := h.service.BulletinEntitlement(r.Context(), string(edition.Series), string(edition.Locale))
		if errors.Is(err, bulletins.ErrNotFound) {
			continue
		}
		if err != nil || code == "" {
			unavailable()
			return
		}
		if _, exists := codeIndex[code]; !exists {
			codeIndex[code] = len(codes)
			codes = append(codes, code)
		}
		policies = append(policies, struct {
			edition bulletins.BulletinEdition
			code    string
		}{edition, code})
	}
	if len(codes) > 0 {
		decisions, err := h.bulletinEntitlements.CheckEntitlements(r.Context(), subject, codes)
		if err != nil || len(decisions) != len(codes) {
			unavailable()
			return
		}
		for i, decision := range decisions {
			if decision.EntitlementCode != codes[i] || (decision.Decision != "allow" && decision.Decision != "deny") {
				unavailable()
				return
			}
		}
		for _, policy := range policies {
			if decisions[codeIndex[policy.code]].Decision == "allow" {
				result.Editions = append(result.Editions, policy.edition)
			}
		}
	}
	accepted, err := h.memberLegalAccepted(r.Context(), subject, locale(r))
	if err != nil {
		unavailable()
		return
	}
	result.Legal = "required"
	if accepted {
		result.Legal = "allowed"
	}
	writeData(w, 200, result, nil)
}
