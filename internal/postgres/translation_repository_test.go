package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/translation"
)

func TestReserveTranslationRejectsNonPositiveLimitsBeforeDatabaseAccess(t *testing.T) {
	repository := New(nil)
	for _, limits := range [][4]int{{0, 1, 1, 1}, {1, 0, 1, 1}, {1, 1, 0, 1}, {1, 1, 1, 0}} {
		reservation := translation.Reservation{Actor: "actor-1", ResourceType: "news", ResourceID: "resource-1", SourceVersion: 1, TargetLocale: "ja", Now: time.Now(), ActorMinuteLimit: limits[0], DeploymentMinuteLimit: limits[1], ActorDailyLimit: limits[2], DeploymentDailyLimit: limits[3], Cooldown: time.Minute}
		if err := repository.ReserveTranslation(context.Background(), reservation); err == nil {
			t.Fatalf("limits %v were accepted", limits)
		}
	}
}

func TestReleaseTranslationRejectsIncompleteReservationBeforeDatabaseAccess(t *testing.T) {
	repository := New(nil)
	if err := repository.ReleaseTranslation(context.Background(), translation.Reservation{}); err == nil {
		t.Fatal("incomplete reservation was accepted")
	}
}
