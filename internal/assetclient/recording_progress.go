package assetclient

import "time"

// Advisory summary from Asset's canonical contract, never a readiness signal.
type RecordingProcessingProgress struct {
	Attempt          int       `json:"attempt"`
	Phase            string    `json:"phase"`
	Rendition        string    `json:"rendition,omitempty"`
	ObjectsVerified  *int64    `json:"objectsVerified,omitempty"`
	ObjectsTotal     *int64    `json:"objectsTotal,omitempty"`
	SegmentsVerified *int64    `json:"segmentsVerified,omitempty"`
	SegmentsTotal    *int64    `json:"segmentsTotal,omitempty"`
	BytesVerified    *int64    `json:"bytesVerified,omitempty"`
	BytesTotal       *int64    `json:"bytesTotal,omitempty"`
	AttemptStartedAt time.Time `json:"attemptStartedAt"`
	PhaseStartedAt   time.Time `json:"phaseStartedAt"`
	LastProgressAt   time.Time `json:"lastProgressAt"`
	HeartbeatAt      time.Time `json:"heartbeatAt"`
}

func (p *RecordingProcessingProgress) Valid() bool {
	if p == nil {
		return true
	}
	if p.Attempt < 0 || p.Attempt > 3 || (p.Attempt == 0 && p.Phase != "queued") {
		return false
	}
	switch p.Phase {
	case "queued", "source_finalization", "encoding", "package_validation", "package_finalization":
	default:
		return false
	}
	switch p.Rendition {
	case "", "480p", "720p", "1080p":
	default:
		return false
	}
	for _, pair := range [][2]*int64{{p.ObjectsVerified, p.ObjectsTotal}, {p.SegmentsVerified, p.SegmentsTotal}, {p.BytesVerified, p.BytesTotal}} {
		if pair[0] != nil && *pair[0] < 0 || pair[1] != nil && *pair[1] < 0 || pair[0] != nil && pair[1] != nil && *pair[0] > *pair[1] {
			return false
		}
	}
	return !p.AttemptStartedAt.IsZero() && !p.PhaseStartedAt.Before(p.AttemptStartedAt) && !p.LastProgressAt.Before(p.PhaseStartedAt) && !p.HeartbeatAt.Before(p.LastProgressAt)
}
