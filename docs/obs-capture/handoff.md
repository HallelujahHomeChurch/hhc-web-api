# OBS capture contract — C1 snapshot

The original C1 wire snapshot is pinned in `c1-manifest.json`. Current deployment,
OAuth and Windows integration entry points are supplied in
the historical integration handoff and its manifest in the private repository archive.
The C1 snapshot remains awaiting Windows consumer acknowledgement; deployed
entry points do not establish real NVENC or end-to-end acceptance.

## Ownership and wire format

The canonical public contract is `../../openapi.yaml`. Asset owns `asset-api/docs/openapi.yaml`; Account owns `account-api/docs/openapi.yaml`; Gateway owns its host/method/body policy. Public paths in the CMS document are relative to `/api`. Use the separately supplied S1 test origins; do not point the plugin at production merely because OpenAPI lists the production host.

JSON keys are camelCase. Recording and human user IDs are UUIDs; capture IDs are 32 lowercase hexadecimal characters. `playbackScopeId` is a UUID registered by the server to a viewer/recording/capture; possession of a client-generated UUID is not authorization. Time values are UTC RFC3339 with a `Z` suffix. Preserve server deadlines; local clock changes never extend them.

Create the recording with the existing title API and durable `Idempotency-Key`, then create its capture with `operationKey`, `expectedVersion`, explicit `autoPublish` and explicit `liveEnabled`. `expectedVersion` is the single optimistic precondition for capture creation; it does not also require an `If-Match` header. Existing title/publish APIs keep their current `If-Match` rules. `autoPublish` or `liveEnabled` requires publish permission in addition to write. The uploader identity is the original human; the initial capture endpoints reject service principals.

Operation keys match `[a-zA-Z0-9._:-]{1,128}`. They are bound to actor, recording, capture (once created), operation and canonical request content. Persist one key per logical action; retry with the same original request after an uncertain response. Same key and content returns the immutable acceptance receipt plus **current** capture state. Different content conflicts. A receipt never reopens a closed capture or proves it is still live/published. Declaration replay must match path, size and SHA-256; paths are unique and immutable. Sign requests issue renewable capabilities and have no durable operation key.

## Producer sequence

1. Create capture; record its fixed creation time and 24-hour upload expiry. Same-recording active captures cannot overlap. Poll before recovering an uncertain create.
2. Declare at most 100 closed objects, then sign at most 100 declared paths. Each object is 1–134217728 bytes; package total including the final inventory is at most 10000000000 bytes and at most 10000 objects. Playlist files additionally retain the existing 1 MiB limit. Paths reuse the canonical package convention: `master.m3u8`, `{480p|720p|1080p}/{index.m3u8|init.mp4|seg-NNNNNN.m4s}`. Segment numbering starts at 000000. Do not upload changing playlists during capture; declare final playlist bytes when complete.
3. PUT only the exact closed bytes to the single-object signed URL. It lasts at most 15 minutes and never beyond capture expiry. Never store URLs or tokens in journals/logs. On an uncertain PUT response, poll/confirm instead of rebuilding the recording.
4. Confirm at most 100 paths. A 202 receipt means durable verification was queued, **not** that media is valid or publicly playable. Status pagination is lexical by path, limit 1–1000; `nextCursor` is the last examined path or empty at the end. Continue traversing all pages. `declared`, `queued`, `verified` and `failed` refer to each object.
5. Send `stop` promptly after persisting local stop intent. Its acceptance closes admission of new viewer scopes but leaves tail uploads open. If offline, show pending synchronization. Do not equate local stop with server acknowledgment.
6. Once normal encoder shutdown and every closed object are confirmed, send `seal` with the full canonical package inventory and `normalEnd=true`. CMS requires a prior stop receipt; Asset atomically freezes only the complete matching inventory. Missing objects returns 409 and remains uploadable. Inventory digest uses the existing package canonicalization, including sorted paths/renditions; the fixture digest is synthetic and must not be used as real media evidence.
7. Poll through freezing/validation/ready. `ready` is not publication. Background auto-publication rechecks the authorizer and human-edit generation; manual title/cover/description/publication changes cannot be overwritten by an old intent.

Crash, encoder failure or incomplete finalization must not fabricate normalEnd. `abort` reasons are bounded in the schema. A terminal capture refuses new writes and grants; keep failed local media for explicit recovery. Normal completion and subsequent live replay expiry must not undo a legitimate VOD publication.

The three media profiles remain H.264 1080p/3 Mbps, 720p/1.5 Mbps and 854×480p/800 kbps, each AAC 128 kbps/48kHz/stereo. Live validation accepts measured CFR of 30 or 30000/1001 fps; all profiles and segments must keep the same verified rate. Segments are 30 seconds with a shorter final segment permitted; maximum duration is 12 hours. Wire metadata is a declaration; actual codec/timing/hash/complete-package validation remains mandatory.

## Playback and controls

Member list/playback requests use the website origin and current member/legal entitlement. Live EVENT history begins at sequence zero and never slides older entries out. Only the common contiguous **verified** waterline of all three renditions is visible; queued objects/staging are never readable. `lastSequence=-1` means no playable common segment. Three common segments are required for startup. Capture state, live state, and autoPublish state are distinct fields.

