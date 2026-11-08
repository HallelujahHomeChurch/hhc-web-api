# S10 opt-in segment profile — s10-2026-10-17.rc1

Status: implementation candidate, not production or Windows acceptance. Activate only after the Asset API, recording worker, member-media Worker and CMS deployments have all been read back. Existing outputs remain on their original profile. No global switch, key rotation or existing capture migration is required.

## Wire supplement

This is an explicit additive supplement to K4 `k4-2026-10-15.rc1`. B1 `b1-2026-10-10.rc1`, C1 `c1-2026-10-08.2`, inventory schema 1 and the canonical digest algorithm remain the base contracts. The K4 open input/output schemas gain the optional field below; their schema hashes therefore change. Do not describe this release as an unchanged schema. Frozen earlier snapshots are retained.

`POST /api/ingest/streams/open`: the existing protocolVersion, outputSessionId and streamKey fields remain required. Optional `mediaProfile` has the sole supported nonempty value `obs-hls-10s-v1`. Omit it for the existing 900-frame profile. A successful response echoes the chosen optional mediaProfile alongside existing identity fields. Other response identities, credentials, receipt and state semantics are unchanged. Unknown profiles return HTTP 422 `broadcast_protocol_unsupported`.

Windows must persist the profile with its opening intent before the first request. Retries, reconnect and process recovery use the original outputSessionId and profile. An existing ID requested with a different profile returns HTTP 409 `broadcast_state_conflict`; preserve the journal and stop retrying that mismatch. Do not create a replacement ID, resolve another event, or silently downgrade. Require the exact short-profile echo before accepting a 10-second binding. If an older platform rejects or omits the echo, retain the intent and report unsupported profile.

CMS persists the profile in the capture intent before contacting Asset. Its private capture create adds `segmentFrames: 300` only for this profile, with broadcastEpoch. Asset persists segment_frames; legacy/missing defaults to 900. A retry cannot change the profile. This private field is not a new OBS permission or endpoint.

## Media and boundaries

- 30000/1001 fps, exactly 300 video frames per normal segment: 10.01 seconds. All three existing NVENC qualities, AAC settings and independent Program output remain.
- Do not use 10.00 seconds or 30 fps. Do not change frame cadence, public marker semantics, full SHA-256/probe/decode verification or membership authorization.
- Common contiguous rendition validation remains mandatory. Start/End ACK uses the next common segment boundary. Legal short final segments require accepted normal Stop; short interior segments and mixed profiles remain invalid.
- Inventory presetVersion must be `obs-hls-10s-v1` for a 300-frame capture, and must not be used for a legacy capture. SchemaVersion stays 1. Recompute existing canonical digest from actual inventory bytes, paths and media; never fabricate segments or duration.
- Live and projected member playlists use TARGETDURATION 11 for this profile. Existing profile retains 31. Authority, immutable revision publication and [start,endExclusive) projection stay unchanged.
- Bounds: at most 4320 fragments per rendition for S10; other presets retain 1440. Total inventory/object listing bound becomes 13000. Existing 12-hour duration, 10 GB package, per-object and declaration batch limits remain. Whichever bound is reached first applies.
- Seek preview/cover generation uses the actual validated timeline, never sequence × 30 seconds for S10.
- Initial member admission still requires three common verified public segments. This reduces the nominal media accumulation interval from 90.09 to 30.03 seconds; it does not guarantee a 30-second first decoded frame. Validation, upload, scheduling and player holdback remain measurable costs.

## Notifications and Console

New broadcasts queue one durable notification when the current public projection and three verified public segments are playable. Existing consent, audience entitlements, channels and delivery retries remain. Live-to-VOD first publication does not enqueue a second recording notification. Ordinary uploaded recordings keep their first-publication notification. Delivery admission rechecks live availability; an ended live must not emit a delayed new-live notice. No historical live notification backfill is performed for already-started events.

Direct Studio has no public Start/End action. Normal OBS Stop retains automatic End/archive. Scheduled Studio has one state-dependent Start/End action in the header after Share and before the account menu, with pending/disabled state and End confirmation. Existing permissions remain enforced.

## Explicit unresolved disconnect rule

A direct stream needs a signal-loss grace period and automatic public End. The current implementation does NOT add a 60-second timeout. Existing interruption detection uses verified waterline age; that is unsafe as an auto-End clock because worker backlog is not sender disconnection.

Before enabling timeout, define a durable arriving-media clock, atomic reconnect/timeout arbitration, the exact last admitted public boundary, old-capture补傳/Stop/seal completion, and delayed output recovery without opening a replacement event. Public End and transport completion are different. The historical 16-minute same-live recovery expectation cannot silently remain unchanged after such a policy change. Windows must not implement an independent timeout or claim this missing rule passed.

## Measurements and costs

Compare normal single-service OBS load with identical content, bitrates, machine and upstream; keep extra competing captures as a separate stress case. Record times (Asia/Taipei plus UTC) for OBS output start, first closed fragments, each rendition declare/confirm, worker claim/stage/pointer publication, applied public Start, grant, first decoded frame and notification receipt. Record capture/output/recording IDs, plugin commit and all deployed versions. Media badge/status alone is not first-frame evidence.

Per 2.5 hours, normal media objects grow from approximately 900 to 2700 across three renditions. At 65 hours/month, approximately 23400 to 70200, excluding playlists/init/retries/copies. Byte storage remains approximately bitrate × time, with extra container overhead. Request and worker process overhead increases; do not infer total cost triples. Confirm currently lists the whole staging prefix, so both batch count and listing page count must be counted. Measure request classes, API CPU time, allocated Job CPU/memory × wall time, idle executions, validation throughput and queue growth. No new event bus or capacity expansion is included.

The local benchmark compares moving FFmpeg media with 300/900-frame segments and controlled 0/50ms provider-operation latency. It excludes Azure scheduling, R2 networking, real NVENC and viewer decoding. Results during competing local tests are not a capacity/cost estimate. Production latency and costs remain unverified until Windows A/B evidence.

## Release and minimum Windows work

Producer-first: Asset migration/API + validation worker + member-media Worker, then CMS, then Admin. Read back commits, revision/digests, health/traffic and existing writer flags. Only then allow a new opted-in capture. Existing captures and legacy CLI packages are not rewritten.

Windows can implement the opt-in/journal and local media validation against this fixed supplement in parallel. Before true OBS testing, confirm no meeting is in progress and the user's authorization remains valid. Run one legacy baseline and one S10 short capture under the same normal load; verify all three resolutions, first playable frame, marker boundaries, normal Stop/seal/ready, same-URL VOD, one live-start notice and no duplicate VOD notice. Verify scheduled header Start/End separately from direct automatic flow. Do not rerun already-passed unrelated 2.5-hour/credential cases merely for this change. Longer S10 throughput/cost measurement is a distinct new-profile acceptance gate.

Preserve historical 379/388-second recovery failures, weak-network seek limitations and unverified normal-meeting-plus-YouTube load. No existing recording deletion, manual publication, key reset or retransmission is authorized by this document.
