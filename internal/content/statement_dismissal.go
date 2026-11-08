package content

import (
	"context"
	"errors"
	"github.com/google/uuid"
)

type StatementRef struct {
	StatementID      string `json:"statementId"`
	PublishedVersion int64  `json:"publishedVersion"`
}
type StatementDismissal struct {
	StatementID      string `json:"statementId"`
	PublishedVersion int64  `json:"publishedVersion"`
	Dismissed        bool   `json:"dismissed"`
}

var ErrStatementVersionChanged = errors.New("statement published version changed")
var ErrStatementDismissalRestricted = errors.New("statement preference processing restricted")

func (ref StatementRef) Valid() bool {
	id, err := uuid.Parse(ref.StatementID)
	return err == nil && id != uuid.Nil && id.String() == ref.StatementID && ref.PublishedVersion > 0 && ref.PublishedVersion <= 9007199254740991
}
func (s *Service) GetStatementDismissal(ctx context.Context, owner string, ref StatementRef) (StatementDismissal, error) {
	if !ref.Valid() {
		return StatementDismissal{}, ErrInvalid
	}
	return s.repository.GetStatementDismissal(ctx, owner, ref)
}
func (s *Service) DismissStatement(ctx context.Context, owner string, ref StatementRef) (StatementDismissal, error) {
	if !ref.Valid() {
		return StatementDismissal{}, ErrInvalid
	}
	return s.repository.DismissStatement(ctx, owner, ref)
}
