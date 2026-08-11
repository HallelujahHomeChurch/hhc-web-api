package postgres

import (
	"context"
	"testing"
	"time"
)

func TestReserveTranslationRejectsNonPositiveLimitsBeforeDatabaseAccess(t *testing.T) {
	repository := New(nil)
	for _, limits := range [][2]int{{0, 1}, {1, 0}, {-1, 1}, {1, -1}} {
		if err := repository.ReserveTranslation(context.Background(), "actor-1", time.Now(), limits[0], limits[1]); err == nil {
			t.Fatalf("limits %v were accepted", limits)
		}
	}
}
