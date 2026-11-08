package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
)

type onlineAvailabilityKey struct{}
type onlineDiscoveryLocalesKey struct{}

func (h *Handler) requireOnlineDiscoveryEntitlements(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		query := r.URL.Query()
		locales := []string{query.Get("locale")}
		if query.Has("locales") {
			locales = strings.Split(query.Get("locales"), ",")
			seen := map[string]bool{}
			if query.Has("locale") || len(query["locales"]) != 1 || len(locales) > 3 {
				memberOnlineError(w, r, bulletins.ErrInvalid)
				return
			}
			for _, locale := range locales {
				if locale == "" || seen[locale] || strings.TrimSpace(locale) != locale {
					memberOnlineError(w, r, bulletins.ErrInvalid)
					return
				}
				seen[locale] = true
			}
		}
		var checked *http.Request
		for i, locale := range locales {
			candidate := r.Clone(r.Context())
			selector := candidate.URL.Query()
			selector.Set("locale", locale)
			candidate.URL.RawQuery = selector.Encode()
			checked = nil
			h.requireOnlineBulletinEntitlement(func(_ http.ResponseWriter, allowed *http.Request) { checked = allowed })(w, candidate)
			if checked == nil {
				return
			}
			locales[i] = protectedBulletinFrom(checked).locale
		}
		next(w, checked.WithContext(context.WithValue(checked.Context(), onlineDiscoveryLocalesKey{}, locales)))
	}
}

// Use the exact PDF checker; the context flag only distinguishes an owner's
// terminal denial from an unrelated gateway/router 404 for offline cleanup.
func (h *Handler) requireOnlineBulletinEntitlement(next http.HandlerFunc) http.HandlerFunc {
	checked := h.requireBulletinEntitlement(next)
	return func(w http.ResponseWriter, r *http.Request) {
		checked(w, r.WithContext(context.WithValue(r.Context(), onlineAvailabilityKey{}, true)))
	}
}

func bulletinNotFound(w http.ResponseWriter, r *http.Request) {
	if marked, _ := r.Context().Value(onlineAvailabilityKey{}).(bool); marked {
		w.Header().Set("X-HHC-Bulletin-Access", "unavailable")
	}
	writeError(w, http.StatusNotFound, "not_found", "The bulletin was not found.")
}

func memberOnlineError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, bulletins.ErrNotFound):
		bulletinNotFound(w, r)
	case errors.Is(err, bulletins.ErrInvalid):
		writeError(w, 400, "invalid_request", "The request is invalid.")
	case errors.Is(err, bulletins.ErrConflict):
		writeError(w, 409, "reader_access_conflict", "Use a new access request for this revision.")
	default:
		writeError(w, 503, "bulletin_unavailable", "The bulletin is unavailable.")
	}
}

func (h *Handler) memberOnlineDiscovery(w http.ResponseWriter, r *http.Request) {
	request := protectedBulletinFrom(r)
	integer := func(key string, fallback int) (int, error) {
		raw := r.URL.Query().Get(key)
		if raw == "" {
			return fallback, nil
		}
		v, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || strconv.FormatInt(v, 10) != raw {
			return 0, bulletins.ErrInvalid
		}
		return int(v), nil
	}
	offset, e1 := integer("offset", 0)
	limit, e2 := integer("limit", 20)
	var issue *int
	if r.URL.Query().Has("issueNumber") {
		v, err := integer("issueNumber", 0)
		if err != nil || v < 1 {
			memberOnlineError(w, r, bulletins.ErrInvalid)
			return
		}
		issue = &v
	}
	if e1 != nil || e2 != nil {
		memberOnlineError(w, r, bulletins.ErrInvalid)
		return
	}
	locales, _ := r.Context().Value(onlineDiscoveryLocalesKey{}).([]string)
	value, err := h.service.DiscoverOnline(r.Context(), request.series, locales, offset, limit, issue)
	if err != nil {
		memberOnlineError(w, r, err)
		return
	}
	writeData(w, 200, value, nil)
}

func (h *Handler) memberOnlineAccess(w http.ResponseWriter, r *http.Request) {
	request := protectedBulletinFrom(r)
	var input onlinebulletins.ReaderAccessInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
		memberOnlineError(w, r, bulletins.ErrInvalid)
		return
	}
	actor, _ := r.Context().Value(principalKey{}).(principal)
	value, err := h.service.OpenOnline(r.Context(), r.PathValue("issueID"), request.series, request.locale, actor.UserID, h.watermarkPrivacyVersion, input)
	if err != nil {
		memberOnlineError(w, r, err)
		return
	}
	if err := h.hydrateBulletinIllustrations(r.Context(), r.PathValue("issueID"), value.Document.Content.Components, value.Document.Content.IllustrationAssets); err != nil {
		memberOnlineError(w, r, err)
		return
	}
	raw, err := json.Marshal(envelope{Data: value, Meta: map[string]any{}, Error: nil})
	if err != nil || len(raw) > 9<<20 {
		memberOnlineError(w, r, assetclient.ErrUnavailable)
		return
	}
	sum := sha256.Sum256(raw)
	w.Header().Set("X-HHC-Content-SHA256", hex.EncodeToString(sum[:]))
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}
