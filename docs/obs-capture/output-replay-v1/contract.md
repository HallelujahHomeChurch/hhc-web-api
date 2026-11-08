# Original-output replay K5

`POST /api/ingest/streams/replay` is a new authenticated read/renew operation.
It never opens or completes a binding. K4 open and the S10 wire remain frozen.
The producer must be released before the Gateway exact route and plugin consumer.
A rollback or unsupported route is unknown; consumers must never fall back to open,
resolve or bind.

The request authenticates the original `sourceId` using its stream key and requires
the original `(sourceId, outputSessionId)` ledger, fully bound recording/capture/
epoch, exact media profile and original completed wire receipt. The body key is
never retained by replay. The plugin owns its original per-output credential
reference; a mutable global source key cannot replace that reference.

A successful response echoes the complete original identity, profile and receipt.
`state=active` renews only its session. `state=terminal` additionally proves the
same existing original capture has durable `input_active=false` and never includes
a session. A deletion/revocation/key generation label or a missing row is not input
release evidence. All errors, incomplete bindings and identity uncertainty preserve
the original output and media. Only a proven terminal response can retire that
local output; a future new Start remains explicit and passes the existing global
input admission guard.

Replay takes the existing global/source/record/capture lock order using a plain
transaction and the existing advisory lock. It deliberately does not call
`sourceTransaction`, which also changes stale prepared-source state. It performs
no business updates, provider calls, source cleanup, notification/outbox writes,
abort or queue/media deletion. Publication authorization runs outside the database
transaction; the original authority is read again afterwards.

The existing K4 response renders a stored accepted bind receipt as completed only
once binding is present. K5 derives that identical wire receipt only after proving
the fully bound original ledger. It never completes an intent or changes a stored
receipt to satisfy a replay.

`schema.json`, `wire-fixtures.json`, `server-rules.json` and
`semantic-fixtures.json` contain the reviewed canonical wire and semantic rules.
`manifest.json` pins their bytes, the OpenAPI snapshot and frozen K4/S10 artifacts.
JSON Schema validates structure; service, HTTP and PostgreSQL tests enforce original
identity, input-release, concurrency and zero-side-effect guarantees.
