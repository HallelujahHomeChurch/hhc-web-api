# Bulletin watermark operations

Integrated invisible-only implementation candidate, 2026-09-11. Release does not
activate membership. Final Linux browser decision parity is verified; CMS runtime
deployment, activation and authenticated live acceptance require separate live verification. Historical receipts remain in the private repository archive.

## Data and retention

CMS owns receipts: opaque random code, Account UUID, published issue/locale/revision,
owned source Asset UUID, source/output PDF hashes, private 61-bit fingerprint,
model/PDF pipeline/privacy versions, per-page canonical PNG hashes/dimensions,
issuance and expiry timestamps. No new names, email, IP or user-agent fields. Counts are
issuance counts; retries and interrupted transfers are not completed downloads.

Receipts expire after exactly 365 × 24 hours. Lookup refuses expired records even
before physical removal. The existing publication worker removes expired receipts,
trace audits, investigation evidence/metadata and completed DSR action receipts in
bounded batches. Idle cleanup checks run once per minute. Failures log a content-free error and retry after one minute; backlog is
drained on subsequent ticks. Rate counters keep hashed subjects for at most two
days plus cleanup lag. Erased-subject hashes fence late in-flight writes for 365
days; active processing restrictions remain while that restriction applies.
No automatic release of a restriction is exposed by this feature.

DSR exports use UUID keyset pages and **per-page** recordCount. Account is the only
caller allowed to export, restrict or erase. Erasure is idempotent and blocks new
receipt insertion or trace lookup by the subject, including stale authorized
requests; actor trace audits are also removed. Processing restriction also prevents
other investigators from resolving retained receipts for that subject, returning
the same not-found result as an absent receipt. Subject locks serialize lookup
with restriction and erasure. These APIs do not replace manual
review/export of unrelated website content. Database backups must follow their
existing expiry; after restore, replay deletion/restriction requests and run expiry
cleanup before exposing lookup/download traffic. Do not claim backup deletion from
an online table DELETE.

## Temporary bytes and helper

The runtime includes the offline CPU helper `/hhc-web-bulletin-watermark`, pinned
Python libraries/Poppler and readonly hash-verified Q models. It reads a private original and
writes a private personalized PDF; CMS streams only a fully rendered file with a
committed receipt. No output Blob/cache is created. A process-lifetime filesystem
lock protects the dedicated temporary root from concurrent cleanup; startup removes
all prior render directories, even if the helper binary is unavailable. Normal
completion/failure/client cancellation removes the request directory. Container
storage must remain ephemeral and private; no shared host temp volume.

One shared render/detect slot per replica, 20 MiB source, 32 MiB output, 60 pages,
180-second render deadline and a 300-second investigation deadline covering slot
wait, template reconstruction and detection. Cancellation kills the helper process
group. The packaged worker disables core dumps and caps CPU time at 300 seconds,
file size at 32 MiB and descriptors at 128. Container limits supply the memory/CPU
boundary; private path validation alone is not a sandbox. Unsupported/encrypted/signed/damaged PDFs fail closed.
Do not return originals to recover a member download failure. GET ignores Range
in member mode; HEAD does not issue or transform. Public mode retains originals.
No metrics/log labels contain trace codes, user UUIDs, document text or names.

## Verified local Linux evidence

The final packaged Linux 32-candidate browser holdout matched all 192 frozen macOS
decisions: **35/48 positives matched**, 13 remained inconclusive, and **144 negatives
produced zero attributions**. Positive matches were 12/12 native PNG, 10/12 native
JPEG70, 8/12 600-pixel PNG and 5/12 600-pixel JPEG70. These finite observations do
not guarantee arbitrary crops, mobile/LINE recovery or zero population error.

Measured packaged 32-candidate detection stayed below 13.44 seconds. The separate
64-candidate check measured 161.46 seconds for templates and 14.89 seconds for the
corrected frozen-query detection, with a reported total of 176.36 seconds (phase values are rounded),
with peak memory below 936 MiB. These separate phases exclude queue wait, source
fetch and startup and are not combined CMS request latency or a 64-candidate
quality matrix. Preserve the first combined attempt separately: its query was a
later-overwritten Admin UI capture, not a PDF screenshot, and it was inconclusive.
The frozen 192-case inputs and hashes were unchanged.

