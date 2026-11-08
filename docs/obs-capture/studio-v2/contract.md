# K4 durable output bootstrap

Version: **k4-2026-10-15.rc1**. Status: **consumer implementation contract; endpoint not deployed**.

This contract is additive to K3 `k3-2026-10-14.rc1`, B1 `b1-2026-10-10.rc1`, C1 `c1-2026-10-08.2`, inventory schema 1. It does not change media, digests, B1 command/ACK boundaries or member access. The frozen snapshot and positive/negative wire fixtures are authoritative for syntax. This document is authoritative for retry and lifecycle semantics. The manifest pins both new artifacts and unchanged legacy snapshots/fixtures.

## 1. Why another bootstrap

K3 resolves a Console-prepared target then binds it. Its request has no durable output identity and **must retain that behavior**. K4 allows a source with an explicitly configured direct-live delegation to create its next direct event without an open Console. It must distinguish a fresh sender start from retries, reconnect and crash recovery.

K4 combines target resolution/creation and binding completion into one request. This replaces the preliminary two-step open-then-bind proposal in the design plan, not the already released K3 path. The server owns partial-operation recovery; clients never race a source revision between selection and binding.

No OBS login, event selection, metadata editor, schedule or public Start button is introduced. This is HLS/fMP4 ingest, not RTMP. Named sources and Console direct/scheduled controls are platform concerns; Windows sees only one saved reusable key.

## 2. Request

`POST https://admin.alive.org.tw/api/ingest/streams/open`

Content-Type application/json. No query parameters. Maximum 4096 bytes. Body has **exactly**:

| Field | Requirement |
| --- | --- |
| protocolVersion | `k4-2026-10-15.rc1` |
| outputSessionId | Lowercase canonical nonzero UUID, persisted BEFORE first request |
| streamKey | Existing canonical `hhc_stream_` encoding of 32 random bytes; OS vault only |

Persist a K4 **opening intent journal**, not just active.json, before sending the first POST. The Windows `retireUnbound()` path currently treats an absent intent as safe to release; that inference is invalid after any possibly-sent K4 open. A persisted opening/stopping intent survives timeout, cancellation and restart until the same output is resolved and cleaned up.

One outputSessionId per explicit user Start. The existing Windows localId is suitable. A timeout, 409, 429, 5xx, retry, key settings reopen or process restart is never a reason to generate another ID. No title, recordingId, sourceId, schedule, expected revision or client-selected actor is accepted.

Gateway strips cookies, public identity headers and irrelevant Authorization; key authenticates this bootstrap. No body/secret logging, caching or automatic proxy retry. No arbitrary external caller may invoke the private CMS route.

## 3. Successful response

HTTP **200**, standard `{data,meta,error:null}` envelope. data contains exactly:

- protocolVersion and echoed outputSessionId.
- sourceId: nonzero canonical UUID, informational; clients must not use it to choose a target.
- broadcast: unchanged B1 BroadcastView; **binding is non-null**.
- receipt: unchanged B1 receipt with operation=`bind`, state=`completed`, operationKey=`<outputSessionId>.bind`.
- session: unchanged K3 opaque event-scoped token/expiresAt/recordingId/captureId/epoch. Store secret only in the OS vault; do not journal token.

Check echoed version/outputSessionId, receipt operation/key/state, session.recordingId == broadcast.recordingId and session.captureId/epoch == broadcast.binding.captureId/epoch before installing the response. `identity-fixtures.json` contains schema-valid responses which must nevertheless be rejected for identity mismatches; schema validation alone is insufficient. Source and binding identities become immutable local intent. A mismatching response is a protocol error; preserve state, do not silently accept a different event.

HTTP 202/null binding is **not** a K4 success. Binding completion pending on the server yields 503 and a same-ID retry. Old B1 202 handling remains only on the legacy path.

After this response all transfers, status, control, ACK, stop, seal, abort and renewal use the **unchanged K3 routes**, B1/C1 bodies and validation. The token remains opaque; its signing lookup changes internally from singleton to its bound source, not to the newest target. There is no K4 client-side binding request.

## 4. Durable server resolution

1. Validate request and authenticate key, then resolve its source/generation. Recheck the configured Console delegate's active publication permission. No implicit delegation from whoever last viewed the page.
2. Look up outputSessionId in that source's durable operation ledger before selecting any target. Same output repeats the original intent. Retain identity tombstones through event deletion; an old output must never become a new event. If a generation changes, the previously consumed ID must not be resurrected under the same source.
3. For a new ID, acquire source ownership and the global capacity slot atomically. A prepared scheduled target wins. Without one, direct mode must be explicitly configured/authorized; use its waiting draft or create one from the stored direct settings.
4. Persist source/generation, output ID, recording ID, Console delegate, bind operation key, captured revisions and capture intent in the same ownership transaction. Complete external capture creation using that durable intent. A failure after this commit cannot reselect a target on retry.
5. Return 200 only when the original binding is complete. The current BroadcastView and newly issued session expiry may change across replies; immutable output/recording/capture/epoch/receipt identity cannot.

Before a capture intent is persisted, a reservation abandoned for 120 seconds may release the receiving slot but must retain output→recording resolution. Same output retries the same recording after fresh admission; it does not create another. Once binding/capture creation might have been sent, no TTL may release ownership blindly: reconcile the persisted intent first. Another output cannot steal an uncertain capture.

