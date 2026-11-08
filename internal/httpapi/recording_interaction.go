package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/bulletinwatermark"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func interactionError(w http.ResponseWriter, e error) {
	switch {
	case errors.Is(e, recordings.ErrCommentReadTimeout):
		writeError(w, 503, "recording_comment_read_timeout", "Comments are temporarily unavailable. Try again shortly.")
	case errors.Is(e, recordings.ErrCommentTraversalLimit):
		writeError(w, 422, "recording_comment_thread_too_large", "This reply branch exceeds the display budget.")
	case errors.Is(e, recordings.ErrCommentCursorExpired):
		writeError(w, 409, "recording_comment_cursor_expired", "Refresh comments to continue.")
	case errors.Is(e, recordings.ErrCommentsClosed):
		writeError(w, 409, "recording_comments_closed", "Comments are closed.")
	case errors.Is(e, recordings.ErrCommentForbidden):
		writeError(w, 403, "recording_comment_forbidden", "Comment cannot be changed.")
	case errors.Is(e, bulletinwatermark.ErrRestricted):
		writeError(w, 403, "processing_restricted", "Processing is restricted.")
	default:
		handleRecordingError(w, e)
	}
}
func (h *Handler) commentNames(r *http.Request, comments []*recordings.Comment) {
	ids := []string{}
	seen := map[string]bool{}
	addID := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	for _, c := range comments {
		c.DisplayName = "會員"
		c.AvatarURL = ""
		if c.Deleted || c.Hidden {
			c.ReplyPreviewAuthors = nil
			continue
		}
		if c.ReplyTo != nil {
			addID(c.ReplyTo.AuthorID)
		}
		addID(c.AuthorID)
		for i := range c.ReplyPreviewAuthors {
			preview := &c.ReplyPreviewAuthors[i]
			preview.DisplayName, preview.AvatarURL = "會員", ""
			addID(preview.AuthorID)
		}
	}
	if h.watermarkIdentities == nil || len(ids) == 0 {
		return
	}
	byID, _ := h.commentAuthors.resolve(r.Context(), h.watermarkIdentities, ids)
	for _, c := range comments {
		if c.ReplyTo != nil {
			if name := byID[c.ReplyTo.AuthorID].DisplayName; name != "" {
				c.ReplyTo.DisplayName = name
			}
		}
		if c.Deleted || c.Hidden {
			continue
		}
		if profile, ok := byID[c.AuthorID]; ok {
			if profile.DisplayName != "" {
				c.DisplayName = profile.DisplayName
			}
			c.AvatarURL = profile.AvatarURL
		}
		for i := range c.ReplyPreviewAuthors {
			preview := &c.ReplyPreviewAuthors[i]
			if profile, ok := byID[preview.AuthorID]; ok {
				if profile.DisplayName != "" {
					preview.DisplayName = profile.DisplayName
				}
				preview.AvatarURL = profile.AvatarURL
			}
		}
	}
}
func (h *Handler) memberRecordingInteractions(w http.ResponseWriter, r *http.Request) {
	if !h.interactionRequest(w, r, false) {
		return
	}
	v, e := h.recordings.RecordingInteractions(r.Context(), r.PathValue("id"), actor(r))
	if e != nil {
		if writeMemberCancellation(w, r, e) {
			return
		}
		interactionError(w, e)
		return
	}
	writeData(w, 200, v, nil)
}
func (h *Handler) memberRecordingLike(w http.ResponseWriter, r *http.Request) {
	if !h.interactionRequest(w, r, false) {
		return
	}
	var input struct {
		Liked *bool `json:"liked"`
	}
	if !broadcastDecode(w, r, &input, 1024) {
		return
	}
	if input.Liked == nil {
		interactionError(w, recordings.ErrInvalid)
		return
	}
	v, e := h.recordings.SetRecordingLike(r.Context(), r.PathValue("id"), actor(r), *input.Liked)
	if e != nil {
		interactionError(w, e)
		return
	}
	writeData(w, 200, v, nil)
}
func commentQuery(r *http.Request) (url.Values, int, error) {
	q, e := url.ParseQuery(r.URL.RawQuery)
	if e != nil {
		return q, 0, recordings.ErrInvalid
	}
	for k, v := range q {
		if len(v) != 1 || k != "parentId" && k != "cursor" && k != "order" && k != "limit" && k != "refresh" && k != "view" {
			return q, 0, recordings.ErrInvalid
		}
	}
	if q.Has("view") && q.Get("view") != "threaded" {
		return q, 0, recordings.ErrInvalid
	}
	limit := 20
	if q.Has("limit") {
		limit, e = strconv.Atoi(q.Get("limit"))
		if e != nil {
			return q, 0, recordings.ErrInvalid
		}
	}
	return q, limit, nil
}
func (h *Handler) memberRecordingComments(w http.ResponseWriter, r *http.Request) {
	if !h.interactionRequest(w, r, true) {
		return
	}
	q, limit, e := commentQuery(r)
	if e != nil {
		interactionError(w, e)
		return
	}
	order := q.Get("order")
	if order == "" {
		order = "newest"
	}
	var v recordings.CommentPage
	if q.Has("refresh") && (q.Get("refresh") != "true" || order != "top" || q.Get("cursor") != "") || order == "top" && q.Get("parentId") != "" {
		interactionError(w, recordings.ErrInvalid)
		return
	}
	if q.Get("view") == "threaded" {
		v, e = h.recordings.ListThreadedRecordingComments(r.Context(), r.PathValue("id"), actor(r), q.Get("parentId"), q.Get("cursor"), order, limit, q.Get("refresh") == "true")
	} else if order == "top" {
		v, e = h.recordings.ListTopRecordingComments(r.Context(), r.PathValue("id"), actor(r), q.Get("cursor"), limit, q.Get("refresh") == "true")
	} else {
		v, e = h.recordings.ListRecordingComments(r.Context(), r.PathValue("id"), actor(r), q.Get("parentId"), q.Get("cursor"), order, limit)
	}
	if e != nil {
		interactionError(w, e)
		return
	}
	comments := make([]*recordings.Comment, len(v.Items))
	for i := range v.Items {
		comments[i] = &v.Items[i]
	}
	h.commentNames(r, comments)
	writeData(w, 200, v, nil)
}
func (h *Handler) memberCreateRecordingComment(w http.ResponseWriter, r *http.Request) {
	if !h.interactionRequest(w, r, false) {
		return
	}
	var input recordings.CommentInput
	if !broadcastDecode(w, r, &input, 16384) {
		return
	}
	v, e := h.recordings.CreateRecordingComment(r.Context(), r.PathValue("id"), actor(r), input)
	if e != nil {
		interactionError(w, e)
		return
	}
	h.commentNames(r, []*recordings.Comment{&v})
	writeData(w, 200, v, nil)
}
func (h *Handler) memberUpdateRecordingComment(w http.ResponseWriter, r *http.Request) {
	if !h.interactionRequest(w, r, false) {
		return
	}
	var input struct {
		Text             string `json:"text"`
		ExpectedRevision int64  `json:"expectedRevision"`
	}
	if !broadcastDecode(w, r, &input, 16384) {
		return
	}
	v, e := h.recordings.UpdateRecordingComment(r.Context(), r.PathValue("id"), actor(r), r.PathValue("commentId"), input.Text, input.ExpectedRevision, r.Method == http.MethodDelete)
	if e != nil {
		interactionError(w, e)
		return
	}
	h.commentNames(r, []*recordings.Comment{&v})
	writeData(w, 200, v, nil)
}
func (h *Handler) memberReportRecordingComment(w http.ResponseWriter, r *http.Request) {
	if !h.interactionRequest(w, r, false) {
		return
	}
	var input recordings.CommentReportInput
	if !broadcastDecode(w, r, &input, 4096) {
		return
	}
	if e := h.recordings.ReportRecordingComment(r.Context(), r.PathValue("id"), actor(r), r.PathValue("commentId"), input.Reason); e != nil {
		interactionError(w, e)
		return
	}
	writeData(w, 200, map[string]bool{"reported": true}, nil)
}
func (h *Handler) adminRecordingModeration(w http.ResponseWriter, r *http.Request) {
	if !h.interactionRequest(w, r, true) {
		return
	}
	q, limit, e := commentQuery(r)
	if e != nil || q.Has("parentId") || q.Has("order") || q.Has("refresh") {
		interactionError(w, recordings.ErrInvalid)
		return
	}
	v, e := h.recordings.ListRecordingModeration(r.Context(), r.PathValue("id"), q.Get("cursor"), limit)
	if e != nil {
		interactionError(w, e)
		return
	}
	comments := make([]*recordings.Comment, len(v.Items))
	for i := range v.Items {
		comments[i] = &v.Items[i].Comment
	}
	h.commentNames(r, comments)
	writeData(w, 200, v, nil)
}
func (h *Handler) adminModerateRecording(w http.ResponseWriter, r *http.Request) {
	if !h.interactionRequest(w, r, false) {
		return
	}
	var input recordings.ModerationInput
	if !broadcastDecode(w, r, &input, 2048) {
		return
	}
	if e := h.recordings.ModerateRecordingComments(r.Context(), r.PathValue("id"), actor(r), input); e != nil {
		interactionError(w, e)
		return
	}
	writeData(w, 200, map[string]bool{"updated": true}, nil)
}
func (h *Handler) registerRecordingInteractionRoutes(admin, member *http.ServeMux) {
	member.HandleFunc("POST /api/member/recordings/{id}/heartbeat", h.requireRecordingEntitlement(h.memberPlaybackHeartbeat))
	member.HandleFunc("GET /api/member/recordings/{id}/metrics", h.requireRecordingEntitlement(h.recordingMetrics(false)))
	admin.HandleFunc("GET /api/admin/recordings/{id}/metrics", requireScope("cms:recordings:read", h.adminAuditProvenance(h.recordingMetrics(true))))
	member.HandleFunc("GET /api/member/recordings/{id}/interactions", h.requireRecordingEntitlement(h.memberRecordingInteractions))
	member.HandleFunc("PUT /api/member/recordings/{id}/like", h.requireRecordingEntitlement(h.memberRecordingLike))
	member.HandleFunc("GET /api/member/recordings/{id}/comments", h.requireRecordingEntitlement(h.memberRecordingComments))
	member.HandleFunc("POST /api/member/recordings/{id}/comments", h.requireRecordingEntitlement(h.memberCreateRecordingComment))
	member.HandleFunc("PATCH /api/member/recordings/{id}/comments/{commentId}", h.requireRecordingEntitlement(h.memberUpdateRecordingComment))
	member.HandleFunc("DELETE /api/member/recordings/{id}/comments/{commentId}", h.requireRecordingEntitlement(h.memberUpdateRecordingComment))
	member.HandleFunc("PUT /api/member/recordings/{id}/comments/{commentId}/reaction", h.requireRecordingEntitlement(h.memberCommentReaction))
	member.HandleFunc("PUT /api/member/recordings/{id}/comments/{commentId}/report", h.requireRecordingEntitlement(h.memberReportRecordingComment))
	admin.HandleFunc("GET /api/admin/recordings/{id}/comments", requireScope("cms:recordings:read", h.adminAuditProvenance(h.adminRecordingModeration)))
	admin.HandleFunc("PUT /api/admin/recordings/{id}/comments/moderation", requireScope("cms:recordings:write", h.adminAuditProvenance(h.adminModerateRecording)))
}

func (h *Handler) interactionRequest(w http.ResponseWriter, r *http.Request, query bool) bool {
	if !h.broadcastRequest(w, r, query) {
		return false
	}
	if !h.recordings.VideoInteractionsEnabled() {
		writeError(w, 503, "recording_interactions_unavailable", "Interactions are unavailable.")
		return false
	}
	return true
}

func (h *Handler) memberCommentReaction(w http.ResponseWriter, r *http.Request) {
	if !h.interactionRequest(w, r, false) {
		return
	}
	var in struct {
		Reaction json.RawMessage `json:"reaction"`
	}
	if !broadcastDecode(w, r, &in, 1024) {
		return
	}
	var reaction *string
	if len(in.Reaction) == 0 || json.Unmarshal(in.Reaction, &reaction) != nil {
		interactionError(w, recordings.ErrInvalid)
		return
	}
	v, e := h.recordings.SetCommentReaction(r.Context(), r.PathValue("id"), actor(r), r.PathValue("commentId"), reaction)
	if e != nil {
		interactionError(w, e)
		return
	}
	writeData(w, 200, v, nil)
}