## Configuration and activation

`BULLETIN_WATERMARK_HELPER` defaults to the packaged executable; local developers
may point to an absolute built helper path. `BULLETIN_WATERMARK_PRIVACY_VERSION`
has no default: it must identify the reviewed, actually published privacy notice.
The helper and privacy version apply only to retained watermark investigation and
historical issuance processing. The protected member API streams the authorized
private source PDF and has no public or unmarked-fallback route. Bulletin access is
controlled by exact Operations entitlements; staff investigation access is
separately controlled by `cms:bulletins:investigate`.

## Issuance and screenshot investigations

New PDFs use only the 61-bit invisible fingerprint. The opaque receipt reference
is retained as a CMS record identifier and is not printed into a document. There
is no visible-renderer selection, old-style rendering path or unmarked member
fallback. Retained legacy receipt records do not become valid reconstructable
fingerprints by guessing a payload or changing their renderer version.

Admin retrieves eligible scopes from
`GET /api/admin/bulletins/{issueId}/watermark-versions`, then submits
`POST /api/admin/bulletin-watermark-investigations` with a UUID `Idempotency-Key`
and exactly two multipart fields: `metadata` JSON and `image` PNG/JPEG. Metadata
contains the exact scope. The worker locates the source page automatically. Uploads
are limited to 10 MiB and 20 million decoded pixels. Creation returns 202 plus a job ID;
`GET /api/admin/bulletin-watermark-investigations/{id}` returns creator-owned state.

Scope binds issue, locale, revision, source Asset, source SHA256, PDF pipeline and
model. The worker reconstructs the complete applicable 2–64 candidate set and
checks persisted per-page metadata. Overflow, missing source, changed pipeline or
invalid candidate fails; never truncate the set, choose a nearby revision or emit
a best guess. Candidate-set changes are checked before committing a result.

`cms:bulletins:investigate` is checked at submission and read. Only the creating
investigator can retrieve the job. Restriction,
erasure, receipt expiry and required audit persistence also gate a released match.
`users:read` separately
gates the account-details link; it does not grant investigation permission.

Raw screenshots expire after at most 24 hours and are cleared on processing
completion or applicable DSR actions. Investigation metadata is retained for 365 × 24 hours; screenshots are not included in the metadata export.
The worker uses a 10-minute lease and bounded retries. Expired evidence must not be
requeued by restoring a local file. Logical expiry applies before cleanup runs.

The safe result states are queued/running/completed/failed/expired, with matched or
inconclusive only where applicable. Only a completed match may expose permitted
receipt fields. Do not log or display candidate lists, fingerprint payloads,
rankings or confidence percentages. Matching an issuance does not establish that
the account holder personally shared the file.

Model/runtime upgrades require a new pipeline identity and reconstruction parity.
Never relabel old receipts to a new runtime. Apply additive fingerprint and
investigation migrations before the new binary; keep a compatible healthy runtime
or disable member downloads/investigations during rollback. Runtime provisioning
and exact hashes are documented in [the helper README](../tools/bulletin_watermark/README.md).

## Coordinated rollout

Keep membership inactive throughout. Publish Engagement's additive source/filter/
callback contract first (old CMS exact machine keys remain accepted). Publish CMS
DSR/policy/renderer before Account begins mandatory deletion callbacks. Then publish
Account's fixed-permission audience/trace and durable deletion contract, followed
by Notification's callback/legacy reference worker. During producer rollout, retry
unavailable dependencies; inspect real queues before changing revisions. Publish
shared clients before upgrading frontend consumers. Each repository needs its own
PR, green CI, merge, immutable release and revision/health smoke.

Do not send live Email/Push, assign roles, change the public switch, publish legal
copy or enable membership merely to obtain a green smoke result. Separate the
release of inactive code from activation and actual member/device acceptance.
