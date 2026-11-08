package recordings

import (
	"context"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"strings"
	"time"
	"unicode/utf8"
)

type StudioSource struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	BroadcastSource
	DirectEnabled bool `json:"directEnabled"`
}
type StudioSourceInput struct {
	SealedKey SealedStreamKey `json:"-"`
	BroadcastMutationInput
	Name      string `json:"name,omitempty"`
	StreamKey string `json:"streamKey,omitempty"`
}
type StudioRepository interface {
	ListStudioSources(context.Context) ([]StudioSource, error)
	MutateStudioSource(context.Context, string, string, string, StudioSourceInput, string, string, time.Time) (StudioSource, error)
	SetStudioSourceTarget(context.Context, string, string, BroadcastSourceTargetInput, time.Time) (BroadcastSource, error)
}

func (s *Service) studioRepository() (StudioRepository, error) {
	r, ok := s.repo.(StudioRepository)
	if !ok {
		return nil, assetclient.ErrUnavailable
	}
	return r, nil
}
func (s *Service) ListStudioSources(ctx context.Context) ([]StudioSource, error) {
	r, e := s.studioRepository()
	if e != nil {
		return nil, e
	}
	return r.ListStudioSources(ctx)
}
func (s *Service) MutateStudioSource(ctx context.Context, id, actor, kind string, in StudioSourceInput) (StudioSource, error) {
	var empty StudioSource
	if !s.studioWriters || !s.broadcastWriters {
		return empty, assetclient.ErrUnavailable
	}
	if e := broadcastActor(ctx, actor); e != nil {
		return empty, e
	}
	if !ValidOperationKey(in.OperationKey) || kind != "create" && (!validLiveUUID(id) || in.ExpectedRevision < 1) {
		return empty, ErrInvalid
	}
	var hash, suffix string
	var e error
	switch kind {
	case "create", "rename":
		in.Name = strings.TrimSpace(in.Name)
		if !utf8.ValidString(in.Name) || utf8.RuneCountInString(in.Name) < 1 || utf8.RuneCountInString(in.Name) > 100 {
			return empty, ErrInvalid
		}
	case "key", "delete":
	default:
		return empty, ErrInvalid
	}
	if kind == "create" || kind == "key" {
		hash, suffix, e = HashBroadcastSourceKey(in.StreamKey)
		if e != nil {
			return empty, e
		}
	} else if in.StreamKey != "" {
		return empty, ErrInvalid
	}
	if (kind == "create" || kind == "key") && s.streamKeyCipher != nil {
		in.SealedKey, e = s.streamKeyCipher.Encrypt(hash, in.StreamKey)
		if e != nil {
			return empty, e
		}
	}
	in.StreamKey = ""
	r, e := s.studioRepository()
	if e != nil {
		return empty, e
	}
	return r.MutateStudioSource(ctx, id, actor, kind, in, hash, suffix, s.now().UTC())
}
func (s *Service) PrepareStudioSource(ctx context.Context, id, actor string, in BroadcastSourceTargetInput) (BroadcastSource, error) {
	if !s.studioWriters || !s.broadcastWriters {
		return BroadcastSource{}, assetclient.ErrUnavailable
	}
	if !validLiveUUID(id) || !validLiveUUID(in.RecordingID) || !ValidOperationKey(in.OperationKey) || in.ExpectedRevision < 1 || in.ExpectedSourceRevision < 1 {
		return BroadcastSource{}, ErrInvalid
	}
	if e := broadcastActor(ctx, actor); e != nil {
		return BroadcastSource{}, e
	}
	if e := s.ingestAuthorizer(ctx, actor); e != nil {
		return BroadcastSource{}, e
	}
	r, e := s.studioRepository()
	if e != nil {
		return BroadcastSource{}, e
	}
	return r.SetStudioSourceTarget(ctx, id, actor, in, s.now().UTC())
}

