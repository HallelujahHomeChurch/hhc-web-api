package httpapi

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletins"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/inboxcontent"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"github.com/google/uuid"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type inboxVisibilityRepository interface {
	InboxBulletinEntitlements(context.Context) ([]inboxcontent.Entitlement, error)
	InboxBulletins(context.Context, []inboxcontent.Resource) ([]inboxcontent.Bulletin, error)
	InboxSubjectAllowed(context.Context, string) (bool, error)
}
type inboxAssetReadiness interface {
	WebsiteContentReadiness(context.Context, []assetclient.WebsiteContentRef) ([]assetclient.WebsiteContentDecision, error)
}

func (h *Handler) WithInboxVisibility(enabled bool, repo inboxVisibilityRepository, assets inboxAssetReadiness, notificationAppIDs ...string) *Handler {
	h.inboxNotificationAppID = "notification-api"
	if len(notificationAppIDs) > 0 && notificationAppIDs[0] != "" {
		h.inboxNotificationAppID = notificationAppIDs[0]
	}
	h.inboxEnabled = enabled
	h.inboxRepo = repo
	h.inboxAssets = assets
	return h
}

var inboxResourceID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,128}$`)

func validInboxVisibility(input inboxcontent.Request) bool {
	actor, err := uuid.Parse(input.ActorID)
	if err != nil || actor == uuid.Nil || actor.String() != input.ActorID || len(input.Items) < 1 || len(input.Items) > 100 {
		return false
	}
	switch input.Locale {
	case "zh-Hant", "zh-Hans", "en", "ja", "ko":
	default:
		return false
	}
	seen := map[string]bool{}
	for _, ref := range input.Items {
		row, err := uuid.Parse(ref.ID)
		if err != nil || row == uuid.Nil || row.String() != ref.ID || seen[ref.ID] || !inboxResourceID.MatchString(ref.Resource.ID) {
			return false
		}
		seen[ref.ID] = true
		switch ref.Resource.Type {
		case "recording":
			if ref.Resource.Series != "" {
				return false
			}
		case "bulletin":
			if ref.Resource.Series != "general" && ref.Resource.Series != "children" {
				return false
			}
		default:
			return false
		}
	}
	return true
}
func (h *Handler) inboxVisibility(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if h.daprDisabled || h.daprAPIToken == "" {
		writeError(w, 503, "inbox_transport_unavailable", "A reviewed Notification transport is required.")
		return
	}
	if len(r.Header.Values("Dapr-Caller-App-Id")) != 1 || r.Header.Get("Dapr-Caller-App-Id") != h.inboxNotificationAppID || len(r.Header.Values("dapr-api-token")) > 1 {
		writeError(w, 401, "unauthorized", "Trusted Notification identity is required.")
		return
	}
	requireServiceCaller(map[string]bool{h.inboxNotificationAppID: true}, h.daprAPIToken, false, http.HandlerFunc(h.authorizedInboxVisibility)).ServeHTTP(w, r)
}
func (h *Handler) authorizedInboxVisibility(w http.ResponseWriter, r *http.Request) {
	unavailable := func() {
		writeError(w, 503, "inbox_visibility_unavailable", "Current content authorization is unavailable.")
	}
	if !h.inboxEnabled || h.inboxRepo == nil || h.bulletinEntitlements == nil || h.memberQualification == nil || h.inboxAssets == nil {
		unavailable()
		return
	}
	var input inboxcontent.Request
	if !decodeLimit(w, r, &input, 64<<10) {
		return
	}
	if !validInboxVisibility(input) {
		writeError(w, 422, "invalid_request", "The visibility request is invalid.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 1800*time.Millisecond)
	defer cancel()
	decisions := make([]inboxcontent.Decision, len(input.Items))
	for i, v := range input.Items {
		decisions[i].ID = v.ID
	}
	deny := func() {
		writeData(w, 200, struct {
			Items []inboxcontent.Decision `json:"items"`
		}{decisions}, nil)
	}
	q, err := h.memberQualification.CheckMemberQualification(ctx, input.ActorID)
	if err != nil || q.SubjectID != input.ActorID {
		unavailable()
		return
	}
	if !q.Qualified {
		deny()
		return
	}
	allowed, err := h.inboxRepo.InboxSubjectAllowed(ctx, input.ActorID)
	if err != nil {
		unavailable()
		return
	}
	if !allowed {
		deny()
		return
	}
	accepted, err := h.memberLegalAccepted(ctx, input.ActorID, input.Locale)
	if err != nil {
		unavailable()
		return
	}
	if !accepted {
		deny()
		return
	}
	policies, err := h.inboxRepo.InboxBulletinEntitlements(ctx)
	if err != nil {
		unavailable()
		return
	}
	codes := []string{}
	codeSeen := map[string]bool{}
	byEdition := map[string]string{}
	for _, p := range policies {
		if _, ok := bulletins.NormalizeBulletinEdition(p.Series, p.Locale); !ok {
			continue
		}
		byEdition[p.Series+"/"+p.Locale] = p.Code
		if !codeSeen[p.Code] {
			codes = append(codes, p.Code)
			codeSeen[p.Code] = true
		}
	}
	recordIDs := []string{}
	recordSeen := map[string]bool{}
	resources := []inboxcontent.Resource{}
	resourceSeen := map[inboxcontent.Resource]bool{}
	for _, ref := range input.Items {
		if ref.Resource.Type == "recording" {
			if !recordSeen[ref.Resource.ID] {
				recordIDs = append(recordIDs, ref.Resource.ID)
				recordSeen[ref.Resource.ID] = true
			}
		} else if !resourceSeen[ref.Resource] {
			resources = append(resources, ref.Resource)
			resourceSeen[ref.Resource] = true
		}
	}
	if len(recordIDs) > 0 && !codeSeen[recordings.EntitlementCode] {
		codes = append(codes, recordings.EntitlementCode)
	}
	if len(codes) == 0 {
		unavailable()
		return
	}
	grants, err := h.bulletinEntitlements.CheckEntitlements(ctx, input.ActorID, codes)
	if err != nil || len(grants) != len(codes) {
		unavailable()
		return
	}
	authorized := map[string]bool{}
	for i, g := range grants {
		if g.EntitlementCode != codes[i] || g.Decision != "allow" && g.Decision != "deny" {
			unavailable()
			return
		}
		authorized[g.EntitlementCode] = g.Decision == "allow"
	}
	editions := []inboxcontent.Bulletin{}
	if len(resources) > 0 {
		editions, err = h.inboxRepo.InboxBulletins(ctx, resources)
		if err != nil {
			unavailable()
			return
		}
	}
	type pdfEdition struct{ issueID, series, locale, assetID string }
	checkedPDF := map[pdfEdition]bool{}
	readiness := []assetclient.WebsiteContentRef{}
	readySeen := map[assetclient.WebsiteContentRef]bool{}
	for _, v := range editions {
		if !v.Online && authorized[byEdition[v.Series+"/"+v.Locale]] {
			checkedPDF[pdfEdition{v.IssueID, v.Series, v.Locale, v.ProtectedAssetID}] = true
			ref := assetclient.WebsiteContentRef{Type: "bulletin_pdf", ID: v.ProtectedAssetID, ResourceID: v.IssueID, Locale: v.Locale}
			if !readySeen[ref] {
				readiness = append(readiness, ref)
				readySeen[ref] = true
			}
		}
	}
	// Five supported editions across 100 resources remain bounded in owner-sized batches.
	pdfReady := map[assetclient.WebsiteContentRef]bool{}
	for start := 0; start < len(readiness); start += 100 {
		batch := readiness[start:min(start+100, len(readiness))]
		values, err := h.inboxAssets.WebsiteContentReadiness(ctx, batch)
		if err != nil || len(values) != len(batch) {
			unavailable()
			return
		}
		for i, v := range values {
			if v.Ref != batch[i] {
				unavailable()
				return
			}
			pdfReady[v.Ref] = v.Ready
		}
	}
	recordingTitles := map[string]string{}
	if len(recordIDs) > 0 && authorized[recordings.EntitlementCode] {
		if h.recordings == nil {
			unavailable()
			return
		}
		recordingTitles, err = h.recordings.InboxVisible(ctx, recordIDs)
		if err != nil {
			unavailable()
			return
		}
	}
	// Asset readiness cannot authorize a Website edition withdrawn or rebound during the call.
	if len(resources) > 0 {
		editions, err = h.inboxRepo.InboxBulletins(ctx, resources)
		if err != nil {
			unavailable()
			return
		}
	}
	safeText := func(s string) bool { return utf8.ValidString(s) && len(s) <= 500 && !strings.ContainsAny(s, "\x00\r") }
	for i, ref := range input.Items {
		resource := ref.Resource
		if resource.Type == "recording" {
			title, ok := recordingTitles[resource.ID]
			if ok && safeText(title) {
				decisions[i].Visible = true
				decisions[i].Projection = &inboxcontent.Projection{TitleKey: "notification.recording", MessageKey: "notification.published", Parameters: map[string]string{"title": title}, Action: inboxcontent.Action{Type: "recording", Resource: resource}}
			}
			continue
		}
		candidates := []string{input.Locale, "zh-Hant"}
		for _, edition := range bulletins.BulletinEditionOrder {
			if string(edition.Series) == resource.Series {
				candidates = append(candidates, string(edition.Locale))
			}
		}
		for _, language := range candidates {
			found := false
			for _, v := range editions {
				if v.IssueID != resource.ID || v.Series != resource.Series || v.Locale != language || !authorized[byEdition[v.Series+"/"+v.Locale]] || v.IssueNumber == nil || *v.IssueNumber < 1 || !safeText(v.Title) {
					continue
				}
				if !v.Online && (!checkedPDF[pdfEdition{v.IssueID, v.Series, v.Locale, v.ProtectedAssetID}] || !pdfReady[assetclient.WebsiteContentRef{Type: "bulletin_pdf", ID: v.ProtectedAssetID, ResourceID: v.IssueID, Locale: v.Locale}]) {
					continue
				}
				decisions[i].Visible = true
				decisions[i].Projection = &inboxcontent.Projection{TitleKey: "notification.bulletin", MessageKey: "notification.published", Parameters: map[string]string{"title": v.Title, "issueNumber": strconv.Itoa(*v.IssueNumber)}, Action: inboxcontent.Action{Type: "bulletin", Resource: resource, Locale: v.Locale}}
				found = true
				break
			}
			if found {
				break
			}
		}
	}
	deny()
}
