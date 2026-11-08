package postgres

import (
	"context"
	"database/sql"
	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

type recordingRowReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// subjectReader cannot acquire another pool connection. Its lifetime ends with
// WithRecordingSubject; it does not change transaction isolation or lock order.
type subjectReader struct{ tx *sql.Tx }

func (r subjectReader) Get(ctx context.Context, id string) (recordings.Recording, error) {
	return readSubjectGet(ctx, r.tx, id)
}
func (r subjectReader) GetBroadcast(ctx context.Context, id string) (recordings.Broadcast, error) {
	return readSubjectGetBroadcast(ctx, r.tx, id)
}
func (r subjectReader) LatestRecordingCapture(ctx context.Context, id string) (recordings.Capture, error) {
	return readSubjectLatestRecordingCapture(ctx, r.tx, id)
}
