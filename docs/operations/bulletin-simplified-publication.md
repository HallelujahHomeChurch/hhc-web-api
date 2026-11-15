# Derived Simplified publication

Scope: general online bulletins from issue 1739. Traditional **online** publication
is the authorization for an exact Simplified release. PDF publication and
subscriber notification remain separate; this flow never sends another campaign.

## Original consumer rollout (migration 102)

1. Release the consumer and expand-only migration 102. It adds only nullable
   `online_bulletin_conversion_job.publication_event_id`; legacy rows/queries,
   immutable snapshots, published pointers, renderer artifacts and receipts stay
   intact. This release does not enqueue new publication intents.
2. Verify the exact API/extractor images, healthy revision, and that all old API
   replicas and old scheduled extractor executions have exited. Then release the
   producer that enqueues `bulletin.online.publish_simplified` at Traditional
   publication and removes the earlier layout/review automatic-draft trigger.
   Never roll back behind the compatible consumer while new intents can exist.

The producer release enables this sequence. A genuine Traditional publication
command creates one durable intent. An unchanged source publication replay may
ensure a missing intent, but any existing key is a no-op regardless of status:
it cannot rewrite an original authorizer or revive a failed, withdrawn or
superseded grant. A new publication command version has a distinct key/operation.
Layout completion and review confirmation do not start conversion. Explicit source
or target withdrawal cancels pending derived grants in the same transaction,
including withdrawal before Simplified has its first published revision.

## Processing and invariants

The outbox intent captures the publishing human/request, exact source revision
and immutable publication metadata. It queues/reuses a matching conversion,
waits for fresh native layout evidence, and confirms/publishes Simplified within
one transaction after rechecking the source and claim. No browser needs to stay
open. An un-published Traditional draft or metadata edit cannot become the source.
Pending source replacement/unpublish fences the intent. A stale worker cannot
retry or publish a newer claim. Existing Simplified publication stays available
until the replacement succeeds.

Conversion/layout failures retain actionable status. The existing conversion
retry route can resume the original publication intent with write scope, but
cannot invent publication authority, revive cancelled/superseded authority, or
restore an old automatic grant after a committed human recovery. Deferred audit events retain the publishing command's actor/request;
the retrying operator is separately audited. Intent selection uses the captured
publication command version, not wall-clock/UUID ordering or an unchanged mutable
Traditional draft counter. Competing same-revision history with unknown ordering
fails closed. Retained failures survive retry and healthy waits; withdrawal
cancels failed as well as pending/processing grants. An ambiguous note/highlight mapping
is a genuine exception: reuse explicit Traditional decisions only for proven
publication endpoints, otherwise retain the old publication for manual review.

Source text/metadata, native layout proof, confirmation, mapping checkpoint,
publication and audit validations are shared with the ordinary publication path.
No technical gate is bypassed. Background failure does not undo Traditional
publication. Both locales retain separate document/account/revision identities.
The existing independent unpublish behavior for already published editions stays
unchanged; source withdrawal cancels pending derived releases.

## Admin evidence and initial notification

PDF, Traditional online and Simplified online availability come from their own
publication projections. Conversion review readiness and measured layout readiness
are saved evidence. `derivedPublication` is one coherent automatic operation/source
identity; its `completed` status remains historical after an edition is withdrawn.
A distinct optional `recovery` receipt summary records a completed human grant and
also remains historical. Current `publishedRevision` establishes online availability.

The source-bound history returns at most twenty sanitized transitions plus
`hasMore`, coverage, and a separately retained last failure. Failure counts exclude
cancellation/supersession; current retry-budget attempts are separate. Healthy
waiting clears a resolved current error without erasing retained diagnostics or
consuming the failure budget. Missing legacy evidence remains unknown.

`notification` selects only the exact issue/series generation-zero command, whether
it originated automatically or was the first manual command. Later generation one
and subsequent manual commands do not replace it. Persisted valid request/command
identity is required; incomplete legacy rows remain unknown. Canonical/source
versions are not inferred from the overloaded outbox aggregate version.
`accepted` means Engagement accepted the command; provider email/push delivery is
outside this CMS state and this change creates no new notification command.

## Explicit saved-proof recovery

`BULLETIN_SIMPLIFIED_PUBLICATION_RECOVERY_ENABLED` is a dedicated capability and
defaults to false. Absence of an advertised producer capability does not authorize
an action. The existing Traditional recovery contract stays separate.

The private, no-store `POST /api/admin/bulletins/{issueID}/online/general/zh-Hans/recover-publication`
requires current human publish permission, the target document ID and If-Match,
fresh canonical/source CAS, exact immutable Traditional Published source identity,
the original publication operation, saved Simplified Local revision/layout hash,
and an actor-scoped idempotency key. Conversion protocol/assessment, frozen source
metadata, current target metadata, native measurement and mapping must qualify.
Missing intent/proof or changed target metadata blocks the action; recovery never
queues conversion, OCR, layout, Asset or notification work. A legacy draft without
an original publication event needs the separate deliberate review/confirm/publish
path; missing history never invents old authority.

A new operation records a distinct human grant and immutable receipt. It preserves
the original authorizer and terminal reasons and fences old automatic workers.
A currently published exact Local is not a new recovery candidate; a separately
confirmed human grant may restore qualified saved proof after withdrawal even if
the old automatic operation completed historically. The old operation is not revived.

After authentication, publish scope and syntax validation, exact receipt lookup
precedes live issue/source/Local/proof/capability gates and repeats after the issue
lock. An original actor/key/full-payload replay therefore returns its original
outcome after source change, withdrawal or capability disablement, without writes.
The shared actor/key namespace rejects changed payloads and cross-locale reuse.
For an unknown response, reconcile the original captured request/key; a new action
needs a fresh read, new confirmation and new key. Historical replay never proves
current availability.

## Current compatibility and rollback gate

Apply additive migration 116 through the normal release path before compatible
API/worker binaries use the new state, ledger and claim fields. Verify their role
privileges and that every writer understands opaque Simplified claims, captured
publication ordering, cancellation and human receipts. Keep the dedicated recovery
capability off until compatible-writer, Gateway/client and operational acceptance
is complete; this documentation does not activate a runtime manifest.

Disabling the capability blocks new human grants, while authenticated exact receipt
replay remains available. Keep a compatible API/worker version for existing grants
and receipts. Before an older-writer rollback, pause/drain affected work and follow
the reviewed release/rollback path; an old writer must not process active new
claims. Preserve additive schema and immutable histories; do not down-migrate,
reset counters or hand-edit queue/receipt rows to manufacture recovery.

## Verification

Real PostgreSQL tests cover a clean automatic release, un-published draft and
metadata edits, source withdrawal before/during conversion, expired/replaced
claims, conversion retries, layout failure, audit rollback, idempotent enqueue
and completion, and inheritance of a Traditional structural mapping. Worker
unit tests cover transient retry and exhaustion without generic unfenced writes.
Migration policy, complete race/vet suites, immutable extractor image and normal
CI/CD remain required before activation.

Use only the normal publication/retry UI for production smoke. Review the exact
issue/source/published revision first. Do not hand-edit outbox/provider rows or
unpublish/modify content to manufacture a test. Re-publishing an unchanged
Traditional publication may ensure its missing derived intent; replay must not
send a notification or create duplicate publications. Existing valid derived
proofs may be reused instead of recomputing identical conversion inputs.
