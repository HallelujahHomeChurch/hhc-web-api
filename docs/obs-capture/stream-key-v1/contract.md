# K1 event stream key contract

Contract: `k1-2026-10-13.rc1`. This is an additive event credential contract;
B1 remains `b1-2026-10-10.rc1`, C1 remains `c1-2026-10-08.2`, inventory schema 1.
The B1/C1 snapshots and media verification requirements are unchanged.

Each key belongs to exactly one broadcast/recording UUID. YouTube supports reusable
streams, but this HHC delivery intentionally isolates keys per event. A key never
selects the nearest scheduled event, creates another event, or changes the transport
to RTMP. Enter it in the **HHC OBS panel**, not OBS's native RTMP settings.

## Human authorization and management

OAuth `hhc-obs` PKCE S256, existing loopback callback rules, current Account
permissions and gateway trust headers remain mandatory. Keys do not replace user,
actor, capture, epoch or command checks. They do not grant member playback access.

- GET `/api/admin/broadcasts/{recordingId}/stream-key` needs read scope and returns
  `configured`, `canRotate`, `revision`, nullable four-character `suffix` only.
- PUT at that path needs write AND publish scope, writer gate, `operationKey`,
  `expectedRevision` and `streamKey`. Response is the same metadata envelope.
- Admin generates 32 CSPRNG bytes, encoded without padding in base64url:
  `hhc_live_<canonical lowercase recording UUID>_<43 base64url characters>`.
  The exact UUID must match the path. Fixtures use synthetic nonsecret bytes solely
  for format validation; clients MUST use cryptographic randomness.
- Persist only SHA-256 and the suffix in private broadcast state and sanitized
  mutation intent/audit. No new SQL table or backfill is required. Legacy broadcasts
  default to no configured key. Public and B1 views have no new fields.
- The browser keeps the credential only in component memory. After reload, reset
  before first binding to obtain another credential; there is no recover/reveal API.
  Ambiguous PUT retries MUST reuse the same key, operation key and revision. Never
  automatically generate another key after a network failure. Successful replay
  does not increment revision. Replay after a subsequent rotation conflicts instead
  of falsely confirming an obsolete credential.
- Configuration/rotation is limited to draft or scheduled, before any capture intent
  or epoch/public start. Even a producer-pending intent blocks rotation. Reset is
  not a running-session revoke action; existing emergency-close and stop/abort
  semantics remain the relevant controls.

## OBS binding and recovery

Feature-detect K1 using the dedicated metadata endpoint. Parse the recording UUID
from the configured key and GET that broadcast to display the exact target. POST
`/api/admin/broadcasts/{recordingId}/stream-key-bindings` with `streamKey` plus the
unchanged B1 bind fields (`operationKey`, `expectedRevision`, `protocolVersion`).
It requires write scope, and publish scope when autoStart or autoPublish is enabled.
A supplied wrong or malformed key returns `403 broadcast_stream_key_invalid`.
A missing required JSON field, or malformed management input, returns
`400 broadcast_invalid`; existing B1 conflicts remain.

Key validation occurs under the same broadcast row lock before both replay and new
capture reservation. The global single-input advisory lock and active-capture
limit include C1 and B1. No key or second device can preempt an active capture.
The unkeyed B1 endpoint continues to accept only events without configured keys.
There is no fallback to unkeyed binding or a new C1 capture on K1 failure.

Current CMS implementation returns HTTP 200 on successful binding. A producer
failure can return 503 after the capture intent and immutable bind receipt have
committed. Retry the original key/operation/revision and GET the broadcast to
recover; retries resume that same capture intent. Windows must retain the existing
B1 consumer handling of HTTP 202 and null binding: poll until binding becomes
non-null before C1 upload. Do not infer capture readiness from acceptance alone.

After binding, existing B1 control polling, actor checks, marker journal and C1
uploads remain unchanged. Persist a configured key only in Windows credential
storage if needed. Raw keys must not enter OBS profile text, wire diagnostics,
operation journals, telemetry or logs. Same OAuth actor possession remains the
existing continuation boundary; K1 does not add independent device authentication.

## Immediate and scheduled semantics

Immediate creation uses `scheduledAt:null`, while scheduled creation sets a time.
Both create a private draft first. If autoStart is enabled, explicitly announce
before OBS binding; existing B1 refuses unannounced autoStart. Scheduling alone
never starts or stops output. Announcing or configuring a key never starts public
playback. Existing verified-media and common-segment marker gates still apply.
Manual public End does not stop OBS, YouTube or local recording.

30000/1001 fps, 900 frames / 30.03-second segments, complete inventory/digest/media
verification and public `[start,endExclusive)` are unchanged. Private preview is
not member publication; normal stop/seal/backfill/archive stays under C1/B1.

## Delivery and rollback

Release CMS before consumers, preserving the new JSON fields on rollback. Do not
roll back to a pre-K1 writer while keyed events exist: an old writer could discard
unknown JSON fields or accept legacy unkeyed binding. Disable new writers first
and retain K1-aware binding/configuration code until keyed events are resolved.
Disabling writers alone does not make an old reconciler JSON-safe. If a pre-K1
writer has mutated keyed state, treat the credential as lost and require explicit
reconfiguration where still eligible; never infer that restoring K1 restores the
old digest. Prefer a K1-aware forward fix. There is no separate K1 feature flag.
No production data, configuration, release or Windows hardware acceptance is
established by these source tests or snapshots.
