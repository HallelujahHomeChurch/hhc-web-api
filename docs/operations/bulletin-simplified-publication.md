# Derived Simplified publication

Scope: general online bulletins from issue 1739. Traditional **online** publication
is the authorization for an exact Simplified release. PDF publication and
subscriber notification remain separate; this flow never sends another campaign.

## Consumer before producer

1. Release the consumer and expand-only migration 102. It adds only nullable
   `online_bulletin_conversion_job.publication_event_id`; legacy rows/queries,
   immutable snapshots, published pointers, renderer artifacts and receipts stay
   intact. This release does not enqueue new publication intents.
2. Verify the exact API/extractor images, healthy revision, and that all old API
   replicas and old scheduled extractor executions have exited. Then release the
   producer that enqueues `bulletin.online.publish_simplified` at Traditional
   publication and removes the earlier layout/review automatic-draft trigger.
   Never roll back behind the compatible consumer while new intents can exist.

The producer release enables this sequence. Successful Traditional publication
(including an unchanged publication replay) ensures one durable intent. Layout
completion and review confirmation no longer start conversion. Explicit source
unpublish cancels pending intents in the same transaction.

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
cannot invent a new publication authorization or revive an explicitly cancelled
source. Deferred audit events retain the publishing command's actor/request;
the retrying operator is separately audited. Only the latest intent can resume;
a newer delivered intent masks old failures, and withdrawal cancels failed as
well as pending/processing intents. An ambiguous note/highlight mapping
is a genuine exception: reuse explicit Traditional decisions only for proven
publication endpoints, otherwise retain the old publication for manual review.

Source text/metadata, native layout proof, confirmation, mapping checkpoint,
publication and audit validations are shared with the ordinary publication path.
No technical gate is bypassed. Background failure does not undo Traditional
publication. Both locales retain separate document/account/revision identities.
The existing independent unpublish behavior for already published editions stays
unchanged; source withdrawal cancels pending derived releases.

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
