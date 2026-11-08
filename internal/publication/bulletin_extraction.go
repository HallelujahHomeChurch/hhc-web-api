package publication

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
)

type BulletinExtractionPayload struct {
	IssueID   string `json:"issueId"`
	AssetID   string `json:"assetId"`
	ActorID   string `json:"actorId"`
	RequestID string `json:"requestId"`
}

func (w *Worker) ensureBulletinExtraction(ctx context.Context, event Event) error {
	var payload BulletinExtractionPayload
	if json.Unmarshal(event.Payload, &payload) != nil || payload.IssueID != event.AggregateID || payload.AssetID == "" || !auditclient.ValidActorRequest(payload.ActorID, payload.RequestID) {
		return terminalError{fmt.Errorf("invalid bulletin extraction request")}
	}
	repository, ok := w.repository.(interface {
		CompleteOnlineExtractionRequest(context.Context, Event, string, time.Time) error
	})
	if !ok {
		return fmt.Errorf("bulletin extraction queue unavailable")
	}
	asset, err := w.assets.Get(ctx, payload.AssetID)
	if err != nil {
		return err
	}
	if asset.ID != payload.AssetID || asset.Namespace != "cms.weekly.pdf" || asset.OwnerService != "hhc-web-api" || asset.OwnerType != "bulletin_issue" || asset.OwnerID != payload.IssueID || asset.Locale != "zh-Hant" {
		return terminalError{fmt.Errorf("bulletin extraction asset owner mismatch")}
	}
	if err = readyAsset(asset); err != nil {
		return err
	}
	checksum, err := hex.DecodeString(asset.ChecksumSHA256)
	if asset.DetectedMIMEType != "application/pdf" || err != nil || len(checksum) != 32 || asset.ChecksumSHA256 != strings.ToLower(asset.ChecksumSHA256) {
		return terminalError{fmt.Errorf("invalid bulletin extraction source")}
	}
	return repository.CompleteOnlineExtractionRequest(ctx, event, asset.ChecksumSHA256, w.now().UTC())
}

func (w *Worker) rescheduleBulletinExtraction(ctx context.Context, event Event, cause error) error {
	if errors.Is(cause, ErrStalePublication) {
		return nil
	}
	repository, ok := w.repository.(interface {
		RescheduleOnlineExtractionRequest(context.Context, Event, string, time.Time, time.Time, bool) error
	})
	if !ok {
		return fmt.Errorf("bulletin extraction reschedule unavailable")
	}
	now := w.now().UTC()
	next := now.Add(time.Duration(1<<min(event.Attempts-1, 5)) * 5 * time.Second)
	code, waiting := "upload_request_failed", false
	var pending assetPendingError
	var terminal terminalError
	var permanent interface{ Permanent() bool }
	switch {
	case errors.As(cause, &pending):
		code = "upload_source_pending"
		if !event.CreatedAt.IsZero() && now.Sub(event.CreatedAt) >= assetReadinessDeadline {
			code, next = "upload_source_unavailable", time.Time{}
		} else {
			next, waiting = now.Add(10*time.Second), true
		}
	case errors.As(cause, &terminal) || errors.As(cause, &permanent) && permanent.Permanent():
		code, next = "upload_source_invalid", time.Time{}
	case event.Attempts >= w.maxAttempts:
		next = time.Time{}
	}
	return repository.RescheduleOnlineExtractionRequest(ctx, event, code, next, now, waiting)
}
