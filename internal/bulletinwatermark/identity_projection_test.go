package bulletinwatermark

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/accountclient"
)

type identityProjectionStoreStub struct {
	ids        []string
	identities []accountclient.Identity
	refreshed  time.Time
}

func (s *identityProjectionStoreStub) ListStaleWatermarkIdentityIDs(context.Context, time.Time, int) ([]string, error) {
	return s.ids, nil
}

func (s *identityProjectionStoreStub) UpsertWatermarkIdentities(_ context.Context, ids []string, identities []accountclient.Identity, refreshed time.Time) error {
	s.ids, s.identities, s.refreshed = ids, identities, refreshed
	return nil
}

type identityProjectionResolverStub struct {
	identities []accountclient.Identity
	err        error
}

func (s identityProjectionResolverStub) ResolveIdentities(context.Context, []string) ([]accountclient.Identity, error) {
	return s.identities, s.err
}

func TestIdentityProjectionReconcilerRefreshesReferencedAccounts(t *testing.T) {
	now := time.Date(2026, 9, 23, 8, 0, 0, 0, time.UTC)
	store := &identityProjectionStoreStub{ids: []string{"account-1", "account-2"}}
	want := []accountclient.Identity{{ID: "account-1", DisplayName: "Alice", Email: "alice@example.test"}}
	reconciler := NewIdentityProjectionReconciler(store, identityProjectionResolverStub{identities: want})
	reconciler.now = func() time.Time { return now }
	if err := reconciler.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.identities, want) || !store.refreshed.Equal(now) {
		t.Fatalf("stored %+v at %v", store.identities, store.refreshed)
	}
}

func TestIdentityProjectionReconcilerDoesNotWriteAfterResolverFailure(t *testing.T) {
	store := &identityProjectionStoreStub{ids: []string{"account-1"}}
	reconciler := NewIdentityProjectionReconciler(store, identityProjectionResolverStub{err: errors.New("unavailable")})
	if err := reconciler.ProcessOnce(context.Background()); err == nil {
		t.Fatal("resolver failure accepted")
	}
	if !store.refreshed.IsZero() {
		t.Fatal("failed resolution was stored")
	}
}
