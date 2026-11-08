# Detached PostgreSQL cleanup

Schema-migration advisory unlock, content-seed advisory unlock and failed-run
bookkeeping each use a detached context with a ten-second deadline and explicit
cancellation. Cancellation of the original work still allows cleanup to run.
The existing SQL, lock keys, transaction boundaries and successful business
operations are unchanged.

Failed-run bookkeeping and advisory unlock can run sequentially. Their context
waits can therefore add up to twenty seconds; this is not a ten-second limit for
the whole invocation. Transaction rollback, physical driver close, backend
removal, startup and retry/drain time have no newly proved bound.

Lock-acquisition errors discard the session because lock ownership is uncertain.
Unlock failure or timeout preserves the primary error, joins the cleanup error
and discards the possibly locked physical connection through `driver.ErrBadConn`.
The caller-owned pool remains usable. A migration may already have committed
before cleanup fails: a cleanup error does not undo committed schema changes.
Use the normal migration/recovery policy, not a blind old-binary rollback.

Focused tests use virtual time, sqlmock and the existing in-memory seed driver.
They prove cooperative context deadlines, cleanup attempts after parent
cancellation, additive waits, error preservation and physical-discard behavior
in those fixtures. They do not prove actual driver-close/backend timing or
continuing execution admission. The conditional PG budget remains uncertified.

Production integration requires the manual-entry ownership changes in
[PR #274](https://github.com/HallelujahHomeChurch/hhc-web-api/pull/274), a refreshed
main-based review and CI after that merge, and a coordinator-assigned release
slot. This change does not authorize deployment, extra migration executions or
PG configuration changes.
