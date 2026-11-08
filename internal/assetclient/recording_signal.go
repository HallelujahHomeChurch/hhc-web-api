package assetclient

import (
	"context"
	"net/http"
	"time"
)

type BroadcastSignal struct {
	RecoveryVersion                     string     `json:"recoveryVersion"`
	LastCommonConfirmedAt               *time.Time `json:"lastCommonConfirmedAt"`
	CommonConfirmedEndSequenceExclusive *int       `json:"commonConfirmedEndSequenceExclusive"`
	Reason                              string     `json:"reason"`
	State                               string     `json:"state"`
	LastMediaConfirmedAt                *time.Time `json:"lastMediaConfirmedAt"`
	Deadline                            *time.Time `json:"deadline"`
	EndSequenceExclusive                *int       `json:"endSequenceExclusive"`
}

func (c *Client) ReconcileRecordingBroadcastSignal(ctx context.Context, capture string, closeIfIdle bool) (BroadcastSignal, error) {
	var out BroadcastSignal
	if !recordingPackageID.MatchString(capture) {
		return out, ErrInvalid
	}
	err := c.request(ctx, http.MethodPost, "/priv/recording-captures/"+capture+"/broadcast-signal", struct {
		CloseIfIdle bool `json:"closeIfIdle"`
	}{closeIfIdle}, "", &out)
	return out, err
}
