# D1 control — d1-2026-10-17.rc2

This version corrects D1 rc1's claim that the plugin's existing control response exposes public boundaries. It did not: both Admin and key-only ingest returned only revision, phase and pendingCommand. The rc1 document remains historical evidence; Windows must implement this explicit rc2 negotiation before D1 timeout finalization.

S10 s10-2026-10-17.rc1 (30000/1001 fps, 300 frames/10.01 seconds), K4, K3, B1 b1-2026-10-10.rc1, C1 c1-2026-10-08.2, inventory 1, canonical digest, public [start,endExclusive), complete media validation and old journal compatibility remain unchanged. No new permission, endpoint, key, capture, migration or feature flag is required.

## Request and response

Use the original event-scoped ingest bearer and original identity:

GET /api/ingest/broadcasts/{recordingId}/control?captureId={originalCaptureId}&epoch={originalEpoch}&controlVersion=d1-2026-10-17.rc2

The Admin control route supports the same opt-in. Neither route exposes the private Asset broadcast-signal endpoint. Responses are private, no-store.

The standard success envelope is {data, meta, error:null}. data contains exactly:
- revision: existing positive broadcast revision.
- phase: existing B1 phase.
- pendingCommand: existing B1 command or explicit null.
- controlVersion: exactly d1-2026-10-17.rc2.
- recordingId: original recording UUID.
- captureId: original bound capture ID.
- epoch: original positive binding epoch.
- boundaries: {startSequence, endSequenceExclusive, revision}, from the same persisted broadcast snapshot.

The identity is read from the server binding, not echoed from the query. Windows must compare all three identity values with its durable original journal before applying state. Broadcast revision and boundary revision are separate counters. Persist the accepted identity, boundary revision and cutoff atomically before finalization; reject contradictory cutoffs or identity changes, and do not roll durable state back on an older response.

Both sequence fields are required but nullable. null means that boundary has not been fixed in this snapshot; absent fields or absent version/identity are an incompatible response, never permission to infer a boundary. Zero is a valid fixed sequence and must not be treated as absent. The boundary revision is always required and positive. The JSON schema validates shape; ordering, identity and immutable transitions remain server/client state checks.

## State interpretation

| Phase | Boundary interpretation and action |
| --- | --- |
| preview / start_pending | Start may still be null; End is normally null. Never infer public exposure from input or verification progress. |
| live | Start is fixed. End is normally null. Continue ordinary B1 command handling. |
| end_pending | A pending End command with null cutoff still needs the existing encoder ACK. A non-null cutoff is already authoritative, including a timeout with pendingCommand=null: persist it and finalize the original independent HHC output. |
| processing | A non-null cutoff is final; public projection/archival processing can continue independently of original capture completion. Do not issue another public End or ACK a nonexistent command. |
| archived / failed / cancelled | Available identity and boundaries are retained. Terminal phase alone does not imply a non-null cutoff, successful publication, or permission to alter the range. Explicitly revoked events retain existing cleanup-only authorization and may reject control. |
| draft / scheduled | Existing B1 phases; an unbound event does not grant control access. A valid original capture binding is required for this route. |

Once non-null, the cutoff cannot be cleared or extended for that identity. D1 timeout atomically fixes Asset's contiguous common confirmed prefix; CMS persists that same cutoff before exposing it through this control response. There can be a reconciliation delay between Asset's claim and CMS control observation. Null during that interval is not a fallback to verified watermark or local inventory.

Late bytes, duplicate queries, reconnect, renewed sessions, normal Stop and seal with a longer complete inventory cannot extend the public range. Media beyond the cutoff remains private. This response is not an input-release signal.

## Original capture recovery and completion

1. Retain the original journal, outputSessionId, recordingId, captureId, epoch and mediaProfile.
2. Renew the original session when necessary using the existing fixed key plus original session token. Renewal preserves identity; no resolve/bind/new output is required.
3. Read rc2 control; verify version/identity, then durably record any authoritative non-null cutoff. Existing pending commands retain their B1 ACK rules.
4. When a timeout has fixed the cutoff without a pending End command, finish only the independent HHC output at its actual common encoding boundary. Do not stop OBS global streaming, YouTube or local recording.
5. Upload retained original-capture objects through the existing declare/sign/PUT/confirm flow, including bytes outside the public interval. Preserve original operation keys and retry rules.
6. Send normal Stop, then seal with the complete actual inventory, digest and normalEnd. Do not truncate it to the public interval. Resume/duplicate operations use existing receipts.
7. Poll original capture state through validation and publication. Only the existing accepted Stop/seal rule releases occupied input; public End or Stop alone cannot release an unsealed input.
8. A later user Start can resolve the next event after release. Never automatically create a replacement to work around timeout or expiry.

Normal D1 public End does not revoke sessions. Existing session lifetime, capture expiry, key revocation/rotation, owner authorization and epoch fencing still apply. An expired but otherwise valid original session can renew while the capture remains eligible; rotation/revocation or permission loss is not bypassed. Permanent Console revocation keeps its existing status/abort-only behavior. No grant is added for deleted, expired or wrong-owner resources.

## Compatibility and errors

Without controlVersion, data remains exactly the legacy three-field BroadcastControl (additionalProperties=false). Legacy schema, envelope, Go type and clients continue to apply. B1 capabilities remain B1; rc2 is negotiated per control request, not a replacement B1 protocol. New consumers must not silently fall back to legacy when completing D1.

The rc2 response uses the separate strict BroadcastControlD1 and BroadcastControlD1Envelope schemas. The route's success schema permits either envelope, selected by the query. A pre-rc2 server rejects the extra query; treat that as an unsupported deployment and retain the journal.

- 400 broadcast_invalid: invalid query shape, duplicates or unknown query fields.
- 422 broadcast_protocol_unsupported: unsupported or empty controlVersion.
- 409 broadcast_epoch_mismatch: authenticated original session but mismatching control capture/epoch query.
- 401 broadcast_ingest_session_invalid: invalid/expired/fenced session or mismatching route identity.
- 404 broadcast_not_found: absent broadcast/binding or Admin caller not owning the binding.
- Existing 403 permission rejection, 409 broadcast_ingest_revoked cleanup-only, resource expiry and retryable service errors remain unchanged. Never classify authentication/network/5xx failures as an empty input or successful completion.

See openapi.snapshot.yaml and fixtures.json in this directory; SHA256SUMS pins these and this contract. The canonical CMS OpenAPI adds only control negotiation plus the separate D1 response types. The old frozen B1/C1/K3/K4/S10 documents are unchanged.

## Evidence and Windows acceptance boundary

The CMS regression uses a real PostgreSQL repository and actual key-only ingest HTTP handlers, with external Account/Asset doubles. It checks timeout with no command, original identity, legacy response compatibility, strict version negotiation, epoch/route mismatch, expired-session renewal, late declare/sign/confirm, Stop/seal idempotency, complete longer inventory, immutable public cutoff, terminal responses and input release. Schema fixtures validate null/missing/invalid values against both the canonical and frozen schema.

These tests do not demonstrate physical OBS, real Blob PUT, decoding or end-to-end latency. Windows need only resume targeted D1 tests: one short interruption recovering before timeout; one timeout followed by original-capture recovery/renewal and complete finalization; verify private late bytes, input release and next explicit Start. Retain previous passed S10/media/long-run evidence; do not rerun it solely for this response addition. Preserve historical 379/388-second delay and weak-network seek limitations.
