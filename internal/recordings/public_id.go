package recordings

import (
	"context"
	"regexp"
)

var publicIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

func ValidPublicID(id string) bool { return publicIDPattern.MatchString(id) }

// RecordingIDByPublicID only resolves identity. Callers still enforce preview or member authority.
func (s *Service) RecordingIDByPublicID(ctx context.Context, publicID string) (string, error) {
	if !ValidPublicID(publicID) {
		return "", ErrNotFound
	}
	repo, ok := s.repo.(interface {
		RecordingIDByPublicID(context.Context, string) (string, error)
	})
	if !ok {
		return "", ErrNotFound
	}
	return repo.RecordingIDByPublicID(ctx, publicID)
}
