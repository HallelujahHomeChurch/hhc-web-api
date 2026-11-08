package postgres

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"time"

	"github.com/HallelujahHomeChurch/hhc-web-api/internal/recordings"
)

// Admission precedes checkout, so recording lock waiters cannot consume this
// repository's entire finite pool budget. Other repositories and plain queries
// remain outside this class. Main owns one RecordingRepository per shared DB.
func (r *RecordingRepository) beginRecordingTransaction(ctx context.Context) (*sql.Tx, func(), error) {
	return r.beginRecordingTransactionClass(ctx, false)
}
func (r *RecordingRepository) beginRecordingTransactionClass(ctx context.Context, subjectRead bool) (*sql.Tx, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if !r.admitRecordingTransactionClass(subjectRead) {
		return nil, nil, recordings.ErrTransactionCapacity
	}
	admitted := true
	defer func() {
		if admitted {
			r.releaseRecordingTransactionClass(subjectRead)
		}
	}()
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, err
	}
	var once sync.Once
	finishTx := func() { once.Do(func() { defer r.releaseRecordingTransactionClass(subjectRead); _ = tx.Rollback() }) }
	admitted = false
	return tx, finishTx, nil
}

// Share one budget across recording transactions, including grants and waiters.
// Other repositories and direct queries remain outside this admission class.
func (r *RecordingRepository) admitRecordingTransaction() bool {
	return r.admitRecordingTransactionClass(false)
}
func (r *RecordingRepository) admitRecordingTransactionClass(subjectRead bool) bool {
	maximum := r.db.Stats().MaxOpenConnections
	limit := maximum - 2
	if maximum == 2 {
		limit = 1
	}
	r.transactionMu.Lock()
	defer r.transactionMu.Unlock()
	// An unlimited pool has no finite connection budget to reserve.
	if maximum > 0 && r.transactions >= limit {
		return false
	}
	if subjectRead && maximum > 0 {
		readLimit := limit - 2
		if readLimit < 1 {
			readLimit = 1
		}
		if r.subjectTransactions >= readLimit {
			return false
		}
	}
	r.transactions++
	if subjectRead {
		r.subjectTransactions++
	}
	return true
}

func (r *RecordingRepository) releaseRecordingTransaction() {
	r.releaseRecordingTransactionClass(false)
}
func (r *RecordingRepository) releaseRecordingTransactionClass(subjectRead bool) {
	r.transactionMu.Lock()
	r.transactions--
	if subjectRead {
		r.subjectTransactions--
	}
	if r.transactionWake != nil {
		close(r.transactionWake)
		r.transactionWake = make(chan struct{})
	}
	r.transactionMu.Unlock()
}

// Only scoped subject reads queue. They do not borrow a SQL connection while
// waiting, and still share the transaction budget/reserved slots with writes.
func (r *RecordingRepository) beginSubjectReadTransaction(ctx context.Context) (*sql.Tx, func(), error) {
	tx, finish, err := r.beginRecordingTransactionClass(ctx, true)
	if !errors.Is(err, recordings.ErrTransactionCapacity) || r.db.Stats().MaxOpenConnections == 1 {
		return tx, finish, err
	}
	r.transactionMu.Lock()
	if r.subjectWaiters >= 64 {
		r.transactionMu.Unlock()
		return nil, nil, recordings.ErrTransactionCapacity
	}
	r.subjectWaiters++
	if r.transactionWake == nil {
		r.transactionWake = make(chan struct{})
	}
	r.transactionMu.Unlock()
	defer func() { r.transactionMu.Lock(); r.subjectWaiters--; r.transactionMu.Unlock() }()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		select {
		case <-timer.C:
			return nil, nil, recordings.ErrTransactionCapacity
		default:
		}
		r.transactionMu.Lock()
		wake := r.transactionWake
		r.transactionMu.Unlock()
		tx, finish, err = r.beginRecordingTransactionClass(ctx, true)
		if !errors.Is(err, recordings.ErrTransactionCapacity) {
			return tx, finish, err
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-timer.C:
			return nil, nil, recordings.ErrTransactionCapacity
		case <-wake:
		}
	}
}
