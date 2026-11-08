package httpapi

import (
	"net/http"
	"strconv"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

func (h *Handler) recordingPackagesAvailable(w http.ResponseWriter) bool {
	w.Header().Set("Cache-Control", "private, no-store")
	if h.recordings == nil {
		recordingUnavailable(w)
		return false
	}
	return true
}

func (h *Handler) adminCreateRecordingPackage(w http.ResponseWriter, r *http.Request) {
	if !h.recordingPackagesAvailable(w) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(r.Header.Values("Idempotency-Key")) != 1 || !recordings.ValidOperationKey(key) {
		handleRecordingError(w, recordings.ErrInvalid)
		return
	}
	var inventory assetclient.RecordingPackageInventory
	if !decodeLimit(w, r, &inventory, 8<<20) {
		return
	}
	value, err := h.recordings.CreatePackage(r.Context(), r.PathValue("id"), inventory, key)
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	writeData(w, http.StatusCreated, value, nil)
}

func (h *Handler) adminRecordingPackageStatus(w http.ResponseWriter, r *http.Request) {
	if !h.recordingPackagesAvailable(w) {
		return
	}
	query := r.URL.Query()
	limit := 1000
	if len(query["cursor"]) > 1 || len(query["limit"]) > 1 {
		handleRecordingError(w, recordings.ErrInvalid)
		return
	}
	if query.Has("limit") {
		var err error
		limit, err = strconv.Atoi(query.Get("limit"))
		if err != nil {
			handleRecordingError(w, recordings.ErrInvalid)
			return
		}
	}
	value, err := h.recordings.PackageStatus(r.Context(), r.PathValue("id"), r.PathValue("packageID"), query.Get("cursor"), limit)
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	if !h.enqueueAdminAudit(w, r, "cms.recording.read", r.PathValue("id"), auditclient.Metadata{}) {
		return
	}
	writeData(w, http.StatusOK, value, nil)
}

func (h *Handler) adminSignRecordingPackage(w http.ResponseWriter, r *http.Request) {
	if !h.recordingPackagesAvailable(w) {
		return
	}
	var input struct {
		Paths []string `json:"paths"`
	}
	if !decode(w, r, &input) {
		return
	}
	value, err := h.recordings.SignPackage(r.Context(), r.PathValue("id"), r.PathValue("packageID"), input.Paths)
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	writeData(w, http.StatusOK, value, nil)
}

func (h *Handler) adminCompleteRecordingPackage(w http.ResponseWriter, r *http.Request) {
	if !h.recordingPackagesAvailable(w) {
		return
	}
	var input struct{}
	if !decode(w, r, &input) {
		return
	}
	value, err := h.recordings.CompletePackage(r.Context(), r.PathValue("id"), r.PathValue("packageID"))
	if err != nil {
		handleRecordingError(w, err)
		return
	}
	writeData(w, http.StatusAccepted, value, nil)
}
