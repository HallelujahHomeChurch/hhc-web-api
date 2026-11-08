package recordings

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

type InboxSnapshot struct {
	Recording Recording
	Broadcast *Broadcast
	Capture   *Capture
}
type inboxRepository interface {
	InboxRecordings(context.Context, []string) ([]InboxSnapshot, error)
}

// Reads current owner snapshots without playback credentials or watermark writes.
func (s *Service) InboxVisible(ctx context.Context, ids []string) (map[string]string, error) {
	repo, ok := s.repo.(inboxRepository)
	if !ok || s.packages == nil {
		return nil, assetclient.ErrUnavailable
	}
	snapshots, err := repo.InboxRecordings(ctx, ids)
	if err != nil {
		return nil, err
	}
	bindings := []assetclient.RecordingLifecycleBinding{}
	liveRefs := []assetclient.WebsiteContentRef{}
	for _, v := range snapshots {
		r := v.Recording
		if r.MediaTerminal {
			continue
		}
		// Asset can extend a formerly expired package; filter only after its current snapshot.
		if r.Status == "published" && !r.Hidden && r.ReadyAt != nil && r.PackageID != "" {
			bindings = append(bindings, assetclient.RecordingLifecycleBinding{RecordingID: r.ID, PackageID: r.PackageID})
		}
		if v.Capture != nil && v.Capture.Status.CaptureID != "" && v.Capture.LiveAvailable(s.now()) && v.Capture.Status.TerminalAt == nil && !r.Expired(s.now()) {
			liveRefs = append(liveRefs, assetclient.WebsiteContentRef{Type: "recording_live", ID: v.Capture.Status.CaptureID, ResourceID: r.ID})
		}
	}
	packages := map[string]assetclient.RecordingPackageStatus{}
	if len(bindings) > 0 {
		snapshot, err := s.packages.RecordingLifecycle(ctx, bindings)
		if err != nil {
			return nil, err
		}
		if err = validateLifecycleSnapshot(snapshot, bindings, nil, s.now()); err != nil {
			return nil, err
		}
		for _, p := range snapshot.Items {
			packages[p.PackageID] = p
		}
	}
	live := map[assetclient.WebsiteContentRef]assetclient.WebsiteContentDecision{}
	if len(liveRefs) > 0 {
		decisions, err := s.packages.WebsiteContentReadiness(ctx, liveRefs)
		if err != nil {
			return nil, err
		}
		for _, d := range decisions {
			live[d.Ref] = d
		}
	}
	// A remote check cannot authorize a binding or publication withdrawn while it was in flight.
	snapshots, err = repo.InboxRecordings(ctx, ids)
	if err != nil {
		return nil, err
	}
	titles := map[string]string{}
	for _, v := range snapshots {
		r := v.Recording
		if r.MediaTerminal {
			continue
		}
		packageReady := false
		if p, ok := packages[r.PackageID]; ok && p.RecordingID == r.ID && p.State == "ready" && s.validReadyPackage(r.ID, r.PackageID, p) {
			r.UploadedAt = p.UploadedAt
			r.ReadyAt = p.ReadyAt
			r.ExpiresAt = p.MediaExpiresAt
			packageReady = p.MediaExpiresAt != nil && s.now().Before(*p.MediaExpiresAt)
		}
		if r.Expired(s.now()) {
			continue
		}
		vod := r.Visible(s.now()) && packageReady
		if v.Broadcast != nil {
			vod = packageReady && v.Broadcast.InboxVODReady(r, s.now())
		}
		liveReady := false
		if v.Capture != nil && v.Capture.LiveAvailable(s.now()) && v.Capture.Status.TerminalAt == nil {
			c := *v.Capture
			ref := assetclient.WebsiteContentRef{Type: "recording_live", ID: c.Status.CaptureID, ResourceID: r.ID}
			d, ok := live[ref]
			if ok && d.Ready {
				if c.RecordingID != r.ID || c.Status.RecordingID != r.ID {
					return nil, ErrConflict
				}
				c.Status.State = d.Live.State
				if d.Live.ExpiresAt.Before(c.Status.ExpiresAt) {
					c.Status.ExpiresAt = d.Live.ExpiresAt
				}
				c.Status.TerminalAt = d.Live.TerminalAt
				c.Status.Progress = d.Live.Progress
				c.DeriveLiveState(s.now())
				liveReady = c.Status.TerminalAt == nil && c.LiveAvailable(s.now()) && (c.Status.LiveState == "live" || c.Status.LiveState == "ending" || c.Status.LiveState == "ended")
				if v.Broadcast != nil {
					b := v.Broadcast
					liveReady = liveReady && b.View.Binding != nil && b.View.Binding.CaptureID == ref.ID && b.CaptureIntentID == c.IntentID && b.InboxLiveReady(c, s.now())
				}
			}
		}
		if vod || liveReady {
			titles[r.ID] = r.Title
		}
	}
	return titles, nil
}
