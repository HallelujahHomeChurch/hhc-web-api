# Automatic online conversion uploads

Scope: general Traditional Chinese PDFs from issue 1739. Other uploads retain
their existing workflow. Neither extraction nor this release publishes content.

## Reviewed migration preview

Migration 086 expands the UNIQUE key on
`hhc_web.online_bulletin_extraction_job`. It replaces the old key
`(issue_id,series,content_locale,source_asset_checksum,template_version,extractor_version)`
with the same key plus `source_asset_id`, restricted to active (`NOT superseded`)
jobs. This also permits A→B→A uploads without modifying superseded snapshots.
It adds a partial outbox lookup index for the editor's current-source status.
Every existing row remains valid; no
rows, revisions, snapshots, checksums or publication pointers are removed.
The snapshot immutability trigger and lease fencing remain active. A different
upload object gets independent evidence even if its bytes are identical.
The migration policy pins this exact expand-only file by SHA-256; any alteration
requires a new migration. CI tests both that pin and real PostgreSQL behavior.

The ALTER takes a table lock during UNIQUE index construction. Assess table size
and long-running transactions in the normal release what-if before merge. Run
the migration only through the repository release job, never a local prod command.

## Two-stage rollout

1. Release this consumer and migration through normal CI/CD. The producer
   defaults to disabled (`BULLETIN_AUTO_EXTRACTION_ENABLED` unset/false). Verify
   runtime health and the unchanged manual extraction flow. This artifact is the
   minimum rollback target for the activation release.
2. Enable automatic extraction in a subsequent reviewed CI/CD release only after
   that verification. Do not activate by an ad hoc production command.

The activation release wires `bulletinAutoExtractionEnabled` to the API only;
Bicep's default remains false. Its workflow defaults the reviewed producer on,
unless repository variable `BULLETIN_AUTO_EXTRACTION_ENABLED` is explicitly
`false`. Both flags accept only lowercase `true`/`false`; enabled automatic
conversion requires `BULLETIN_EXTRACTION_ENABLED=true`. What-if and deployment
use the same value, and readiness verifies it on the actual serving revision.
Emergency disable uses that override and a normal main release, not a local
Container Apps update. Rollback copies the prior compatible revision with its
prior producer configuration, not just its image.

Once enabled, do not roll back behind the compatibility artifact: older workers
do not recognize upload requests. Emergency-disable the producer while retaining
the compatible consumer; pending requests can then finish safely.

## Smoke and recovery

Migration 090 adds a nullable paired human authorizer/request to extraction jobs;
existing rows remain unchanged and do not automatically authorize conversion.
New jobs retain upload/extraction authorization on retries. A matching Traditional
Local with resolved review and exact native layout proof automatically queues a
Simplified draft. Pending Incoming comparisons, changed PDFs and missing legacy
authorization block automatic queuing. Review and publication remain separate.

- Upload a supported clean PDF; inspect online extraction status after scan.
- Verify Simplified waits for unresolved Traditional findings, then queues once
  after verified layout/review. Retry completion must not duplicate the job.
- Replace its PDF; verify authored Local/publication stay intact and Incoming
  arrives for comparison. Repeat with identical bytes/new upload ID.
- Scan-pending PDFs must defer; infected or wrong-owner assets must not parse.
- Audit and queue failure must leave the outbox request retryable. Failed source
  requests appear in the editor and can recover using the existing retry action
  after the asset becomes clean/ready.

If release fails, use the existing runtime rollback workflow to the compatible
artifact. Do not restore the
old UNIQUE constraint once multiple upload objects share a checksum, and do not
delete evidence to make it fit. Keep the new migration and forward-fix rather
than rewriting evidence. Verify the deployed revision and API route separately
from local tests.
