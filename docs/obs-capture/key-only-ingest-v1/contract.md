# K3 key-only OBS ingest

Version **k3-2026-10-14.rc1**. Additive to K2 `k2-2026-10-14.rc1`, K1 `k1-2026-10-13.rc1`, B1 `b1-2026-10-10.rc1`, C1 `c1-2026-10-08.2`, inventory schema 1. The canonical OpenAPI and recursive snapshot/fixtures are part of this change. Existing contracts and hashes are unchanged.

## Operator workflow

Console owns immediate/scheduled event creation, title/description, thumbnail, schedule, announcement, auto-start/auto-end/auto-publish and manual Start/End. **Prepare broadcast** selects the receiving event. A schedule is informational, not a clock-triggered start. OBS saves one reusable `hhc_stream_` key in Windows Credential Manager and sends the independent HHC Program output. No Account login, event chooser, title editor or publisher exists in the new plugin path. Native OBS YouTube/local recording settings remain independent. This is incremental HLS/fMP4 ingest, not an RTMP server.

## Bootstrap and fixed intent

API origin `https://admin.alive.org.tw`; all paths below include `/api`. No OAuth token is required or honored for ingest authentication. Only Gateway may invoke CMS; external trusted identity headers are stripped.

1. `POST /api/ingest/broadcast-source/resolve` with `{streamKey}` returns the unchanged K2 `BroadcastSourceResolved` envelope: sourceRevision, recordingId, broadcastRevision. Valid key but no eligible prepared event returns 409 `broadcast_source_not_prepared`; wait with capped backoff/jitter. Never select another event locally.
2. Persist the nonsecret binding intent before POST: operationKey, expectedSourceRevision, recordingId, expectedRevision, protocolVersion. `POST /api/ingest/broadcast-source/bindings` with those fields and the saved streamKey returns the unchanged B1 `broadcast`/`receipt` plus `session`. ProtocolVersion is still `b1-2026-10-10.rc1`, not K3. The returned broadcast supplies the display-only title and binding.
3. On response loss retry the exact intent with the same key. Do not resolve/retarget or create C1 capture first. The source ledger pins the original Console preparation authorizer even when another staff member later prepares an event. Bootstrap rechecks that authorizer's active publish permission through Account; Console preparation already required write and publish. Audit/capture ownership records this explicit Console delegation, not a logged-in OBS user. Publication still uses its independent background authorizer check.
4. Binding is server-completed before 200/session. Retain old B1 202/null polling compatibility for the legacy path; never fabricate a new capture on any uncertain result.

Bootstrap bodies are 4 KiB, no query parameters. No raw key is persisted by CMS or sent to Asset. CMS keeps only its existing hash/suffix. Gateway disables retries and caching, strips cookies and caller identity, and strips irrelevant bootstrap Authorization.

## Session and restart/reconnect

`session` contains `token`, `expiresAt` (UTC RFC3339), `recordingId`, `captureId`, `epoch`. Token is opaque, signed with an independent server-only CSPRNG seed (migration 092), at most 2048 bytes, with a 15-minute lifetime bounded by the existing capture deadline. Treat it as a secret; save it only in OS credential storage if restart recovery needs it. Never decode it to select an event, log it, journal it, or use it as an Account token.

Session requests use `Authorization: Bearer <session.token>`. Renew before expiry, e.g. with 2 minutes remaining. `POST /api/ingest/broadcast-source/session` body `{streamKey,sessionToken}` returns a new session envelope. An expired but authentic original session can renew while its original capture remains within deadline; both key and session are required. Ordinary renewal rechecks the Console delegate's active publish permission and never resolves the current source target. An old sealed/validating capture can therefore continue polling after the source is prepared for the next event. Account outages fail closed with 503; preserve intent, queue and OS-stored credentials.

Current source hash, existing undeleted source-managed broadcast, binding owner/capture/epoch/intent and capture deadline are checked on each session request. Key reset, deletion, epoch supersession or capture expiry fence sessions. Permanent Console revocation fences transfer/control/ACK with 409 `broadcast_ingest_revoked`; only original capture status and abort remain available, so the reserved input can be released. Cleanup-only renewal requires the original key/session but no remaining publish grant; it cannot restore transfer or publication. A permission removal prevents bootstrap/renewal immediately; an already issued session remains bounded to 15 minutes. Abort/failed capture status and immutable receipt replay may remain readable within that deadline; existing capture logic rejects fresh writes. Resetting the source key intentionally invalidates all old sessions, including old validation status polling; it remains forbidden during active input.

Do not resolve a new target to recover an existing journal. Resume the pinned session/intent first. A genuinely finished prior event starts a fresh operation/capture for the next Console-prepared event. Source releases after normal End + accepted Stop + immutable successful seal receipt, not from disconnection or a badge. Existing maximum two uploading/processing captures per actor stays unchanged.

## Allowed event-scoped routes

- GET `/api/ingest/broadcasts/{id}/captures/{captureId}`: unchanged C1 status/inventory paging (`cursor`, `limit` only).
- POST same base plus `/objects`, `/sign`, `/confirm`, `/stop`, `/seal`, `/abort`: unchanged C1 bodies/results/digests/idempotency. Objects/sign/confirm/stop/abort body maximum 256 KiB; seal 8 MiB.
- GET `/api/ingest/broadcasts/{id}/control?captureId=...&epoch=...`: unchanged B1 polling/result; both exact query fields required.
- POST `/api/ingest/broadcasts/{id}/commands/{commandId}/ack`: unchanged B1 ACK body/result, 4 KiB.

No metadata/list/create, source target/key management, announce/Start/End commands, publish/unpublish, preview/playback grants or C1-only capture routes exist on this surface. Session cannot operate another event/capture. A sender only acknowledges Console commands at the next common encoder segment boundary.

On Console End, the plugin may finish the HHC event capture after ACK and automatically drive its existing Stop, upload drain and seal state machine. This must not stop OBS global streaming, local recording or YouTube. When OBS HHC output itself normally stops, existing auto-end policy and normal-ending state machine apply. Transient transport failure never implies End. Preserve all captured material required for upload/replay.

## Errors and validation

New 401 `broadcast_ingest_session_invalid` covers missing/bad/expired/unusable session; renew the original session, never silently switch events. Key syntax/hash mismatch returns 403 `broadcast_stream_key_invalid`. Preparation authorizer denial returns 403 `forbidden`; preserve local media and require Console recovery. Existing B1 409/412/422 and C1 capture errors remain unchanged. Dependency failures use 503 and Retry-After 5; Gateway rate limits may return 429. Unsupported/extra paths are 404, methods 405, query/body violations 400/413. Do not print body credentials in diagnostics.

30000/1001 fps, 900-frame/30.03-second segmentation, full hash/probe/decode validation, 480/720/1080 renditions, public `[start,endExclusive)` and member entitlement/legal checks do not change. CLI direct upload continues using its existing credential flow. No worker expansion, event bus, cloud transcoding or Stream fees are introduced.

## Acceptance boundary

Local mocks/PostgreSQL tests, CI and deployment smoke do not prove Windows true OBS acceptance. Windows must test no-login restart/expiry/reconnect, two events sharing one saved key with distinct capture IDs, bounded original-page recovery, normal workload playback/ABR/DVR and Stop/seal/VOD auto-publication. Preserve prior 379/388-second lag and weak-network seek failures as independent unresolved evidence until measured again.