A different output encountering occupied input receives a conflict. It never attaches to the active capture even with the same key. Capacity=1 also applies across different keys. Future scheduled metadata alone takes no slot.

## 5. Selection and lifecycle

- Console direct entry and OBS open race through the same atomic draft selection: at most one current direct draft per source. Console viewing an archived event never implicitly creates another.
- Direct mode: after valid media, automatically establish the public start boundary. No extra operator Start. Normal sender stop closes at the existing common boundary. Transport loss does not imply stop/end.
- Scheduled mode: prepared event receives preview; Console Start establishes the public start boundary. Scheduled time is informational, never a time-triggered publication gate.
- Console End closes this HHC event only; it never stops global OBS streaming, YouTube or local recording. The sender drains its existing journal and drives Stop/seal.
- Successful normal End + accepted Stop + immutable successful seal evidence releases input. The next explicit Start may proceed while the prior archive is processing, subject to the existing per-actor capture limit.
- Public End is not transport completion. If the original capture still needs transfer, Stop or seal, same-ID open returns its original completed binding/session, even after Console End; this also recovers a lost initial response. Only an output with settled transport (accepted normal Stop + successful seal, or completed abort/expiry/deletion fencing) returns terminal and never a fresh event. A sender with its existing session/journal continues original status/cleanup via K3 rather than calling open.
- Never-public preview has no public archive range and is not automatically published. Existing false/record-only policies are not rewritten. Newly public events publish automatically after full verification and independent authorization.

Signal dropout, permission loss and normal Stop remain separate states. No relaxation of verification or fabricated boundary is permitted.

## 6. Errors and sender decisions

Errors retain the unchanged K3 `{data:null,meta:{},error:{code,message}}` shape. Do not classify by translated message. Preserve output ID and journal on **every** error.

| HTTP / code | Sender action |
| --- | --- |
| 400 `broadcast_invalid` | Invalid body/ID; stop automatic retry, report protocol issue |
| 403 `broadcast_stream_key_invalid` | Invalid/revoked key; ask operator to check saved key, never auto-reset |
| 403 `forbidden` | Source delegate lacks authority; preserve intent/media and wait for Console/operator recovery |
| 409 `broadcast_source_not_prepared` | No prepared target and direct not enabled; wait |
| 409 `broadcast_source_busy` | Same source occupied by another output; wait, never select event |
| 409 `broadcast_output_capacity_exhausted` | Global input slot occupied; wait |
| 409 `broadcast_active_limit` | Existing per-actor processing limit; wait |
| 409 `broadcast_output_terminal` | Original transport settled or definitively fenced; no automatic reopen |
| 409 `broadcast_operation_conflict` | Output identity conflicts with persisted intent; stop retry and retain evidence |
| 422 `broadcast_protocol_unsupported` | Upgrade required; no silent fallback to K3 resolve |
| 429 | Respect Retry-After; preserve ID |
| 503 `unavailable` | Disabled feature/dependency failure/uncertain in-progress bind; retry same ID |
| 404/405 | Endpoint absent or wrong version/deployment; no fallback or ID reset |
| 413 | Fix oversized request; no automatic retry |
| network/timeout/other 5xx | Outcome unknown; retry same ID |

Wait/transient retry: exponential 1,2,4,8,16,30 seconds capped at 30 with ±20% jitter; honor larger Retry-After (no early retry). Do not retry after explicit user cancellation except the existing bounded recovery/cleanup process. Unknown 4xx is not a signal to create a new output.

For a never-bound output cancelled locally after uncertain response, keep the ID marked stopping and recover the result with that same ID; if server binding exists, use the existing abort/cleanup path. Do not discard the journal or claim that local cancel proves no remote capture exists.

If publication permission is removed after the server binds but before the first response arrives, open remains 403; it must not mint a normal session and merely label it cleanup-only. This case needs an authorized Console operator to restore delegation or explicitly reconcile/revoke the original input. Preserve the opening intent and server ownership; do not release an uncertain capture on a timer. Existing K3 cleanup-only recovery remains available only under its existing authenticated, durable revocation rules. This is an explicit operator-recovery limitation, not automatic recovery acceptance.

## 7. Compatibility and secrets

- Upgrade with an existing K3 journal: complete/resume that journal through K3. Never convert an already resolved intent into a K4 open. New explicit outputs use K4 only after client support is configured by its version, not by guessing from HTTP failures.
- Initial singleton migration preserves the current key value, hash/suffix and signing seed. K3 source-management compatibility routes address only that migrated legacy source, never an arbitrary newly added source.
- K3 session renewal remains bound to its original recording/capture/epoch/delegate. Rotation, revocation, expiry and cleanup-only rules remain as released. No new account privilege is conferred by key possession.
- C1/B1 wire, digest, idempotency and public range are untouched. Full media validation and 30000/1001 fps, 900-frame/30.03-second segmentation remain mandatory.
- Fixtures contain deliberately synthetic, non-production placeholders. Never replace them with production tokens or keys.

## 8. Deliverable versus readiness

This contract package includes schema, valid/invalid fixtures, Go input types/validation and acceptance scenarios. It does **not** prove repository atomicity, permission enforcement, deployed routing, source migration or media success. Those are Mac M2–M4 and Windows real-device gates.

Windows may implement/CI against this fixed candidate now. Formal OBS tests wait for a separate deployment handoff with actual revision, flags, health, and authorization. Do not point new clients at production expecting K4 until that handoff.
