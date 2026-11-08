package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/diagnostics"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/operationsclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func (h *Handler) recordingNotificationPolicy(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if h.recordings == nil {
		recordingUnavailable(w)
		return
	}
	available, err := h.recordings.NotificationAvailable(r.Context(), r.PathValue("recordingID"))
	if err != nil {
		recordingUnavailable(w)
		return
	}
	writeData(w, http.StatusOK, map[string]bool{"published": available}, nil)
}

func (h *Handler) requireRecordingEntitlement(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "private, no-store")
		if h.recordings == nil || h.bulletinEntitlements == nil {
			writeError(w, http.StatusServiceUnavailable, "authorization_unavailable", "Recording authorization is unavailable.")
			return
		}
		caller, direct := r.Context().Value(privateServiceCallerKey{}).(string)
		if !direct {
			caller = strings.TrimSpace(r.Header.Get("Dapr-Caller-App-Id"))
			if caller == "" && h.allowDevCaller {
				caller = strings.TrimSpace(r.Header.Get("X-Internal-Caller-App-Id"))
			}
		}
		if caller != h.trustedCaller {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Trusted gateway identity is required.")
			return
		}
		p, ok := r.Context().Value(principalKey{}).(principal)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Trusted gateway identity is required.")
			return
		}
		decisions, err := h.bulletinEntitlements.CheckEntitlements(r.Context(), p.UserID, []string{recordings.EntitlementCode})
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "authorization_unavailable", "Recording authorization is unavailable.")
			return
		}
		if len(decisions) != 1 || decisions[0].EntitlementCode != recordings.EntitlementCode || decisions[0].Decision != "allow" {
			writeError(w, http.StatusNotFound, "not_found", "Recording not found.")
			return
		}
		if !h.requireCurrentMemberLegal(w, r) {
			return
		}
		next(w, r)
	}
}

func (h *Handler) memberListRecordings(w http.ResponseWriter, r *http.Request) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	search, searchErr := recordings.NormalizeSearch(query.Get("q"))
	if err != nil || searchErr != nil || len(query["q"]) > 1 {
		handleRecordingError(w, recordings.ErrInvalid)
		return
	}
	if search != "" && !query.Has("limit") {
		handleRecordingError(w, recordings.ErrInvalid)
		return
	}
	if query.Has("limit") || query.Has("cursor") {
		limit, err := strconv.Atoi(query.Get("limit"))
		if err != nil || len(query["limit"]) != 1 || len(query["cursor"]) > 1 {
			handleRecordingError(w, recordings.ErrInvalid)
			return
		}
		values, next, err := h.recordings.ListPage(r.Context(), query.Get("cursor"), limit, search)
		if err != nil {
			handleMemberListError(w, r, err)
			return
		}
		p, ok := r.Context().Value(principalKey{}).(principal)
		if !ok {
			writeError(w, http.StatusUnauthorized, "unauthorized", "Trusted gateway identity is required.")
			return
		}
		ids := make([]string, len(values))
		for i, value := range values {
			ids[i] = value.ID
		}
		progress, err := h.recordings.GetCardProgress(r.Context(), p.UserID, ids)
		if err != nil {
			handleMemberListError(w, r, err)
			return
		}
		type memberCard struct {
			recordings.Recording
			PlaybackProgress     *recordings.CardProgress `json:"playbackProgress"`
			PlaybackMediaVersion *string                  `json:"playbackMediaVersion"`
		}
		projectionDone := diagnostics.StartRequestPhase(r.Context(), "projection")
		cards := make([]memberCard, len(values))
		for i, value := range values {
			cards[i].Recording = value
			if item, ok := progress[value.ID]; ok {
				cards[i].PlaybackProgress = item.Progress
				cards[i].PlaybackMediaVersion = &item.MediaVersion
			}
		}
		projectionDone()
		var nextCursor any
		if next != "" {
			nextCursor = next
		}
		defer diagnostics.StartRequestPhase(r.Context(), "serialization")()
		writeData(w, http.StatusOK, cards, map[string]any{"nextCursor": nextCursor})
		return
	}
	values, err := h.recordings.List(r.Context())
	if err != nil {
		handleMemberListError(w, r, err)
		return
	}
	defer diagnostics.StartRequestPhase(r.Context(), "serialization")()
	writeData(w, http.StatusOK, values, nil)
}

