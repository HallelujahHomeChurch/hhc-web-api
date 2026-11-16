package bulletins

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/onlinebulletins"
	"github.com/google/uuid"
)

var (
	ErrDeletePublished       = errors.New("published bulletin cannot be deleted")
	ErrDeleteCleanupPending  = errors.New("bulletin withdrawal or cleanup is pending")
	ErrDeleteRetainedHistory = errors.New("published bulletin history must be retained")
)

type DeletionEligibility struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason,omitempty"`
}

type PublicationChannels struct {
	PDF         bool `json:"pdf"`
	Traditional bool `json:"traditional"`
	Simplified  bool `json:"simplified"`
}

type PublicationOutcome struct {
	PendingMetadata *PublicationMetadata `json:"pendingMetadata,omitempty"`
	Status          string               `json:"status"`
	Reason          string               `json:"reason,omitempty"`
	NextAction      string               `json:"nextAction,omitempty"`
	Availability    PublicationChannels  `json:"availability"`
	Current         PublicationChannels  `json:"current"`
}

type PublicationMetadata struct {
	IssueNumber int    `json:"issueNumber"`
	IssueDate   string `json:"issueDate"`
	Title       string `json:"title"`
	Subtitle    string `json:"subtitle"`
}

type PublicationUpdateInput struct {
	Metadata *PublicationMetadata `json:"metadata,omitempty"`
}

type PublicationUpdateResult struct {
	Issue   Issue              `json:"issue"`
	Outcome PublicationOutcome `json:"outcome"`
}

type publicationProductRepository interface {
	GetIssueWithPublicationOutcome(context.Context, string) (Issue, error)
	UpdateBulletinPublication(context.Context, string, int64, PublicationUpdateInput, string, string, time.Time) (PublicationUpdateResult, error)
}

func (s *Service) GetIssueWithPublicationOutcome(ctx context.Context, id string) (Issue, error) {
	if strings.TrimSpace(id) == "" {
		return Issue{}, ErrInvalid
	}
	if repo, ok := s.repository.(publicationProductRepository); ok {
		return repo.GetIssueWithPublicationOutcome(ctx, id)
	}
	return s.GetIssue(ctx, id)
}

func (s *Service) UpdatePublication(ctx context.Context, id string, expected int64, input PublicationUpdateInput, key, actor string) (PublicationUpdateResult, error) {
	if uuid.Validate(id) != nil || expected < 1 || !onlinebulletins.ValidSimplifiedRecoveryKey(key) || actor == "" {
		return PublicationUpdateResult{}, ErrInvalid
	}
	if input.Metadata != nil {
		metadata := *input.Metadata
		metadata.Title, metadata.Subtitle = strings.TrimSpace(metadata.Title), strings.TrimSpace(metadata.Subtitle)
		if metadata.IssueNumber < 1 || metadata.IssueNumber > 2147483647 || !validDate(metadata.IssueDate) || metadata.Title == "" || utf8.RuneCountInString(metadata.Title) > 200 || utf8.RuneCountInString(metadata.Subtitle) > 300 {
			return PublicationUpdateResult{}, ErrInvalid
		}
		input.Metadata = &metadata
	}
	repo, ok := s.repository.(publicationProductRepository)
	if !ok {
		return PublicationUpdateResult{}, ErrConflict
	}
	return repo.UpdateBulletinPublication(ctx, id, expected, input, key, actor, s.now().UTC())
}

// UploadCompletionCommand binds the public completion request to its durable
// attachment. The legacy unkeyed completion path remains supported.
type UploadCompletionCommand struct {
	Key       string
	InputHash [32]byte
}

func (s *Service) ReplayUploadCompletion(ctx context.Context, issue, actor string, command UploadCompletionCommand) (Issue, bool, error) {
	if command.Key == "" {
		return Issue{}, false, nil
	}
	if !onlinebulletins.ValidSimplifiedRecoveryKey(command.Key) || uuid.Validate(issue) != nil {
		return Issue{}, false, ErrInvalid
	}
	repo, ok := s.repository.(interface {
		ReplayBulletinUploadCompletion(context.Context, string, string, UploadCompletionCommand) (Issue, bool, error)
	})
	if !ok {
		return Issue{}, false, nil
	}
	return repo.ReplayBulletinUploadCompletion(ctx, issue, actor, command)
}

func (s *Service) TrackBulletinUploadAsset(ctx context.Context, issue string, edition BulletinEdition, asset, actor string) error {
	repo, ok := s.repository.(interface {
		TrackBulletinUploadAsset(context.Context, string, BulletinEdition, string, string, time.Time) error
	})
	if !ok {
		return nil
	}
	if uuid.Validate(issue) != nil || asset == "" || len(asset) > 255 {
		return ErrInvalid
	}
	return repo.TrackBulletinUploadAsset(ctx, issue, edition, asset, actor, s.now().UTC())
}
