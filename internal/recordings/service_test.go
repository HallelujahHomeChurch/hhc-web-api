package recordings

import (
	"context"
	"errors"
	"testing"
	"time"
)

type testRepo struct {
	Repository
	recording    Recording
	createdTitle string
	completions  int
}

type replayPublishRepo struct {
	Repository
	result PublishResult
}

func (r replayPublishRepo) GetPublishOperation(context.Context, string, int64, string, string, time.Time) (PublishResult, error) {
	return r.result, nil
}

func TestPublishReplayReturnsCurrentStateWithoutRevalidatingOrRepublishing(t *testing.T) {
	expected := PublishResult{Receipt: PublishReceipt{OperationKey: "operation", PublishedVersion: 2}, Current: Recording{Status: "draft", Version: 3}, Outcome: "state_changed"}
	service := NewService(replayPublishRepo{result: expected}, "", nil, time.Now)
	result, err := service.PublishOperation(context.Background(), "rec-a", 1, "actor", "operation")
	if err != nil || result != expected {
		t.Fatalf("replay: %+v %v", result, err)
	}
	if _, err := service.PublishOperation(context.Background(), "rec-a", 1, "actor", "bad key"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe key: %v", err)
	}
}

func (r *testRepo) Create(_ context.Context, title, _, _ string, _ time.Time) (Recording, error) {
	r.createdTitle = title
	return r.recording, nil
}
func (r *testRepo) Get(context.Context, string) (Recording, error) { return r.recording, nil }
func (r *testRepo) ListVisible(context.Context, time.Time) ([]Recording, error) {
	return []Recording{r.recording}, nil
}
func (r *testRepo) ListPendingCompletions(context.Context) ([]Recording, error) {
	return []Recording{r.recording}, nil
}
func (r *testRepo) RecordUploadCompletion(_ context.Context, _, _ string, at time.Time) (Recording, error) {
	r.completions++
	expiry := at.Add(720 * time.Hour)
	r.recording.UploadedAt = &at
	r.recording.ExpiresAt = &expiry
	return r.recording, nil
}
func (r *testRepo) Publish(context.Context, string, int64, string, time.Time) (Recording, error) {
	r.recording.Status = "published"
	return r.recording, nil
}
func (r *testRepo) RemoveFile(context.Context, string, string) error { return nil }

func TestRecordingCreateRequiresTitle(t *testing.T) {
	repo := &testRepo{}
	service := NewService(repo, "", nil, time.Now)
	if _, err := service.Create(context.Background(), " ", "admin", "key"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty title accepted: %v", err)
	}
}
func TestRecordingCreateDoesNotRequireOccurrence(t *testing.T) {
	repo := &testRepo{recording: Recording{ID: "rec-a"}}
	service := NewService(repo, "", nil, time.Now)
	if _, err := service.Create(context.Background(), " 主日聚會 ", "admin", "key"); err != nil || repo.createdTitle != "主日聚會" {
		t.Fatalf("title-only recording rejected: %v", err)
	}
}
func TestLegacyRecordingCannotPublishListNotifyOrPlay(t *testing.T) {
	now := time.Now().UTC()
	expiry := now.Add(time.Hour)
	repo := &testRepo{recording: Recording{ID: "rec-a", Title: "聚會", Version: 1, Status: "published", PrimaryAssetVersionID: "file", UploadedAt: &now, ExpiresAt: &expiry}}
	svc := NewService(repo, "https://media.example.test", make([]byte, 32), func() time.Time { return now })
	ctx := context.Background()
	if _, err := svc.Playback(ctx, "rec-a", "scope", "", "member"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy playback: %v", err)
	}
	if _, err := svc.Publish(ctx, "rec-a", 1, "admin"); !errors.Is(err, ErrConflict) {
		t.Fatalf("legacy publication: %v", err)
	}
	if values, err := svc.List(ctx); err != nil || len(values) != 0 {
		t.Fatalf("legacy list: %v %v", values, err)
	}
	if available, err := svc.NotificationAvailable(ctx, "rec-a"); err != nil || available {
		t.Fatalf("legacy notification: %v %v", available, err)
	}
}
