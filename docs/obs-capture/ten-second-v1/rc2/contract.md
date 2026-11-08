# S10 limits correction — s10-2026-10-18.rc2

This supplement corrects the incomplete S10 rc1 CMS schema. The old snapshots
remain immutable historical artifacts; do not use their four old ceilings for
an opted-in S10 output. `s10-2026-10-18.rc2` is a document/schema revision, not a
new HTTP `protocolVersion`. K4 open and D1 `controlVersion=d1-2026-10-17.rc2`
remain unchanged. Inventory schema 1, digest, receipts, public boundaries,
member authorization and full media validation remain unchanged.

## Exact limits and profile conditions

| Field / resource | Shared schema ceiling | Runtime condition |
| --- | ---: | --- |
| RecordingRendition.segmentCount | 4320 | Only inventory presetVersion `obs-hls-10s-v1`; every other preset remains at most 1440 at inventory validation. |
| RecordingPackageInventory.objects | 13000 | Aggregate bound for all profiles; excludes package.json. Exact unique contiguous object graph is additionally required. |
| RecordingCaptureStatus.declaredObjects | 13000 | Aggregate declaration count, not the size of one status page. |
| RecordingLiveProgress.lastSequence | 4319 | Zero-based inclusive index; -1 means no verified segment. Not D1 public cutoff. |
| CMS durable upload operations | 26002 | Create + up to 13000 individual declarations + 13000 confirmations + seal. Existing local control budgets stay separate and bounded. |

The rendition/status schemas are shared response types and do not carry the
profile. Their numeric bounds describe the supported envelope, not permission
to use 4320 legacy segments. The persisted capture profile is authoritative:
S10 requires K4 open `mediaProfile=obs-hls-10s-v1`, its exact echo and a durable
journal; CMS sends Asset `segmentFrames=300`. Missing/legacy profile is 900.
Asset rejects a seal inventory whose preset does not match the capture. A
package inventory with any other preset rejects segmentCount >1440. The
13000-object declaration/listing bound applies to both profiles; it does not
allow extraneous objects or legacy media to bypass the seal limit.

12 hours (43200 seconds), 10 GB including inventory, 8 MiB inventory, 128 MiB
per object, 1 MiB per playlist, batch size 100 and status page size 1000 remain.
The first limit reached wins. 4320 is a structural ceiling, **not** a promise
that 4320 full 10.01-second segments fit: at exact 30000/1001 fps, 300 frames per
normal segment, 4315 full segments occupy 43193.15 seconds; a legal final short
segment must still keep the complete actual media at or below 43200 seconds.
Never truncate inventory, invent segments, alter duration, change to 30 fps or
weaken short-interior/hash/probe/decode checks to fit a schema.

The shared schema checks shape and numeric bounds. Profile eligibility, exact
object graph, canonical digest, actual media and cross-rendition timing are
additional runtime checks. `fixtures.json` is schema-only metadata, never
uploadable media or evidence of successful decoding. The contract regression
also generates the complete 12967-object structural graph for 4320 segments
per rendition, and tests 13000/13001 array ceilings and 4320/4321 counts. Padded
array-ceiling cases intentionally do not satisfy runtime graph validation.

## Why rc1 was inconsistent

Asset API/DB/worker had the expanded bounds; CMS canonical OpenAPI and its S10
snapshot retained 1440/10000/1439. The S10 snapshot test covered only open-input
and profile echo. It never validated large inventory/status/progress payloads.
The separate CMS operation budget also retained 20002, so individual-object
requests could exhaust it before a full S10 capture finished. Neither finding
is represented as an observed production failure.

This correction aligns the four CMS schemas with the already released Asset
schemas and expands CMS operation capacity to match Asset. New tests validate
serialized CMS types, positive/negative boundary fixtures, complete inventory
shape, exact current ingest schema closure, both producers' shared schema
properties, large client responses and finalization at the expanded operation
budget. Legacy snapshots are checked with only these explicit supplements
normalized; all other fields and hashes stay fixed. D1 rc2 is not changed.

## Fixed artifacts and verification scope

- `cms.openapi.snapshot.yaml`: current ingest routes and transitive component
  closure, including K4 open, D1 rc2 control, capture query/upload/Stop/seal.
- `asset.openapi.snapshot.yaml`: existing private capture/package producer
  routes and their component closure. This grants OBS no private API access.
- `fixtures.json`: shared schema boundary cases; the Go regression generates
  large arrays and serializes real capture/status types separately.
- `manifest.json`: hashes, immutable base contracts and exact Asset source.
- `SHA256SUMS`: all fixed files, excluding itself.

CMS needs the PR merge and CI/CD release for the operation-budget fix. Asset,
recording worker, member-media Worker, Gateway and Admin need no change for
this correction. Read back the CMS deployment and existing producer version;
report those separately from local tests and CI. A schema pass does not prove
real OBS, startup speed, validation throughput, cost or overall acceptance.

## Minimum Windows continuation

1. Verify this fixed commit and hashes; replace the S10 rc1 schema overlay with
   rc2. Keep K4 wire, D1 rc2, inventory 1 and the journal format unchanged.
2. Run strict local schema/decoder tests: 1440/1441/4320/4321 rendition counts,
   10000/13000/13001 declarations/inventory lengths, -1/1439/4319/4320 progress.
   The shared rendition schema accepts 1441 for S10, while legacy profile
   inventory eligibility remains capped at 1440. Never use schema validity as
   proof of a valid inventory graph or decoded media.
3. Resume the already planned S10+D1 short integration test. Do not create a
   huge real capture merely to exercise a numeric ceiling. Preserve the same
   outputSessionId, negotiated profile, captureId, epoch, D1 cutoff and full
   inventory during recovery. Query/renew/upload/normal Stop/seal keep their
   existing order and semantics; public End is not input release.
4. Before true OBS testing, confirm no gathering is in progress and existing
   authorization still applies. Report actual installed plugin commit, IDs,
   Asia/Taipei timestamps, profile echo, frame rate/segment frames, decode and
   finalization results. Do not repeat passed player/2.5-hour tests for this
   schema correction alone; S10 performance/cost acceptance remains separate.

No key reset, replacement capture, old-material reupload, manual publication
or diagnostic recording deletion. Historical 379/388-second delay and weak
network limitations remain recorded. Never share credentials or signed URLs.