func (h *Handler) memberRecordingPlayback(w http.ResponseWriter, r *http.Request) {
	var input struct {
		PlaybackScopeID        string `json:"playbackScopeId"`
		ExpectedAssetVersionID string `json:"expectedAssetVersionId"`
	}
	if !decode(w, r, &input) {
		return
	}
	p := r.Context().Value(principalKey{}).(principal)
	value, err := h.recordings.Playback(r.Context(), r.PathValue("id"), input.PlaybackScopeID, input.ExpectedAssetVersionID, p.UserID)
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	writeData(w, http.StatusOK, value, nil)
}

func (h *Handler) adminCreateRecording(w http.ResponseWriter, r *http.Request) {
	if h.recordings == nil {
		writeError(w, http.StatusServiceUnavailable, "recordings_unavailable", "Recording service is unavailable.")
		return
	}
	var input struct {
		Title string `json:"title"`
	}
	if !decode(w, r, &input) {
		return
	}
	value, err := h.recordings.Create(r.Context(), strings.TrimSpace(input.Title), actor(r), strings.TrimSpace(r.Header.Get("Idempotency-Key")))
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	writeData(w, http.StatusCreated, value, nil)
}

type adminRecording struct {
	recordings.Recording
	IsBroadcast        bool                                     `json:"isBroadcast,omitempty"`
	BroadcastStartedAt *time.Time                               `json:"broadcastStartedAt,omitempty"`
	ProcessingProgress *assetclient.RecordingProcessingProgress `json:"processingProgress,omitempty"`
	Capture            *recordings.AdminCaptureSummary          `json:"capture,omitempty"`
}

func (h *Handler) adminListRecordings(w http.ResponseWriter, r *http.Request) {
	if h.recordings == nil {
		recordingUnavailable(w)
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(query["origin"]) > 1 {
		handleRecordingError(w, recordings.ErrInvalid)
		return
	}
	values, err := h.recordings.AdminList(r.Context(), query.Get("origin"))
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.recording.list", "", auditclient.Metadata{}) {
		return
	}
	items := make([]adminRecording, 0, len(values))
	for _, value := range values {
		items = append(items, adminRecording{Recording: value, IsBroadcast: value.Broadcast, BroadcastStartedAt: value.BroadcastStartedAt})
	}
	writeData(w, http.StatusOK, items, nil)
}

func (h *Handler) adminGetRecording(w http.ResponseWriter, r *http.Request) {
	if h.recordings == nil {
		recordingUnavailable(w)
		return
	}
	value, err := h.recordings.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.recording.read", value.ID, auditclient.Metadata{}) {
		return
	}
	capture, err := h.recordings.AdminCaptureSummary(r.Context(), value.ID)
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	w.Header().Set("ETag", `"`+strconv.FormatInt(value.Version, 10)+`"`)
	writeData(w, http.StatusOK, adminRecording{Recording: value, IsBroadcast: value.Broadcast, BroadcastStartedAt: value.BroadcastStartedAt, ProcessingProgress: value.ProcessingProgress, Capture: capture}, nil)
}

func (h *Handler) adminUpdateRecordingTitle(w http.ResponseWriter, r *http.Request) {
	if h.recordings == nil {
		recordingUnavailable(w)
		return
	}
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input struct {
		Title       *string         `json:"title"`
		Description json.RawMessage `json:"description"`
	}
	if !decodeLimit(w, r, &input, 64<<10) {
		return
	}
	if input.Title == nil {
		writeError(w, http.StatusBadRequest, "invalid_recording", "Supply one recording change.")
		return
	}
	var description *string
	if len(input.Description) > 0 {
		if err := json.Unmarshal(input.Description, &description); err != nil || description == nil {
			writeError(w, http.StatusBadRequest, "invalid_recording", "Description must be plain text.")
			return
		}
	}
	current, err := h.recordings.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	p, _ := r.Context().Value(principalKey{}).(principal)
	canPublish := p.Scopes["cms:recordings:publish"] || p.Scopes["*"]
	if current.Status == "published" && !canPublish {
		writeError(w, http.StatusForbidden, "forbidden", "Publishing capability is required to edit published recordings.")
		return
	}
	value, err := h.recordings.UpdateTitle(r.Context(), current.ID, expected, *input.Title, actor(r), canPublish, description)
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	writeData(w, http.StatusOK, value, nil)
}

