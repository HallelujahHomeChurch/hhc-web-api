package content

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStatementDismissalValidatesRef(t *testing.T) {
	s := NewService(&serviceRepository{}, time.Now)
	const id = "018f0000-0000-7000-8000-000000000001"
	for _, ref := range []StatementRef{{StatementID: "bad", PublishedVersion: 7}, {StatementID: id, PublishedVersion: 0}, {StatementID: id, PublishedVersion: -1}, {StatementID: id, PublishedVersion: 9007199254740992}} {
		if _, err := s.DismissStatement(context.Background(), id, ref); !errors.Is(err, ErrInvalid) {
			t.Fatalf("ref=%+v err=%v", ref, err)
		}
		if _, err := s.GetStatementDismissal(context.Background(), id, ref); !errors.Is(err, ErrInvalid) {
			t.Fatalf("GET ref=%+v err=%v", ref, err)
		}
	}
}
