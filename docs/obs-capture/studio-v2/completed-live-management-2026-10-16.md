# Completed live recording management — 2026-10-16

## Scope and semantics

Completed broadcasts remain in the Live library, but their title and Open action go to `/content/recordings/{id}`. A completed control-room URL redirects to that editor; an in-progress unsaved edit is retained until saved or discarded. The member URL and live origin remain unchanged. Reuse details belongs to scheduled-event creation, not the completed-row action.

The Live library queries recording state for processing/archived broadcasts, with the same thumbnail/title row as uploads, actual publication status and retention expiry. Completion alone does not mark an unpublished draft as published. Processing recordings remain visible while validation completes. Retention calculation is unchanged. The recording editor returns to the Live library and hides obsolete ingest controls for broadcast-origin recordings.

CMS adds optional `isBroadcast` and `broadcastStartedAt` fields to Admin recording reads only; frontend client 1.0.71 carries the same fields. `broadcastStartedAt` is the CMS observation of the first verified public-live transition, for both direct and scheduled broadcasts. It is neither OBS transmission start nor the scheduled time nor a frame timestamp. It is persisted independently of the bounded signal history and never overwritten by later polls. For historical records, the first retained live observation is usable only if an earlier pre-live observation remains; otherwise the date is unknown. No bulk historical write, synthetic date or retention recalculation.

## Lifecycle recording audit

Source inspected: CMS at this change; Asset origin/main `7875b25` (confirm full revision separately when delivering). This is implementation evidence, not a fresh production database audit of all events.

| Event/data | Existing evidence | Remaining limitation |
| --- | --- | --- |
| Event creation and schedule | Broadcast CreatedAt, scheduledAt, direct/scheduled source identity | Schedule is intention, not actual start |
| Public range | Immutable command receipts and `[start,endExclusive)` boundaries | Sequence is media position, not a wall-clock timestamp |
| First public live | Stable StartedAt added here; proven retained history for older events | Older truncated history cannot be reconstructed reliably |
| Public end | End command/ACK/boundary and phase observations | No separate immutable first public-ended-at summary; StopAcceptedAt is capture completion, not the same event |
| Signal interruption/recovery | Phase/signal observed_at rows, last 200 per recording | Platform-observed time only; not exact OBS network timing, older observations roll off |
| Stop and seal | Accepted receipts and StopAcceptedAt | Do not substitute these for public End |
| Validation | Package/source attempt tables with created/finished/state; current progress and heartbeat snapshot | Stage-by-stage download/hash/decode durations and detailed previous failure causes still depend on worker logs; not all durable structured metrics |
| Ready and publication | ready_at, verified-ready and permission checks, publication receipts and published_at | Distinguish first publication from later publication receipts; timestamp names must not be conflated |
| Viewing statistics | Live/replay/total views, watch time, peak concurrency, minute aggregates and tracking_since | Minute retention is bounded; no complete player buffering, ABR, decode or recovery quality history |

Recommended next observability scope: permanent first public-ended timestamp, structured per-attempt stage durations/reasons, then sampled player quality events only if needed to diagnose outstanding recovery/weak-network cases. Reuse current PostgreSQL tables/log correlation and privacy draft review; no new event bus or unbounded viewer event collection. These extensions are not implemented by this management fix.

## Contract boundaries

K4 `k4-2026-10-15.rc1`, K3 `k3-2026-10-14.rc1`, B1 `b1-2026-10-10.rc1`, C1 `c1-2026-10-08.2`, inventory 1 are unchanged. Existing frozen manifests, digest/idempotency, membership authorization, 30000/1001 fps and 900-frame / 30.03-second media checks are unchanged. Frozen MemberRecording responses retain their exact shape. Only the current CMS Admin recording read schema gains optional fields; its overall OpenAPI hash changes. No OBS, CLI, Asset or Website code change required.

## Validation and handoff gate

Local regression scope: direct/scheduled public start persistence, retained/truncated historical evidence, database origin filtering before limit, completed routing, unsaved metadata, publication/expiry display, thumbnail cleanup/retry, editor back/delete and absence of ingest controls. Client generation/build/consumer checks and repository CI must pass. Production deployment must be read back separately; this document alone does not claim release or Windows acceptance.

## Windows minimum retest

1. No OBS session is required for library/editor checks. Use retained diagnostic recording `3e93b740-1915-4cad-b5b6-c379107c2b06` without modifying or deleting it: Live row has thumbnail, actual publication state and expiry; title/Open reach recording settings, Back returns Live, and an old completed control-room URL redirects. Upload rows use the same thumbnail layout. No Reuse details or Replay publication state in this library.
2. Dates show first verified public-live observation where known, otherwise Not recorded; they must not use schedule/upload/end times. Existing recordings remain unchanged. Do not manually publish or resend Stop/seal.
3. On the next already-authorized short test only, record the first public-live transition and compare the later library start date. This is not a request to rerun normal Stop, scheduled flow, 16-minute recovery or 2.5-hour collection. Confirm current gathering window and explicit machine-test authorization before starting OBS.
4. The prior first Continue-to-VOD fix still needs its separate targeted Windows acceptance: keep original member page, first handoff without reload, actual decoded dimensions/frames, duration, position/pause/rate, HTTP result and timestamp. Preserve first failure if a later retry succeeds. No full long-session rerun needed.
5. Report installed plugin commit, recording/capture/output IDs, platform revisions, Asia/Taipei times, pass/fail/not-tested and actual player evidence when applicable. Historical 379/388-second delay, weak-network seek/playback and normal gathering plus YouTube load are not newly passed. Retain all diagnostic recordings and formal recordings. Never include credentials or signed URLs.
