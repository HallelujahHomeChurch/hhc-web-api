package assetclient

import (
	"encoding/json"
	"strings"
)

type ProcessingReadiness struct {
	Version int    `json:"version"`
	State   string `json:"state"`
}
type SafetyEvidence struct {
	Kind string `json:"kind"`
}

// Validate the optional contract at the shared decoder, before any legacy reader.
func (a *Asset) UnmarshalJSON(data []byte) error {
	type plain Asset
	var value plain
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	present := func(name string) bool {
		for key := range fields {
			if strings.EqualFold(key, name) {
				return true
			}
		}
		return false
	}
	if present("processingReadiness") {
		r := value.ProcessingReadiness
		if r == nil || r.Version != 1 || (r.State != "ready" && r.State != "pending" && r.State != "rejected") {
			return ErrUnavailable
		}
		legacyReady := value.UploadStatus == "completed" && value.ScanStatus == "clean" && (value.ProcessingStatus == "ready" || value.ProcessingStatus == "not_required")
		if (r.State == "ready") != legacyReady {
			return ErrUnavailable
		}
	}
	if present("safetyEvidence") {
		e := value.SafetyEvidence
		if e == nil || value.UploadStatus != "completed" || value.ScanStatus != "clean" || (e.Kind != "scanned" && e.Kind != "trusted-derived") {
			return ErrUnavailable
		}
		if (e.Kind == "trusted-derived") != (value.ScanSignature == "trusted-bulletin-watermark-v1") {
			return ErrUnavailable
		}
	}
	*a = Asset(value)
	return nil
}

func (a Asset) Scanned() bool {
	return a.ScanStatus == "clean" && a.ScanSignature != "trusted-bulletin-watermark-v1" && ((a.ProcessingReadiness == nil && a.SafetyEvidence == nil) || (a.SafetyEvidence != nil && a.SafetyEvidence.Kind == "scanned"))
}