An issued live grant lasts at most five minutes and is bounded by capture expiry, replayUntil and any earlier media retention deadline. The exchange credential itself follows the existing one-minute body-only exchange; it is not the five-minute media cookie. A known scope can reauthorize after its previous grant expired, but no new scope may register after stop. An empty expired browser credential does not justify generating a new scope. The same capture/scope URL remains stable across refreshes. Preserve user DVR position/pause intent; only followLive recovery or explicit return-to-live moves to the safe live point.

Normal end sets `replayUntil=min(endedAt+verifiedDuration+30 minutes, captureExpiresAt, earlier media deadline if present)`. Abnormal terminal or manual closure denies reauthorization even before that deadline. `DELETE .../auto-publish` cancels only publication intent. `DELETE .../live` irreversibly closes that capture's live exposure, keeping recording/publication intent. Whole-recording unpublish/delete closes live and cancels pending publication. These DELETE control requests carry their bounded `operationKey` JSON body.

## Failures

- 400 `capture_invalid`: correct the request; do not retry unchanged.
- 401: refresh once or sign in again; preserve media and account-bound queue. Trusted gateway/auth middleware may return the existing `unauthorized` envelope.
- 403: permissions/account boundary failed; stop automatic retries. Existing auth middleware may return `forbidden` rather than a capture-specific code.
- 404 `capture_not_found`: do not create a replacement automatically.
- 409 `capture_conflict`: key/content/state mismatch; poll and reconcile. `capture_missing_objects` permits missing tail objects to be confirmed, then retry the same seal intent.
- 410 `capture_expired`: fixed deadline reached; retain local media for explicit recovery.
- 413 `capture_too_large`: quota/body exceeded; do not split one logical immutable object to bypass limits. Gateway errors can be non-JSON; branch on status first.
- 429 `capture_rate_limited`: honor Retry-After, capped backoff and jitter.
- 503 `capture_unavailable` and other transient 5xx: bounded retry with jitter, poll after uncertainty. Do not infer successful acceptance from a timeout. Reject malformed/unexpected success payloads.

## Developer mock

From this repository root run `go run ./tools/obs-capture-mock`; it listens only on `127.0.0.1:8789`. Or build a Windows executable with `GOOS=windows GOARCH=amd64 go build -o obs-capture-mock.exe ./tools/obs-capture-mock` on a development machine, copy it together with `openapi.yaml` and `docs/obs-capture/fixtures.json`, then launch from the copied directory. This is developer tooling; operating the finished OBS plugin does not require a CLI.

The mock loads schemas from canonical OpenAPI and validates every example at startup and JSON request bodies at runtime. Set `X-OBS-Mock-Scenario` to `conflicting_key`, `missing_objects`, `expired`, `forbidden`, `rate_limited` or `temporary_failure` to exercise response handling. Responses use fixed synthetic IDs/times; they do not echo arbitrary input, maintain state, authenticate, issue working grants, accept R2 PUTs or simulate media validation. Signed URLs use `.invalid`, and all media/hash examples are synthetic. Do not send real credentials to the mock. It proves wire parsing/error handling only; receipt replay, ownership and real media require producer integration tests and S1/F1/V1.

Verification: `go test . ./tools/obs-capture-mock -count=1`. Account setup preview is maintained in that repo's `docs/obs-capture/`.

## Direct integration workflow

Mac delivers the deployed platform, its verified origins and immutable contract;
Windows delivers the plugin and tests upload directly against those APIs. Manual
F1/F1-L transfer to a Mac folder is not a prerequisite for development, deployment
or integration. Request selected media/hash evidence only when diagnosing a real
failure. The Windows-reported 29.97 fps short and 2.5-hour local tests are useful
producer evidence; they do not establish deployed validation, playback or latency.
Record deployed revisions, plugin revision and observed results for short and long
runs. S1 is ready only after deployed routes, authentication and media access have
been verified. Operational member live use still requires end-to-end acceptance.

## Immutable snapshot retrieval

`c1-manifest.json` revision `c1-2026-10-08.2` records owner repository commit, path,
SHA-256, byte count and immutable GitHub URL for each artifact. Fetch the pinned
commit in each authorized checkout, verify the bytes against SHA-256, and read
CMS OpenAPI with its fixtures. Build the mock from that CMS commit using its
committed go.mod/go.sum. The Windows amd64 cross-build passed on Mac; execution on
Windows has not been verified. No executable or credentials are embedded here.

This candidate can be used to implement wire parsing without waiting for a merge.
Record the revision, verified hashes, Windows plugin commit and accepted or
rejected items when returning the C1 consumer receipt. Any later wire change must
produce a new manifest revision and consumer acknowledgement. The current
`windowsAcknowledgement: null` is deliberate. S1 authenticated origins and real
media access are a separate handoff; the mock is loopback and synthetic only.

## Final seal with fractional frame rates

Inventory `schemaVersion` remains 1. Declare actual contiguous segment count and
actual media duration. Eight segments totalling 240.206633 seconds at 30000/1001
are not nine segments: do not calculate count with `ceil(duration/30)`, manufacture
a segment, shorten the duration, or change frame rate. The 1–1440 segment bound
and exact inventory object graph remain enforced. Final playlist count/EXTINF,
actual decoded packet boundaries, hashes, codecs, CFR, IDR and cross-rendition
alignment remain authoritative. Only the final segment may be short; short
interior segments remain invalid. The Windows 900-frame/30.03-second segmentation
repair is independent and does not justify weakening this rule.

Fixture `seal_2997_eight_actual_segments` demonstrates the wire shape using fake
hashes; it is not uploadable media. Follow deployed release evidence separately.
