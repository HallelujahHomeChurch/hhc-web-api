package content

import (
	"context"
	"encoding/json"
	"errors"

	"time"
)

const KindStatement = "statement"
const KindGeneral = "general"

func StatementWindowActive(start, end *time.Time, now time.Time) bool {
	return start != nil && end != nil && !now.Before(*start) && now.Before(*end)
}

// UnmarshalJSON distinguishes omitted window fields from an explicit clear.
func (in *WriteInput) UnmarshalJSON(data []byte) error {
	type plain WriteInput
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*in = WriteInput(decoded)
	_, start := fields["popupStartsAt"]
	_, end := fields["popupEndsAt"]
	in.PopupWindowSet = start || end
	return nil
}

func validStatementFields(module Module, in WriteInput) bool {
	if module != ModuleNews {
		return in.Kind == "" && in.PopupStartsAt == nil && in.PopupEndsAt == nil
	}
	if in.Kind != "" && in.Kind != KindGeneral && in.Kind != KindStatement {
		return false
	}
	if in.PopupStartsAt == nil && in.PopupEndsAt == nil {
		return true
	}
	return in.Kind == KindStatement && in.PopupStartsAt != nil && in.PopupEndsAt != nil && in.PopupStartsAt.Before(*in.PopupEndsAt)
}

func preserveStatementFields(in WriteInput, current Item) WriteInput {
	if in.Kind == "" {
		in.Kind = current.Kind
	}
	if !in.PopupWindowSet && in.PopupStartsAt == nil && in.PopupEndsAt == nil {
		in.PopupStartsAt = current.PopupStartsAt
		in.PopupEndsAt = current.PopupEndsAt
	}
	return in
}

// ActiveStatement uses the same published article as the standalone page.
type ActiveStatement struct {
	ServerNow    time.Time   `json:"serverNow"`
	NextChangeAt *time.Time  `json:"nextChangeAt"`
	Statement    *PublicItem `json:"statement"`
}

type StatementWindowConflict struct{ ContentID string }

func (e *StatementWindowConflict) Error() string {
	return "statement popup period overlaps another statement"
}
func (e *StatementWindowConflict) Unwrap() error { return ErrConflict }

func (s *Service) ActiveStatement(ctx context.Context, locale string) (ActiveStatement, error) {
	if !validLocale(locale) {
		return ActiveStatement{}, ErrInvalid
	}
	return s.repository.ActiveStatement(ctx, locale, s.now().UTC())
}

var ErrInvalidStatementWindow = errors.New("invalid statement window")

var ErrDraftChangesPresent = errors.New("resolve unpublished draft changes before ending the popup")

func (s *Service) EndStatementPopup(ctx context.Context, id string, expected int64, actor string) (Item, error) {
	return s.repository.EndStatementPopup(ctx, id, expected, actor, s.now().UTC())
}

func (s *Service) PublishStatement(ctx context.Context, id string, expected int64, actor string, request StatementNotificationRequest) (Item, error) {
	if !request.Valid() {
		return Item{}, ErrInvalid
	}
	item, err := s.repository.GetContent(ctx, ModuleNews, id)
	if err != nil {
		return Item{}, err
	}
	if !IsPublishable(item) {
		return Item{}, ErrNotPublishable
	}
	return s.repository.PublishStatement(ctx, id, expected, actor, request, s.now().UTC())
}
