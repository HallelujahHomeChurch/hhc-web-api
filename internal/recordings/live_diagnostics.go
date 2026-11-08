package recordings

import (
	"context"
	"errors"
	"log/slog"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/assetclient"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/auditclient"
)

// Dependency errors may contain credentials or URLs; log only known categories.
func logLivePlaybackFailure(ctx context.Context, stage string, err error) {
	class := "internal"
	var databaseError interface{ SQLState() string }
	switch {
	case errors.As(err, &databaseError):
		class = "database"
		if databaseError.SQLState() == "53300" {
			class = "database_capacity"
		}
	case errors.Is(err, ErrTransactionCapacity):
		class = "live_capacity"
	case errors.Is(err, assetclient.ErrUnavailable):
		class = "asset_unavailable"
	case errors.Is(err, context.DeadlineExceeded):
		class = "deadline"
	case errors.Is(err, context.Canceled):
		class = "cancelled"
	case errors.Is(err, ErrNotFound), errors.Is(err, assetclient.ErrNotFound):
		class = "not_found"
	case errors.Is(err, ErrConflict), errors.Is(err, assetclient.ErrConflict):
		class = "conflict"
	case errors.Is(err, ErrForbidden), errors.Is(err, assetclient.ErrForbidden):
		class = "forbidden"
	}
	p, _ := auditclient.ProvenanceFromContext(ctx)
	slog.WarnContext(ctx, "recording_live_playback_failed", "stage", stage, "error_class", class, "request_id", p.RequestID)
}