type StudioSignalObservation struct {
	Phase      string    `json:"phase"`
	Signal     string    `json:"signal"`
	ObservedAt time.Time `json:"observedAt"`
}
type StudioBroadcast struct {
	DurationSeconds *float64                  `json:"durationSeconds,omitempty"`
	SignalHistory   []StudioSignalObservation `json:"signalHistory,omitempty"`
	Broadcast       BroadcastView             `json:"broadcast"`
	SourceID        *string                   `json:"sourceId"`
	Mode            string                    `json:"mode"`
}
type StudioScheduleInput struct {
	OperationKey string              `json:"operationKey"`
	SourceID     string              `json:"sourceId"`
	Title        string              `json:"title"`
	Description  string              `json:"description"`
	ScheduledAt  *time.Time          `json:"scheduledAt"`
	Cover        *LiveCoverSelection `json:"cover,omitempty"`
}
type StudioScheduleRepository interface {
	CreateStudioSchedule(context.Context, string, StudioScheduleInput, time.Time) (StudioBroadcast, error)
	GetStudioBroadcast(context.Context, string) (StudioBroadcast, error)
}

func (s *Service) CreateStudioSchedule(ctx context.Context, actor string, in StudioScheduleInput) (StudioBroadcast, error) {
	var empty StudioBroadcast
	if !s.studioWriters || !s.broadcastWriters {
		return empty, assetclient.ErrUnavailable
	}
	in.Title = strings.TrimSpace(in.Title)
	if !ValidOperationKey(in.OperationKey) || !validLiveUUID(in.SourceID) || !ValidTitle(in.Title) || !ValidDescription(in.Description) || in.ScheduledAt == nil || in.Cover != nil && !in.Cover.Valid() {
		return empty, ErrInvalid
	}
	if e := broadcastActor(ctx, actor); e != nil {
		return empty, e
	}
	if e := s.ingestAuthorizer(ctx, actor); e != nil {
		return empty, e
	}
	r, ok := s.repo.(StudioScheduleRepository)
	if !ok {
		return empty, assetclient.ErrUnavailable
	}
	if in.Cover != nil && in.Cover.Mode == "custom" {
		if s.packages == nil {
			return empty, assetclient.ErrUnavailable
		}
		if _, e := s.packages.GetLiveCoverUpload(ctx, "defaults", "", in.Cover.UploadID); e != nil {
			return empty, e
		}
	}
	return r.CreateStudioSchedule(ctx, actor, in, s.now().UTC())
}
func (s *Service) GetStudioBroadcast(ctx context.Context, id string) (StudioBroadcast, error) {
	if !validLiveUUID(id) {
		return StudioBroadcast{}, ErrNotFound
	}
	r, ok := s.repo.(StudioScheduleRepository)
	if !ok {
		return StudioBroadcast{}, assetclient.ErrUnavailable
	}
	return r.GetStudioBroadcast(ctx, id)
}

// Studio uploads are reusable across event deletion; legacy ownership is unchanged.
func (s *Service) broadcastCoverScope(ctx context.Context, id string) (string, string, bool, error) {
	if r, ok := s.repo.(StudioScheduleRepository); ok {
		v, e := r.GetStudioBroadcast(ctx, id)
		if e != nil {
			return "", "", false, e
		}
		if v.Mode == "direct" || v.Mode == "scheduled" {
			return "defaults", "", true, nil
		}
	}
	return "broadcast-" + id, id, false, nil
}

type StudioSourceAssignment struct {
	BroadcastMutationInput
	SourceID               string `json:"sourceId"`
	ExpectedSourceRevision int64  `json:"expectedSourceRevision"`
}
type studioAssignmentRepository interface {
	AssignStudioSource(context.Context, string, string, StudioSourceAssignment, time.Time) (StudioBroadcast, error)
}

func (s *Service) AssignStudioSource(ctx context.Context, id, actor string, in StudioSourceAssignment) (StudioBroadcast, error) {
	var empty StudioBroadcast
	if !s.studioWriters || !s.broadcastWriters {
		return empty, assetclient.ErrUnavailable
	}
	if !validLiveUUID(id) || !validLiveUUID(in.SourceID) || !ValidOperationKey(in.OperationKey) || in.ExpectedRevision < 1 || in.ExpectedSourceRevision < 1 {
		return empty, ErrInvalid
	}
	if e := broadcastActor(ctx, actor); e != nil {
		return empty, e
	}
	if e := s.ingestAuthorizer(ctx, actor); e != nil {
		return empty, e
	}
	r, ok := s.repo.(studioAssignmentRepository)
	if !ok {
		return empty, assetclient.ErrUnavailable
	}
	return r.AssignStudioSource(ctx, id, actor, in, s.now().UTC())
}
