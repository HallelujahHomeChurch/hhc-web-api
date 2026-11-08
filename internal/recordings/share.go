package recordings

import (
	"context"
	_ "embed"
)

//go:embed assets/church-live-v1.jpg
var churchShareCover []byte

// SharePreview deliberately excludes member descriptions, media identifiers and grants.
type SharePreview struct {
	Title    string `json:"title"`
	PublicID string `json:"-"`
}

func (s *Service) shareRecording(ctx context.Context, id string) (Recording, error) {
	v, err := s.repo.Get(ctx, id)
	if err != nil {
		return Recording{}, err
	}
	if v.Hidden || v.Expired(s.now()) {
		return Recording{}, ErrNotFound
	}
	if v.Broadcast {
		repo, err := s.broadcastRepository()
		if err != nil {
			return Recording{}, err
		}
		b, err := repo.GetBroadcast(ctx, id)
		if err != nil {
			return Recording{}, err
		}
		if !b.View.Announced || b.Revoked || b.CancelledAt != nil {
			return Recording{}, ErrNotFound
		}
		switch b.View.Phase {
		case "scheduled", "preview", "start_pending", "live", "end_pending", "processing":
			return v, nil
		case "archived":
			if v.Visible(s.now()) {
				return v, nil
			}
		}
		return Recording{}, ErrNotFound
	}
	if !v.Visible(s.now()) {
		return Recording{}, ErrNotFound
	}
	return v, nil
}

func (s *Service) SharePreview(ctx context.Context, id string) (SharePreview, error) {
	v, err := s.shareRecording(ctx, id)
	if err != nil {
		return SharePreview{}, err
	}
	return SharePreview{Title: v.Title, PublicID: v.PublicID}, nil
}

func (s *Service) ShareCover(ctx context.Context, id string) ([]byte, error) {
	v, err := s.shareRecording(ctx, id)
	if err != nil {
		return nil, err
	}
	var data []byte
	var coverSnapshot string
	if v.Broadcast && !v.Visible(s.now()) {
		repo, err := s.broadcastRepository()
		if err != nil {
			return nil, err
		}
		b, err := repo.GetBroadcast(ctx, id)
		if err != nil {
			return nil, err
		}
		coverSnapshot = broadcastShareCoverKey(b)
		cover := b.CoverSnapshot
		switch cover.Settings.Selection.Mode {
		case "auto":
			data = churchShareCover
		case "custom":
			if s.packages == nil || cover.Settings.CoverState != "ready" || cover.Settings.CoverID == "" {
				return nil, ErrNotFound
			}
			scope, owner := cover.Scope, id
			if scope == "" && b.View.Binding != nil {
				scope = b.View.Binding.CaptureID
			}
			if scope == "defaults" {
				owner = ""
			}
			data, err = s.packages.ReadLiveCover(ctx, scope, owner, cover.Settings.CoverID)
			if err != nil {
				return nil, err
			}
		default:
			return nil, ErrNotFound
		}
	} else {
		if s.packages == nil {
			return nil, ErrNotFound
		}
		data, err = s.selectedCoverContent(ctx, v)
		if err != nil {
			return nil, err
		}
	}
	// Image I/O can race with revocation or a new cover selection.
	current, err := s.shareRecording(ctx, id)
	if err != nil {
		return nil, err
	}
	if current.PackageID != v.PackageID || current.SelectedCoverID != v.SelectedCoverID || current.Broadcast != v.Broadcast {
		return nil, ErrNotFound
	}
	if coverSnapshot != "" {
		repo, err := s.broadcastRepository()
		if err != nil {
			return nil, err
		}
		b, err := repo.GetBroadcast(ctx, id)
		if err != nil {
			return nil, err
		}
		if broadcastShareCoverKey(b) != coverSnapshot {
			return nil, ErrNotFound
		}
	}
	return data, nil
}

func broadcastShareCoverKey(b Broadcast) string {
	capture := ""
	if b.View.Binding != nil {
		capture = b.View.Binding.CaptureID
	}
	return captureDigest(struct {
		Epoch                                int64
		Capture, Scope, Mode, CoverID, State string
	}{b.Epoch, capture, b.CoverSnapshot.Scope, b.CoverSnapshot.Settings.Selection.Mode, b.CoverSnapshot.Settings.CoverID, b.CoverSnapshot.Settings.CoverState})
}
