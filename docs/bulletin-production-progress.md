# Admin extraction progress

`AdminState.production` is optional and belongs to the current issue, edition,
source checksum, canonical metadata and extraction job. This implementation
reports **scan, recognition and reconciliation**. Reconciliation here means
assembling the verified recognition result into an extraction candidate. Existing
Admin update, layout and publication fields remain the authority for subsequent
semantic planning, layout qualification and publication. Reserved enum values
are not evidence that those later progress hooks exist.

Totals remain null until validated geometry is available, or an accepted complete
recognition snapshot is reused. Pages are one-based. A provider attempt does not
advance `verifiedPages`: the runner must validate materialization, the semantic
hash and exact preceding continuation. The database then joins a content-free
receipt to that job's immutable verified checkpoint and counts only the contiguous
prefix. A changed input removes its dependent receipt suffix before checkpoint
reuse. Retry attempts can revalidate existing checkpoints without paid replay.

Normal completion removes checkpoint results as before. A completed prefix then
requires the retained receipts to match the job's immutable completed recognition
evidence, including source, canonical metadata, protocol, model and page hashes.
Complete snapshot reuse records its accepted immutable snapshot identity and
reports the verified reuse count. Neither path qualifies a document for publication.

Migration 115 adds a same-row `BEFORE UPDATE` revision trigger. It writes no other
row and introduces no cross-table lock order. Every job mutation, including a
staff retry that resets the attempt count and a no-op update, advances
`progressVersion`. Each claim also records a fresh opaque `attemptId`; lease tokens
are never returned. The first claim's `startedAt` survives retries for newly tracked
jobs. Migration marks existing jobs as historically untracked; their unknown first
start remains null even when a staff retry resets the attempt counter.
The same-row trigger also preserves that unknown marker when a compatible old
worker leaves no first-start timestamp and an old or new staff writer resets its
attempt count. A later tracked claim cannot mistake that reset for a first start.
`lastProgressAt` changes only on verified advancement; `heartbeatAt` is explicit
worker activity and does not imply success or prove a stall when old. Polling is
read-only: `observedAt` is the current database statement time.

The projection stays in the existing single Admin-state MVCC statement. It first
selects one matching job, preferring active work over superseded history, before
evaluating its bounded prefix. The receipt primary key and existing verified-page
checkpoint index are sufficient for the local 40-page query-plan fixture; no
additional speculative checkpoint index is installed. The same-row trigger and
two extra progress transactions per processed page add work. Local fixture timing
is supporting evidence, not a production load or cost measurement.

The new table retains only page/hash/reuse/timestamp evidence and cascades with
its extraction job. Job metadata and receipt retention follow existing job
history; the inventory records retention approval as pending. No independent raw
source/model retention policy is introduced. Member/public projections, provider
concurrency, native layout qualification, publication authority and notification
behavior are unchanged by this progress slice.

Release the compatible migration and CMS producer before a published SDK and
consumer. Old writers remain compatible with the additive columns and ordering
trigger, but cannot create validated progress receipts. Missing historical totals
and progress remain unknown. Full native renderer/image gates, producer/SDK
release, registry-pinned consumer integration, later phase hooks and production
polling/load acceptance remain separate work.