func (h *Handler) adminSetRecordingExposure(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	writeError(w, http.StatusGone, "recording_exposure_retired", "Pinning and hiding are retired. Use unpublish to return a recording to draft.")
}

func (h *Handler) adminPublishRecording(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	if h.recordings == nil {
		recordingUnavailable(w)
		return
	}
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	var input struct{}
	if !decode(w, r, &input) {
		return
	}
	keys := r.Header.Values("Idempotency-Key")
	key := r.Header.Get("Idempotency-Key")
	p := r.Context().Value(principalKey{}).(principal)
	if len(keys) > 1 || (len(keys) > 0 && !recordings.ValidOperationKey(key)) || (p.ActorType == "service" && key == "") {
		writeError(w, http.StatusBadRequest, "invalid_recording", "One bounded Idempotency-Key is required for service publication.")
		return
	}
	if key != "" {
		value, err := h.recordings.PublishOperation(r.Context(), r.PathValue("id"), expected, actor(r), key)
		if err != nil {
			handleRecordingError(w, err)
			return
		}
		writeData(w, http.StatusOK, value, nil)
		return
	}
	value, err := h.recordings.Publish(r.Context(), r.PathValue("id"), expected, actor(r))
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	writeData(w, http.StatusOK, value, nil)
}

func (h *Handler) adminUnpublishRecording(w http.ResponseWriter, r *http.Request) {
	if h.recordings == nil {
		recordingUnavailable(w)
		return
	}
	expected, ok := ifMatch(w, r)
	if !ok {
		return
	}
	value, err := h.recordings.Unpublish(r.Context(), r.PathValue("id"), expected, actor(r))
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	writeData(w, http.StatusOK, value, nil)
}

func recordingUnavailable(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, "recordings_unavailable", "Recording service is unavailable.")
}

func handleRecordingError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, assetclient.ErrCoverTooLarge):
		writeError(w, 413, "cover_too_large", "Cover exceeds 5 MiB.")
	case errors.Is(err, assetclient.ErrInvalidCover):
		writeError(w, 422, "invalid_cover", "Invalid JPEG/PNG or unsupported dimensions/orientation.")
	case errors.Is(err, recordings.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "Publish capability is required to change a published recording.")
	case errors.Is(err, assetclient.ErrPackageTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "package_too_large", "The recording package exceeds 10GB.")
	case errors.Is(err, assetclient.ErrSourceTooLarge):
		writeError(w, http.StatusRequestEntityTooLarge, "source_too_large", "The original recording exceeds 50GB.")
	case errors.Is(err, recordings.ErrNotFound), errors.Is(err, assetclient.ErrNotFound), errors.Is(err, assetclient.ErrForbidden):
		writeError(w, http.StatusNotFound, "not_found", "Recording not found.")
	case errors.Is(err, recordings.ErrInvalid), errors.Is(err, assetclient.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", "The recording request is invalid.")
	case errors.Is(err, recordings.ErrPrecondition):
		writeError(w, http.StatusPreconditionFailed, "recording_version_changed", "The recording version changed.")
	case errors.Is(err, recordings.ErrDraftLimit):
		writeError(w, http.StatusConflict, "recording_draft_limit", "At most two ordinary recording drafts may await upload per creator.")
	case errors.Is(err, recordings.ErrConflict), errors.Is(err, assetclient.ErrConflict):
		writeError(w, http.StatusConflict, "recording_conflict", "The recording conflicts with an existing record.")
	case errors.Is(err, operationsclient.ErrUnavailable), errors.Is(err, assetclient.ErrUnavailable):
		writeError(w, http.StatusServiceUnavailable, "recordings_unavailable", "Recording service is unavailable.")
	case errors.Is(err, recordings.ErrTransactionCapacity):
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "recordings_unavailable", "Recording service is unavailable.")
	case errors.Is(err, recordings.ErrAuditUnavailable):
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable", "Audit recording is unavailable.")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", "The recording request failed.")
	}
}

// A cancelled incoming request is distinct from an independent dependency failure.
// Keep this scoped to member listing; cancellation is still an unsuccessful request.
func handleMemberListError(w http.ResponseWriter, r *http.Request, err error) {
	if r.Context().Err() == context.Canceled && errors.Is(err, context.Canceled) {
		writeError(w, 499, "request_canceled", "The request was canceled.")
		return
	}
	handleRecordingError(w, err)
}
