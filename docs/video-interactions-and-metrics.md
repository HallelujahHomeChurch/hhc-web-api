# Video interactions and viewing metrics

Implementation candidate, 2026-10-15. No production enablement is implied.

## Ownership and rollout

CMS owns recording identity, comments, likes, reports and playback statistics. The
existing recording ID survives live → replay → VOD. Neither Asset nor OBS wire
contracts change. Admin preview never sends viewing heartbeats.

Independent, default-off switches:
- `STUDIO_WRITERS_ENABLED`: new Studio source/direct admission.
- `VIDEO_INTERACTIONS_ENABLED`: member likes/comments and Admin moderation.
- `VIDEO_ANALYTICS_ENABLED`: member heartbeat and member/Admin metrics.

Release CMS ordinary account cleanup compatibility first, then its compatible
Account caller, Gateway and frontend-platform 1.0.66, then consumers. Before
enabling either data-writing feature, verify ordinary cleanup, preserved subject
fences and the deployed Account caller, and review the applicable privacy notice.
Activation still requires a separate reviewed configuration preview. Existing
data retention/account cleanup runs even while switches are off.
No configuration change, historical publication, key reset or deletion is part
of this code change.

## Social contract

Canonical schemas and routes are in `openapi.yaml`. Member routes always require
current recording entitlement, legal access, visible content and the subject
fence. Likes are desired-state PUT, unique per account/recording. Comments use a
client mutation UUID plus content digest; exact retry returns the same comment,
changed content with the same UUID conflicts. Text is plain, at most 2,000 Unicode
characters. A reply can reference only a root comment. Owner edits/deletes use
expectedRevision. Deleted parents remain anonymous placeholders; hidden parents
and their replies are omitted from member projection. Closing comments prevents
new roots/replies but preserves existing visible content.

Reports are private, one per reporter/comment. Admin hide/restore, resolve and
open/close operations require recordings write and valid audit provenance. Audit
rows use existing CMS audit storage, contain no comment text, and share the actor
subject fence. They are erased with the actor; no new central audit action is invented.

## Metrics (hhc-video-1)

Heartbeat every 15 seconds: per-mount random session UUID, increasing sequence,
state, media position and bounded deltaPlayingMs. Server wall time bounds credit;
position/seek distance and playback speed do not multiply watch time. Same-account
concurrent tabs share a recording visit and overlapping wall time is credited
once. Repeated or older sequence does not refresh the presence TTL.

Concurrent viewers: unique playing accounts with a heartbeat within 60 seconds.
A visit qualifies at ten credited seconds, or completion of a shorter published
video using trusted package duration. A 30-minute inactive gap creates a new visit.
Live and replay qualifications are separate; total is their visit union. Lifetime
unique uses a retained per-recording account set, not seven-day session history.
Account cleanup removes that identity set, so unique is not falsely presented as permanently
identifiable history. Totals/watch time are anonymous aggregates.

Existing CMS background work samples every 30 seconds into UTC minute buckets;
minute peak is the maximum observed, not a claim of instantaneous peak. Raw
session/visit data expires after seven days, minute buckets after 365 days.
Viewer identities, likes and comment ownership live with the content or ordinary account cleanup.
No new service, event bus or cloud media transform is introduced.

`measuredAt` is the read timestamp, `trackingSince` is the first accepted heartbeat
for that recording, and `status=not_started` means no measurements exist. Consumers
must not portray unavailable/not-started historical statistics as zero lifetime
views. Charts are deferred; minute data is already retained for later chart APIs.

## Account cleanup and failure boundaries

Ordinary Account cleanup removes subject-owned likes, reports, playback sessions/visits, viewer identity entries and moderation audit actor rows. It anonymizes comment bodies/authors while retaining reply structure and preserves anonymous aggregate totals. Existing subject fences continue to block new interaction, moderation and telemetry writes. Legacy private DSR export/restriction/erasure wrappers are retired; no `video-v1` DSR coverage requirement remains.

Telemetry is best-effort and never gates playback. A failed sample is not retried
as a new view; subsequent bounded samples resume. API failures show unavailable,
not zero. This is operational viewing estimation, not proof of people at a screen.

## Unchanged acceptance

K4 k4-2026-10-15.rc1, K3, B1, C1, inventory 1, 30000/1001 fps, 900 frames/30.03s,
media digests, idempotency, authorization and public boundaries are unchanged.
Prior Windows delay/weak-network failures remain unresolved evidence. UI tests and
local DB checks do not establish normal-gathering OBS/YouTube load acceptance.

## Local evidence and rollout limits (2026-10-15 Asia/Taipei)

- PostgreSQL tests include real concurrent writes, account overlap, ordinary account cleanup, preserved restriction fences and moderator fencing. Local 32-worker burst: 100 viewers / 200 writes in 243 ms, per-viewer two-heartbeat p95 146 ms; 500 viewers / 1,000 writes in 990 ms, p95 139 ms. This excludes Gateway, Account, network and cloud billing. It is not production capacity acceptance.
- At a 15-second heartbeat cadence, 100/500 active tabs generate approximately 6.7/33.3 heartbeat requests/second before reads. Shared-NAT rate limits, production DB utilization and actual monthly cost remain deployment measurements.
- Signal history records at most the latest 200 phase/signal changes per recording, starting after migration. Times are platform observations, not ACK media boundaries or invented historical disconnect times. Deleting the recording cascades these non-account observations.
- Studio replay duration comes from the verified Asset projection; no duration is inferred by multiplying segment count.
- `VIDEO_INTERACTIONS_ENABLED` and `VIDEO_ANALYTICS_ENABLED` default false independently. Deploy compatible CMS ordinary account cleanup before considering activation. Legal/privacy text review and an explicit configuration preview remain separate from code completion.
- K4/B1/C1, media digest, fixed frame rate, segment boundaries and Windows acceptance evidence are unchanged. No Asset/CLI/OBS modifications are part of this change.

### Comment experience

The interaction summary includes the visible non-deleted comment count (including
replies, excluding hidden threads). Replies retain root `parentId` and may carry a
`replyToId` in that thread. Reply target identities are resolved on read and disappear
when hidden/deleted. Comment reactions use desired-state PUT (`like`, `dislike`, or
null); only like totals and the caller's choice are returned.

`order=top` freezes root comment order for 15 minutes, with current visibility
checked on every page. Initial `refresh=true` replaces the caller's current ranking;
continuations are account/recording-bound opaque cursors. Expired cursors return
409 `recording_comment_cursor_expired`; invalid/replaced cursors return 400. Existing
newest/oldest UUID cursors remain supported. Snapshots contain IDs/ordinals only,
are purged in bounded reconciliation batches, and participate in account cleanup.

Main has retired the legacy DSR export/action routes. Do not recreate them for
this feature. Active account cleanup and its write fence cover reactions and rank
snapshots; any future export owner must include reaction choice/update time and
rank ownership/expiry alongside existing comment data.
