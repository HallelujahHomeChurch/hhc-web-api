package recordings

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
)

var ErrCommentReadTimeout = errors.New("recording comment read timeout")

var ErrCommentTraversalLimit = errors.New("recording comment traversal limit")

var ErrCommentCursorExpired = errors.New("recording comment cursor expired")

var ErrCommentsClosed = errors.New("recording comments closed")
var ErrCommentForbidden = errors.New("recording comment forbidden")

type ReplyTarget struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	AuthorID    string `json:"-"`
}
type CommentReaction struct {
	Likes    int64   `json:"likes"`
	Reaction *string `json:"reaction"`
}
type Comment struct {
	AvatarURL string `json:"avatarUrl,omitempty"`
	CommentReaction
	HasChildren          bool                 `json:"hasChildren,omitempty"`
	ThreadParentID       string               `json:"threadParentId,omitempty"`
	DirectChildCount     *int                 `json:"directChildCount,omitempty"`
	DescendantReplyCount *int                 `json:"descendantReplyCount,omitempty"`
	ReplyPreviewAuthors  []ReplyPreviewAuthor `json:"replyPreviewAuthors,omitempty"`
	ReplyTo              *ReplyTarget         `json:"replyTo,omitempty"`
	ID                   string               `json:"id"`
	ParentID             string               `json:"parentId,omitempty"`
	Text                 string               `json:"text"`
	DisplayName          string               `json:"displayName"`
	AuthorID             string               `json:"-"`
	Mine                 bool                 `json:"mine"`
	Deleted              bool                 `json:"deleted"`
	Hidden               bool                 `json:"hidden"`
	Revision             int64                `json:"revision"`
	CreatedAt            time.Time            `json:"createdAt"`
	EditedAt             *time.Time           `json:"editedAt,omitempty"`
	ReplyCount           int                  `json:"replyCount"`
}

type ReplyPreviewAuthor struct {
	DisplayName string `json:"displayName"`
	AvatarURL   string `json:"avatarUrl,omitempty"`
	AuthorID    string `json:"-"`
}

type ThreadedCommentRepository interface {
	ListThreadedRecordingComments(context.Context, string, string, string, string, string, int, bool, time.Time) (CommentPage, error)
}

func (s *Service) ListThreadedRecordingComments(ctx context.Context, id, user, parent, cursor, order string, limit int, refresh bool) (CommentPage, error) {
	if limit < 1 || limit > 50 || len(cursor) > 128 || parent != "" && !validLiveUUID(parent) || order != "top" && order != "newest" && order != "oldest" || parent != "" && order == "top" || refresh && (parent != "" || order != "top" || cursor != "") || parent != "" && cursor != "" && !validLiveUUID(cursor) {
		return CommentPage{}, ErrInvalid
	}
	r, ok := s.repo.(ThreadedCommentRepository)
	if !ok {
		return CommentPage{}, assetclient.ErrUnavailable
	}
	return r.ListThreadedRecordingComments(ctx, id, user, parent, cursor, order, limit, refresh, s.now())
}

type CommentInput struct {
	ReplyToID        string `json:"replyToId,omitempty"`
	ClientMutationID string `json:"clientMutationId"`
	ParentID         string `json:"parentId,omitempty"`
	Text             string `json:"text"`
}

func (v CommentInput) Validate() error {
	if v.ReplyToID != "" && (v.ParentID == "" || !validLiveUUID(v.ReplyToID)) || !validLiveUUID(v.ClientMutationID) || v.ParentID != "" && !validLiveUUID(v.ParentID) || strings.TrimSpace(v.Text) == "" || utf8.RuneCountInString(v.Text) > 2000 || strings.ContainsRune(v.Text, 0) {
		return ErrInvalid
	}
	return nil
}

type CommentPage struct {
	Items      []Comment `json:"items"`
	NextCursor string    `json:"nextCursor,omitempty"`
}
type InteractionSummary struct {
	CommentCount    int64 `json:"commentCount"`
	Liked           bool  `json:"liked"`
	Likes           int64 `json:"likes"`
	CommentsEnabled bool  `json:"commentsEnabled"`
}
type InteractionRepository interface {
	RecordingInteractions(context.Context, string, string, time.Time) (InteractionSummary, error)
	SetRecordingLike(context.Context, string, string, bool, time.Time) (InteractionSummary, error)
	CreateRecordingComment(context.Context, string, string, CommentInput, time.Time) (Comment, error)
	ListRecordingComments(context.Context, string, string, string, string, string, int, time.Time) (CommentPage, error)
	UpdateRecordingComment(context.Context, string, string, string, string, int64, bool, time.Time) (Comment, error)
}

