package httpapi

import (
	"net/http"
	"strconv"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func (h *Handler) adminCreateRecordingSource(w http.ResponseWriter, r *http.Request) {
	if !h.recordingPackagesAvailable(w) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(r.Header.Values("Idempotency-Key")) != 1 || !recordings.ValidOperationKey(key) {
		handleRecordingError(w, recordings.ErrInvalid)
		return
	}
	var input struct {
		FileName       string `json:"fileName"`
		SizeBytes      int64  `json:"sizeBytes"`
		ChecksumSHA256 string `json:"checksumSHA256"`
	}
	if !decode(w, r, &input) {
		return
	}
	value, err := h.recordings.CreateSource(r.Context(), r.PathValue("id"), assetclient.RecordingSourceInput{FileName: input.FileName, SizeBytes: input.SizeBytes, ChecksumSHA256: input.ChecksumSHA256}, key)
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	writeData(w, http.StatusCreated, value, nil)
}

func (h *Handler) adminRecordingSourceStatus(w http.ResponseWriter, r *http.Request) {
	if !h.recordingPackagesAvailable(w) {
		return
	}
	cursor, limit := 0, 1000
	query := r.URL.Query()
	for key, target := range map[string]*int{"cursor": &cursor, "limit": &limit} {
		if len(query[key]) > 1 {
			handleRecordingError(w, recordings.ErrInvalid)
			return
		}
		if query.Has(key) {
			value, err := strconv.Atoi(query.Get(key))
			if err != nil {
				handleRecordingError(w, recordings.ErrInvalid)
				return
			}
			*target = value
		}
	}
	value, err := h.recordings.SourceStatus(r.Context(), r.PathValue("id"), r.PathValue("sourceID"), cursor, limit)
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.recording.read", r.PathValue("id"), auditclient.Metadata{}) {
		return
	}
	writeData(w, http.StatusOK, value, nil)
}

func (h *Handler) adminSignRecordingSource(w http.ResponseWriter, r *http.Request) {
	if !h.recordingPackagesAvailable(w) {
		return
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	var input struct {
		Numbers []int `json:"numbers"`
	}
	if !decode(w, r, &input) {
		return
	}
	value, err := h.recordings.SignSource(r.Context(), r.PathValue("id"), r.PathValue("sourceID"), input.Numbers)
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	writeData(w, http.StatusOK, value, nil)
}

func (h *Handler) adminCompleteRecordingSource(w http.ResponseWriter, r *http.Request) {
	if !h.recordingPackagesAvailable(w) {
		return
	}
	var input struct{}
	if !decode(w, r, &input) {
		return
	}
	value, err := h.recordings.CompleteSource(r.Context(), r.PathValue("id"), r.PathValue("sourceID"))
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	writeData(w, http.StatusAccepted, value, nil)
}

func (h *Handler) adminRetryRecordingSource(w http.ResponseWriter, r *http.Request) {
	if !h.recordingPackagesAvailable(w) {
		return
	}
	var input struct{}
	if !decode(w, r, &input) {
		return
	}
	value, err := h.recordings.RetrySource(r.Context(), r.PathValue("id"), r.PathValue("sourceID"))
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	writeData(w, http.StatusAccepted, value, nil)
}
