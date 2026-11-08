package bulletinwatermark

import (
	"context"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/accountclient"
)

type IdentityProjectionStore interface {
	ListStaleWatermarkIdentityIDs(context.Context, time.Time, int) ([]string, error)
	UpsertWatermarkIdentities(context.Context, []string, []accountclient.Identity, time.Time) error
}

type IdentityProjectionResolver interface {
	ResolveIdentities(context.Context, []string) ([]accountclient.Identity, error)
}

type IdentityProjectionReconciler struct {
	store    IdentityProjectionStore
	resolver IdentityProjectionResolver
	now      func() time.Time
}

func NewIdentityProjectionReconciler(store IdentityProjectionStore, resolver IdentityProjectionResolver) *IdentityProjectionReconciler {
	return &IdentityProjectionReconciler{store: store, resolver: resolver, now: func() time.Time { return time.Now().UTC() }}
}

func (r *IdentityProjectionReconciler) ProcessOnce(ctx context.Context) error {
	now := r.now().UTC()
	ids, err := r.store.ListStaleWatermarkIdentityIDs(ctx, now.Add(-5*time.Minute), 50)
	if err != nil || len(ids) == 0 {
		return err
	}
	identities, err := r.resolver.ResolveIdentities(ctx, ids)
	if err != nil {
		return err
	}
	return r.store.UpsertWatermarkIdentities(ctx, ids, identities, now)
}

func (r *IdentityProjectionReconciler) Run(ctx context.Context) {
	_ = r.ProcessOnce(ctx)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = r.ProcessOnce(ctx)
		}
	}
}
