package httpapi

import (
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
	"net/http"
)

func (h *Handler) memberPlaybackHeartbeat(w http.ResponseWriter, r *http.Request) {
	if !h.analyticsRequest(w, r) {
		return
	}
	var input recordings.PlaybackHeartbeat
	if !broadcastDecode(w, r, &input, 2048) {
		return
	}
	if e := h.recordings.RecordPlaybackHeartbeat(r.Context(), r.PathValue("id"), actor(r), input); e != nil {
		if writeMemberCancellation(w, r, e) {
			return
		}
		interactionError(w, e)
		return
	}
	writeData(w, 200, map[string]any{"accepted": true, "metricVersion": recordings.MetricVersion, "heartbeatIntervalSeconds": 15}, nil)
}
func (h *Handler) recordingMetrics(admin bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.analyticsRequest(w, r) {
			return
		}
		v, e := h.recordings.GetPlaybackMetrics(r.Context(), r.PathValue("id"), actor(r), admin)
		if e != nil {
			if !admin && writeMemberCancellation(w, r, e) {
				return
			}
			interactionError(w, e)
			return
		}
		if admin {
			writeData(w, 200, v, nil)
		} else {
			writeData(w, 200, map[string]any{"currentViewers": v.CurrentViewers, "totalViews": v.TotalViews, "measuredAt": v.MeasuredAt, "trackingSince": v.TrackingSince, "metricVersion": v.MetricVersion, "status": v.Status}, nil)
		}
	}
}

func (h *Handler) analyticsRequest(w http.ResponseWriter, r *http.Request) bool {
	if !h.broadcastRequest(w, r, false) {
		return false
	}
	if !h.recordings.VideoAnalyticsEnabled() {
		writeError(w, 503, "recording_analytics_unavailable", "Analytics are unavailable.")
		return false
	}
	return true
}
