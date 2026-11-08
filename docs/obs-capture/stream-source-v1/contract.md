# K2 reusable broadcast source — k2-2026-10-14.rc1

K2 is additive to frozen K1, B1 b1-2026-10-10.rc1 and C1 c1-2026-10-08.2. One installation has one reusable source credential. OAuth human identity and existing scopes remain mandatory. No RTMP/cloud relay, media-profile change, schedule guessing, or fallback to C1.

## Wire

All routes are human-only `/api/admin`, no query parameters, private/no-store, no-referrer. JSON envelopes retain `{data,meta,error}`. Canonical schemas are in root `openapi.yaml` and the adjacent frozen snapshot.

- GET `/broadcast-source` (read) returns `BroadcastSource`: revision, configured, canRotate, inputBusy, suffix nullable, targetRecordingId nullable.
- PUT `/broadcast-source/stream-key` (write+publish) accepts operationKey, expectedSourceRevision, streamKey. Returns source metadata.
- PUT `/broadcast-source/target` (write+publish) accepts operationKey, expectedSourceRevision, recordingId, expectedRevision (event revision). Explicitly prepares an eligible event. Returns source metadata; fetch event again for its revision.
- POST `/broadcast-source/resolve` (read) accepts streamKey. Returns sourceRevision, recordingId, broadcastRevision.
- POST `/broadcast-source/bindings` (write; publish when locked autoStart/autoPublish policy requires it) accepts streamKey, operationKey, expectedSourceRevision, recordingId, expectedRevision, protocolVersion. Returns unchanged B1 BroadcastMutationResult.

Credential format is `hhc_stream_` plus canonical unpadded base64url of 32 CSPRNG bytes. Admin generates the value and retains it only in memory for copy/retry; OBS uses OS credential storage. CMS persists only SHA-256 and four-character suffix, never raw secrets or request bodies. No reveal endpoint. Reset requires entering the new key into OBS once.

Revision starts at 1. Fresh key/target operations increment source revision once. First source adoption increments event revision once; subsequent target selections do not alter event revision. Bind leaves source revision unchanged. Management replay returns current metadata only if its resulting source revision remains current; otherwise 409 broadcast_operation_conflict. Rotation is allowed for an idle prepared target, but forbidden while any reserved/live input exists. canRotate is the inverse of inputBusy, not target eligibility.

Target selection rejects existing K1 credentials and events with capture intent, non-draft/scheduled phase, revocation, deletion, or completed public boundary. Adoption is durable. Legacy bind/K1 bind and per-event credential rotation reject source-managed events. K1 metadata remains unchanged and canRotate=false for these events. Existing unrelated K1/B1/C1 clients continue unchanged.

Target preparation may precede initial credential configuration; resolve requires a configured valid key. Resolve checks current credential and the explicitly prepared target. An already reserved/live, autoStart-but-unannounced, missing, finished, cancelled, or otherwise ineligible target returns 409 broadcast_source_not_prepared: OBS waits with backoff, retaining credentials. Resolve never changes data. New bind atomically checks source revision, target ID, target revision, current credential and current policy under the existing input reservation lock. A durable operation ledger pins actor, sanitized digest, original target, capture intent, epoch and source revision. Replays validate the current key before the receipt and never create an intent for a different target or epoch. Superseded/terminal intent replay conflicts. Persist the exact operation/input across 503 and restart; do not resolve a new target while retrying an uncertain old bind.

Errors: 400 broadcast_invalid; 403 broadcast_stream_key_invalid or forbidden; 409 broadcast_source_busy, broadcast_source_not_prepared, broadcast_operation_conflict, or existing B1 state errors; 412 broadcast_revision_conflict; 422 broadcast_protocol_unsupported; 503 unavailable with Retry-After. Server success is 200; OBS preserves B1 202/null polling compatibility and GET recovery. No error or metadata contains raw credential/hash.

## Input occupancy and compatibility

Capture.Active(), reconciliation and the same-recording active uniqueness remain unchanged. A separate generated input_active column releases global input occupancy only for a B1 capture with normal end, accepted stop, immutable sealed end boundary, freezing/validating state, and a successful durable seal receipt bound to its capture ID. Pending/error seals remain occupied. Generated derivation applies to old writer UPDATEs as well as new code; incomplete historical evidence conservatively stays occupied. Full media verification and public [start,endExclusive) projection remain unchanged.

One sealed capture may verify in the background while the next input starts. Asset's existing per-actor capacity of two uploading/processing captures is unchanged; another capture may wait for capacity. K2 introduces no quota increase.

Migration adds singleton state, permanent operation ledger and source_managed, then replaces only global B1 occupancy uniqueness with input_active. No existing K1 credential is migrated. Producers and gateway release before consumers. After source adoption, rollback must use a K2-aware binary or forward fix: pre-K2 binaries do not enforce durable source ownership. Do not run older writers and assume a later upgrade repairs missing authorization checks. Existing B1/C1 immutable fixtures and media acceptance remain mandatory.
