package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

type captureResponseWriter struct {
	http.ResponseWriter
	preserveAuthorization bool
}

func (w captureResponseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w captureResponseWriter) Write(raw []byte) (int, error) {
	var body map[string]json.RawMessage
	if json.Unmarshal(raw, &body) == nil {
		var failure struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}
		if json.Unmarshal(body["error"], &failure) == nil && failure.Code != "" && !strings.HasPrefix(failure.Code, "capture_") {
			if w.preserveAuthorization && (failure.Code == "policy_acceptance_required" || failure.Code == "access_required" || failure.Code == "authorization_unavailable" || failure.Code == "request_canceled") {
				return w.ResponseWriter.Write(raw)
			}
			failure.Code = map[string]string{"unauthorized": "capture_unauthorized", "forbidden": "capture_forbidden", "invalid_request": "capture_invalid", "not_found": "capture_not_found"}[failure.Code]
			if failure.Code == "" {
				failure.Code = "capture_unavailable"
			}
			failure.Message = failure.Code
			body["error"], _ = json.Marshal(failure)
			out, _ := json.Marshal(body)
			_, err := w.ResponseWriter.Write(append(out, '\n'))
			return len(raw), err
		}
	}
	return w.ResponseWriter.Write(raw)
}
func captureErrors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/member/recordings/") && strings.Contains(r.URL.Path, "/live/") && strings.HasSuffix(r.URL.Path, "/cover") {
			w.Header().Set("Cache-Control", "private, no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/admin/recordings/") && strings.Contains(r.URL.Path, "/captures") || r.URL.Path == "/api/member/recordings/live" || strings.HasPrefix(r.URL.Path, "/api/member/recordings/") && strings.HasSuffix(r.URL.Path, "/live/playback") {
			w.Header().Set("Cache-Control", "private, no-store")
			w.Header().Set("Referrer-Policy", "no-referrer")
			next.ServeHTTP(captureResponseWriter{w, strings.HasPrefix(r.URL.Path, "/api/member/")}, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func captureError(w http.ResponseWriter, err error) {
	status, code := 503, "capture_unavailable"
	switch {
	case errors.Is(err, recordings.ErrInvalid), errors.Is(err, assetclient.ErrInvalid):
		status, code = 400, "capture_invalid"
	case errors.Is(err, recordings.ErrForbidden), errors.Is(err, assetclient.ErrForbidden):
		status, code = 403, "capture_forbidden"
	case errors.Is(err, recordings.ErrNotFound), errors.Is(err, assetclient.ErrNotFound):
		status, code = 404, "capture_not_found"
	case errors.Is(err, assetclient.ErrCaptureExpired):
		status, code = 410, "capture_expired"
	case errors.Is(err, assetclient.ErrCaptureMissingObjects):
		status, code = 409, "capture_missing_objects"
	case errors.Is(err, recordings.ErrConflict), errors.Is(err, recordings.ErrPrecondition), errors.Is(err, assetclient.ErrConflict):
		status, code = 409, "capture_conflict"
	case errors.Is(err, assetclient.ErrPackageTooLarge):
		status, code = 413, "capture_too_large"
	}
	if status == 503 {
		w.Header().Set("Retry-After", "5")
	}
	writeError(w, status, code, code)
}
func captureDecode(w http.ResponseWriter, r *http.Request, input any, limit int64) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			captureError(w, assetclient.ErrPackageTooLarge)
		} else {
			captureError(w, recordings.ErrInvalid)
		}
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	return decodeLimit(w, r, input, limit)
}
func (h *Handler) captureAvailable(w http.ResponseWriter, r *http.Request, read bool) bool {
	if h.recordings == nil {
		captureError(w, assetclient.ErrUnavailable)
		return false
	}
	if !read && r.URL.RawQuery != "" {
		captureError(w, recordings.ErrInvalid)
		return false
	}
	return true
}
func (h *Handler) adminCreateRecordingCapture(w http.ResponseWriter, r *http.Request) {
	if !h.captureAvailable(w, r, false) {
		return
	}
	var input struct {
		OperationKey    string `json:"operationKey"`
		ExpectedVersion int64  `json:"expectedVersion"`
		AutoPublish     *bool  `json:"autoPublish"`
		LiveEnabled     *bool  `json:"liveEnabled"`
	}
	if !captureDecode(w, r, &input, 256<<10) {
		return
	}
	if input.AutoPublish == nil || input.LiveEnabled == nil {
		captureError(w, recordings.ErrInvalid)
		return
	}
	p := r.Context().Value(principalKey{}).(principal)
	result, err := h.recordings.CreateCapture(r.Context(), r.PathValue("id"), actor(r), recordings.CaptureCreateInput{OperationKey: input.OperationKey, ExpectedVersion: input.ExpectedVersion, AutoPublish: *input.AutoPublish, LiveEnabled: *input.LiveEnabled}, p.Scopes["cms:recordings:publish"] || p.Scopes["*"])
	if err != nil {
		captureError(w, err)
		return
	}
	writeData(w, 201, result, nil)
}
func (h *Handler) adminGetRecordingCapture(w http.ResponseWriter, r *http.Request) {
	if !h.captureAvailable(w, r, true) {
		return
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		captureError(w, recordings.ErrInvalid)
		return
	}
	for key, values := range query {
		if (key != "cursor" && key != "limit") || len(values) != 1 {
			captureError(w, recordings.ErrInvalid)
			return
		}
	}
	limit := 1000
	if query.Has("limit") {
		limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil {
			captureError(w, recordings.ErrInvalid)
			return
		}
	}
	result, err := h.recordings.CaptureStatus(r.Context(), r.PathValue("id"), r.PathValue("captureId"), actor(r), query.Get("cursor"), limit)
	if err != nil {
		captureError(w, err)
		return
	}
	writeData(w, 200, result, nil)
}
func (h *Handler) adminDeclareRecordingCapture(w http.ResponseWriter, r *http.Request) {
	if !h.captureAvailable(w, r, false) {
		return
	}
	var input assetclient.RecordingCaptureDeclareInput
	if !captureDecode(w, r, &input, 256<<10) {
		return
	}
	result, err := h.recordings.DeclareCapture(r.Context(), r.PathValue("id"), r.PathValue("captureId"), actor(r), input)
	if err != nil {
		captureError(w, err)
		return
	}
	writeData(w, 200, result, nil)
}
func (h *Handler) adminConfirmRecordingCapture(w http.ResponseWriter, r *http.Request) {
	if !h.captureAvailable(w, r, false) {
		return
	}
	var input assetclient.RecordingCaptureConfirmInput
	if !captureDecode(w, r, &input, 256<<10) {
		return
	}
	result, err := h.recordings.ConfirmCapture(r.Context(), r.PathValue("id"), r.PathValue("captureId"), actor(r), input)
	if err != nil {
		captureError(w, err)
		return
	}
	writeData(w, 202, result, nil)
}
func (h *Handler) adminSignRecordingCapture(w http.ResponseWriter, r *http.Request) {
	if !h.captureAvailable(w, r, false) {
		return
	}
	var input struct {
		Paths []string `json:"paths"`
	}
	if !captureDecode(w, r, &input, 256<<10) {
		return
	}
	result, err := h.recordings.SignCapture(r.Context(), r.PathValue("id"), r.PathValue("captureId"), actor(r), input.Paths)
	if err != nil {
		captureError(w, err)
		return
	}
	writeData(w, 200, result, nil)
}
func (h *Handler) adminSealRecordingCapture(w http.ResponseWriter, r *http.Request) {
	if !h.captureAvailable(w, r, false) {
		return
	}
	var input assetclient.RecordingCaptureSealInput
	if !captureDecode(w, r, &input, 8<<20) {
		return
	}
	result, err := h.recordings.SealCapture(r.Context(), r.PathValue("id"), r.PathValue("captureId"), actor(r), input)
	if err != nil {
		captureError(w, err)
		return
	}
	writeData(w, 202, result, nil)
}
func (h *Handler) adminAbortRecordingCapture(w http.ResponseWriter, r *http.Request) {
	if !h.captureAvailable(w, r, false) {
		return
	}
	var input assetclient.RecordingCaptureAbortInput
	if !captureDecode(w, r, &input, 256<<10) {
		return
	}
	result, err := h.recordings.AbortCapture(r.Context(), r.PathValue("id"), r.PathValue("captureId"), actor(r), input)
	if err != nil {
		captureError(w, err)
		return
	}
	writeData(w, 200, result, nil)
}
func (h *Handler) adminLocalRecordingCapture(w http.ResponseWriter, r *http.Request) {
	if !h.captureAvailable(w, r, false) {
		return
	}
	var input struct {
		OperationKey string `json:"operationKey"`
	}
	if !captureDecode(w, r, &input, 256<<10) {
		return
	}
	p := r.Context().Value(principalKey{}).(principal)
	canPublish := p.Scopes["cms:recordings:publish"] || p.Scopes["*"]
	var result recordings.CaptureResult
	var err error
	switch {
	case strings.HasSuffix(r.URL.Path, "/stop"):
		result, err = h.recordings.StopCapture(r.Context(), r.PathValue("id"), r.PathValue("captureId"), actor(r), input.OperationKey)
	case strings.HasSuffix(r.URL.Path, "/auto-publish"):
		result, err = h.recordings.CancelCaptureAutoPublish(r.Context(), r.PathValue("id"), r.PathValue("captureId"), actor(r), input.OperationKey, canPublish)
	case strings.HasSuffix(r.URL.Path, "/live"):
		result, err = h.recordings.CloseCaptureLive(r.Context(), r.PathValue("id"), r.PathValue("captureId"), actor(r), input.OperationKey, canPublish)
	}
	if err != nil {
		captureError(w, err)
		return
	}
	writeData(w, 200, result, nil)
}
