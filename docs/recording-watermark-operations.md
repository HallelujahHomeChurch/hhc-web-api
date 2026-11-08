# Recording watermark operations

Implementation candidate; not production acceptance. No R2, Worker, CLI, upload,
HLS, thumbnail, notification, or media-retention changes.

## Issuance and investigation

Each authorized member/recording pair receives one cryptographically random
10-character Crockford Base32 code. Playback exposes it only after receipt commit;
renewals reuse it without extending its 365 × 24 hour expiry. Unique constraints,
bounded collision retries and the shared subject advisory lock protect concurrent
requests. No historical receipt is inferred from legacy eight-character HMACs.

`POST /api/admin/recordings/watermark-lookups` accepts only `recordingId` and `code`
in a bounded JSON body, not query parameters. Human `cms:recordings:investigate`
permission is mandatory. Ten attempts per actor/minute include malformed codes,
malformed JSON and misses; unrelated weekly investigations have their own budget.
The dedicated role is unassigned. Recording write/publish and weekly investigation
permissions do not imply this permission; service credentials cannot obtain it.

Identity is returned only after the audit outbox and lookup transaction commit.
Audit failure returns 503 with no result. Audits contain a result enum, operator
and recording UUID, never the supplied code or recipient UUID/name/email. A
restricted recipient is indistinguishable from a missing receipt. As in weekly
investigations, a restricted/erased operator is rejected before new trace rows can
be created. Invalid recording IDs use unrelated audit resource UUIDs.

The UI says **account issued this code**, not proven leaker. Only users with the
existing `users:read` permission can fetch account details or follow their link;
otherwise the exact issuance UUID is shown. No extra identity projection/cache.

## Data rights and retention

The existing `website_watermark` owner now includes recording receipts. Existing
weekly UUID and `investigations:` export cursors remain valid; `recordings:` is a
final UUID-keyset stage, with per-page counts. Account's archive builder must drain
all stages before publishing an export. The existing `personalized_bulletins`
scope identifier remains a compatibility identifier for this shared owner;
privacy/UI wording must explain its recording coverage before activation.

Restrict/erase/account cleanup and independent restriction withdrawal reuse the
existing shared subject fence. Erasure deletes recording receipts and the actor's
local lookup audits. Media deletion does not cascade to issuance receipts.
Expired receipts cannot be resolved/exported even before purge. The existing
publication worker purges expired receipts, 365-day local lookup audits and
two-day hashed minute counters; retries are idempotent. Central operator security
audits use the existing `standard` retention policy, not a recipient receipt copy.
Hashed counters are pseudonymous, not anonymous.

Backups retain their established expiry. After restore, replay deletion and
restriction requests and drain expired records **before** reopening investigation
or playback issuance. Table deletion is not proof of backup deletion.

## Release and rollback

Deploy Audit catalog + Account permission before CMS migration/runtime, then
Gateway, SDK, Admin and Web. Do not grant a role or alter privacy statements as an
incidental deployment step. Privacy notice approval and permission assignment are
separate gates. Keep the additive receipt migration on runtime rollback; never
drop receipts or synthesize historical associations. Native video fullscreen may
omit the DOM watermark; it is not a DRM/recording-prevention mechanism.

Verify matching receipt, renewal stability, wrong recording, legacy code,
expiry, missing audit, insufficient permission, service actor, DSR and cleanup
on disposable data. Real-device fullscreen and 10% visual readability remain
separate acceptance evidence; unit tests do not establish screenshot attribution.
