# K4 automatic publication cadence — 2026-10-17

## Scope and evidence

Recording `4e3a9939-0e60-4cfb-a1c3-71993324abb7`, capture
`33890e9b63b1b860da1ae5fbfee78ede`, naturally published before this patch.
Do not reupload, manually publish or alter that diagnostic recording.

The fixed Windows report is:
https://github.com/HallelujahHomeChurch/hhc-obs-plugin/blob/45b171b9aea4a199380bb8b65d42ba9d69f78a37/docs/reviews/windows-k4-timing-2026-10-17.md

Server evidence, 2026-10-17 Asia/Taipei UTC+8:

- CMS public End: 01:56:42.318636897.
- Asset worker attempt 1 finish log: 01:57:16.590, committed=true,
  elapsed_ms=35710; the exact SQL finished_at was not read.
- Package ready_at: 01:57:16.564643.
- Selected VOD cover inheritance operation created_at: 01:58:42.071579.
- CMS published_at: 02:03:11.988979, published/version 4.
- Ready to recorded publication: 355.424336 seconds. Cover inheritance operation
  to recorded publication: 269.917400 seconds.

Persisted event timestamps are not database commit timestamps. Windows' last draft
response at 02:03:13.555 and first published response at 02:03:18.975 remain valid
client observations; their response window does not establish server commit time.
The first aggregate publication conflict was not tagged with a recording ID, so
its exact failing dependency remains unproven.

## Change

The CMS server runs automatic publication on its own serial 30-second ticker,
including one startup pass, instead of the five-minute maintenance loop. Each pass
retains the existing 30-second context and 100-intent batch limit. Slow capture
maintenance, cleanup, cover-reference release, broadcast reconciliation and metric
rollup do not block this ticker. Runs do not overlap within a process.

The existing persisted intent, per-intent exponential backoff, current permission
probe, ready-package and cover checks, publication/cancellation locking, audit and
notification idempotency remain unchanged. A ready cover still clears obsolete
publication retry delay through existing inheritance logic. No new service, event
bus, worker job, schema migration, feature flag or Windows implementation change.
An idle process performs about two candidate queries per minute rather than one
per five minutes; external verification calls only occur for eligible candidates.

Thirty seconds is the polling cadence, not a seal-to-publication SLA. Validation,
cover generation, legitimate retry backoff, dependency outages, contention and
batch saturation can still add time.

## Frozen contracts

- K4: k4-2026-10-15.rc1; K3: k3-2026-10-14.rc1.
- B1: b1-2026-10-10.rc1; C1: c1-2026-10-08.2; inventory schema 1.
- 30000/1001 fps, 900 frames / 30.03 seconds, [start,endExclusive), digest,
  idempotency, member authorization and complete media verification unchanged.
- K4 manifest SHA-256:
  e984a5a54b80c817ba701f6923d73c02b57bf9c5b7d238f15442543c5ae18464.
- This patch does not change OpenAPI. The current main OpenAPI also contains the
  independently merged bulletin change #234; do not confuse that with K4 changes.

## Verification and release boundary

Scheduling regression tests exercise immediate startup, the next 30-second pass
after an error, bounded context, serial timeout recovery and cancellation. They
fail with the former five-minute ticker. Existing PostgreSQL tests cover cover
inheritance, backoff/permission checks, concurrent publication, manual cancellation
and exactly-once audit/notification. Full required CI must pass before merge.

The release handoff must accompany this immutable document with actual merge SHA,
CI/release runs, deployed revision/image, health and traffic readback. Documentation
or local/CI success alone does not constitute Windows OBS acceptance.

## Minimal Windows retest

1. Confirm no meeting is in progress and current authorization permits a short
   synthetic run. Keep the existing plugin and stream key; no new plugin is needed.
2. One new short direct broadcast (about 3–4 minutes), automatic thumbnail and
   automatic publication enabled in Console. Keep its Console/member page open.
3. Stop HHC output normally once. Do not send manual End, repeat Stop/seal, reload
   to force progress, manually publish, or reupload. Observe normal public End,
   seal, ready, cover selection, natural publication and the same URL's VOD.
4. Record installed plugin commit, recording/capture IDs, timezone, serverNow,
   readyAt, first cover-ready/selected observation, last draft and first published
   response times, final version and one actual VOD decode (resolution, position,
   duration, paused/rate/error). Preserve event vs observation precision.
5. Once media and the selected cover are ready and the intent is eligible, expect
   the next 30-second publication pass plus processing time, without another
   fixed five-minute wait. If still pending 90 seconds after prerequisites are
   observed ready, stop expanding tests and return evidence for CMS correlation;
   this diagnostic threshold does not override valid per-intent backoff or prove
   a new failure cause.
6. Pass this scoped test only if publication is natural, the timing supports the
   new cadence, and the same URL VOD decodes. Mac correlates server timestamps if
   client samples are insufficient. No full player matrix or 2.5-hour rerun.

Historical first-VOD transition failures, 379/388-second recovery lag, weak-network
seek and normal gathering/YouTube load acceptance stay unchanged. Preserve this
new diagnostic recording until diagnosis is complete; no cleanup is authorized
by this document. Never include credentials, cookies, keys or signed URLs.