func (s *Service) interactionRepository() (InteractionRepository, error) {
	r, ok := s.repo.(InteractionRepository)
	if !ok {
		return nil, assetclient.ErrUnavailable
	}
	return r, nil
}
func (s *Service) RecordingInteractions(ctx context.Context, id, user string) (InteractionSummary, error) {
	r, e := s.interactionRepository()
	if e != nil {
		return InteractionSummary{}, e
	}
	return r.RecordingInteractions(ctx, id, user, s.now())
}
func (s *Service) SetRecordingLike(ctx context.Context, id, user string, liked bool) (InteractionSummary, error) {
	r, e := s.interactionRepository()
	if e != nil {
		return InteractionSummary{}, e
	}
	return r.SetRecordingLike(ctx, id, user, liked, s.now())
}
func (s *Service) CreateRecordingComment(ctx context.Context, id, user string, input CommentInput) (Comment, error) {
	if e := input.Validate(); e != nil {
		return Comment{}, e
	}
	r, e := s.interactionRepository()
	if e != nil {
		return Comment{}, e
	}
	return r.CreateRecordingComment(ctx, id, user, input, s.now())
}
func (s *Service) ListRecordingComments(ctx context.Context, id, user, parent, cursor, order string, limit int) (CommentPage, error) {
	if limit < 1 || limit > 50 || order != "newest" && order != "oldest" || parent != "" && !validLiveUUID(parent) || cursor != "" && !validLiveUUID(cursor) {
		return CommentPage{}, ErrInvalid
	}
	r, e := s.interactionRepository()
	if e != nil {
		return CommentPage{}, e
	}
	return r.ListRecordingComments(ctx, id, user, parent, cursor, order, limit, s.now())
}
func (s *Service) UpdateRecordingComment(ctx context.Context, id, user, comment, text string, revision int64, remove bool) (Comment, error) {
	if !validLiveUUID(comment) || revision < 1 || !remove && (strings.TrimSpace(text) == "" || utf8.RuneCountInString(text) > 2000 || strings.ContainsRune(text, 0)) {
		return Comment{}, ErrInvalid
	}
	r, e := s.interactionRepository()
	if e != nil {
		return Comment{}, e
	}
	return r.UpdateRecordingComment(ctx, id, user, comment, text, revision, remove, s.now())
}

type CommentReportInput struct {
	Reason string `json:"reason"`
}
type ModerationInput struct {
	Operation        string `json:"operation"`
	CommentID        string `json:"commentId,omitempty"`
	ExpectedRevision int64  `json:"expectedRevision"`
	Enabled          *bool  `json:"enabled,omitempty"`
}
type ModerationComment struct {
	Comment Comment  `json:"comment"`
	Reports int      `json:"reports"`
	Reasons []string `json:"reasons"`
}
type ModerationPage struct {
	Items           []ModerationComment `json:"items"`
	NextCursor      string              `json:"nextCursor,omitempty"`
	CommentsEnabled bool                `json:"commentsEnabled"`
	Revision        int64               `json:"revision"`
}
type ModerationRepository interface {
	ReportRecordingComment(context.Context, string, string, string, string, time.Time) error
	ModerateRecordingComments(context.Context, string, string, ModerationInput, time.Time) error
	ListRecordingModeration(context.Context, string, string, int) (ModerationPage, error)
}

func (s *Service) ReportRecordingComment(ctx context.Context, id, user, comment, reason string) error {
	if !validLiveUUID(comment) || strings.TrimSpace(reason) == "" || utf8.RuneCountInString(reason) > 500 || strings.ContainsRune(reason, 0) {
		return ErrInvalid
	}
	r, ok := s.repo.(ModerationRepository)
	if !ok {
		return assetclient.ErrUnavailable
	}
	return r.ReportRecordingComment(ctx, id, user, comment, reason, s.now())
}
func (s *Service) ModerateRecordingComments(ctx context.Context, id, actor string, input ModerationInput) error {
	if !validLiveUUID(id) || input.ExpectedRevision < 1 {
		return ErrInvalid
	}
	switch input.Operation {
	case "settings":
		if input.Enabled == nil || input.CommentID != "" {
			return ErrInvalid
		}
	case "hide", "restore", "resolve":
		if !validLiveUUID(input.CommentID) || input.Enabled != nil {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	r, ok := s.repo.(ModerationRepository)
	if !ok {
		return assetclient.ErrUnavailable
	}
	return r.ModerateRecordingComments(ctx, id, actor, input, s.now())
}
func (s *Service) ListRecordingModeration(ctx context.Context, id, cursor string, limit int) (ModerationPage, error) {
	if !validLiveUUID(id) || cursor != "" && !validLiveUUID(cursor) || limit < 1 || limit > 50 {
		return ModerationPage{}, ErrInvalid
	}
	r, ok := s.repo.(ModerationRepository)
	if !ok {
		return ModerationPage{}, assetclient.ErrUnavailable
	}
	return r.ListRecordingModeration(ctx, id, cursor, limit)
}

func (s *Service) WithVideoInteractions(enabled bool) *Service {
	s.videoInteractions = enabled
	return s
}
func (s *Service) VideoInteractionsEnabled() bool { return s.videoInteractions }

// Keep the legacy repository contract usable by older implementations.
type CommentExperienceRepository interface {
	SetCommentReaction(context.Context, string, string, string, *string, time.Time) (CommentReaction, error)
	ListTopRecordingComments(context.Context, string, string, string, int, bool, time.Time) (CommentPage, error)
}

func (s *Service) SetCommentReaction(ctx context.Context, id, user, comment string, reaction *string) (CommentReaction, error) {
	if !validLiveUUID(comment) || reaction != nil && *reaction != "like" && *reaction != "dislike" {
		return CommentReaction{}, ErrInvalid
	}
	r, ok := s.repo.(CommentExperienceRepository)
	if !ok {
		return CommentReaction{}, assetclient.ErrUnavailable
	}
	return r.SetCommentReaction(ctx, id, user, comment, reaction, s.now())
}
func (s *Service) ListTopRecordingComments(ctx context.Context, id, user, cursor string, limit int, refresh bool) (CommentPage, error) {
	if limit < 1 || limit > 50 || len(cursor) > 128 || refresh && cursor != "" {
		return CommentPage{}, ErrInvalid
	}
	r, ok := s.repo.(CommentExperienceRepository)
	if !ok {
		return CommentPage{}, assetclient.ErrUnavailable
	}
	return r.ListTopRecordingComments(ctx, id, user, cursor, limit, refresh, s.now())
}
