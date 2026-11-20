# Bulletin publication controls

The ordinary PDF action rejects an active automatic publication plan with HTTP
409 `automatic_publication_active`, including when the capability flag is off.
It never silently cancels that plan. Explicit `continue_automatic`, `pdf_only`
and saved-draft recovery require `BULLETIN_EXPLICIT_ONLINE_PUBLICATION_ENABLED=true`; the default
is false. No runtime manifest or deployment enables it in this change.

Before activation, release every HTTP/extraction/publication worker with epoch
support and verify compatible writers; approve the new administrative security
history's legal basis and retention in `docs/data-governance.yaml`. These remain
separate acceptance gates. Old binaries cannot preserve epochs after activation.
Disable new controls for rollback while retaining compatible readers/writers and
all migration114 tables/receipts. Do not roll back to a pre-epoch writer, remove
receipts, or replay uploads as recovery. A flag-off compatible writer continues
fencing any edition which already has epoch history.

Migration114 adds no historical grants or inferred cancellation. An actual new
cancellation of a legacy active flag starts with a cancelled transition; it never
credits that canceller with authorizing the historical work. A known present
completion starts with completed, without a synthetic authorized transition. Unknown false flags
remain unknown. New authorizations use monotonic per-edition epochs; superseded
workers cannot regain authority. Terminal rows and recovery receipts are append
only. Source deletion does not delete their administrative security evidence.
No retention duration, automated cleanup, Account erasure or backup purge is
claimed; the inventory marks their policy pending, with manual review required
before activation. Raw operation keys are bounded idempotency identifiers and
must contain no personal/member content.

PDF-only checks canonical and online versions, including zero for no document,
then cancels old automatic work before starting the independent PDF workflow in
one transaction. When that exact PDF is already published, the action instead
requires its matching published projection and real completed workflow. It cancels
only the pending automatic plan and returns that original completed workflow,
without another PDF, Asset or notification command. Its aggregateVersion remains
the original publication version; refresh the issue for the current canonical
version. A real plan change advances canonical and edition versions and records
a version-update audit. For an already-published PDF, already-disabled plans are
read-only no-ops; stale CAS or unavailable publication proof rejects the operation.
It leaves an existing online publication available; withdrawal
uses the separate unpublish operation. Its notification checkbox governs the PDF
action; existing generation-zero notification deduplication remains unchanged.

Recovery requires current human publication scope, online If-Match, exact
canonical/revision/layout hash and an actor-bound Idempotency-Key. It checks saved
server-owned source, semantic and measured-layout evidence and publishes through
existing CMS confirm/publication transactions. It creates no extraction, semantic
planning, layout or notification command. Successful Traditional publication may
use the normal Simplified derivation boundary. The receipt and administrative audit
commit with publication; original extraction authorizer/evidence stay immutable.
Replaying the exact actor/key/input returns the original outcome after withdrawal
without republishing. Changed payload conflicts. Qualification failures expose
only allowlisted blocker codes; they never remeasure or manufacture qualification.

Explicit `continue_automatic` can publish the PDF early only when the server can
prove the current automatic request, source, metadata and epoch still own a
healthy extraction/update/layout chain. Scan-stage or unknown work, a stale or
failed current plan, or any PDF workflow already bound to that current update
returns 409 without side effects. An unrelated historical failure does not block
a new healthy plan. The action neither joins nor repairs an owned workflow and
does not promise later recognition will succeed. Disabled or absent authority
remains disabled or absent. Clients refresh after conflicts and follow the
current workflow rather than silently retrying with a newer version.

The early-PDF UI requests `notifySubscribers:false`; the original automatic
notification preference remains unchanged. An explicit true request still uses
the existing generation-zero notification deduplication. PDF-only means a real
change of plan and cancellation, not early PDF with automatic work retained.
Human confirmed manual Online publication and qualified saved-draft recovery
remain explicit atomic takeovers; neither has a blanket active-plan rejection.
Recovery receipt replay remains read-only even after a later authorization.

## Independent publication for new automatic requests

A publisher's newly authorized general Traditional Chinese edition receives an
independent-release record. Attaching the PDF and delivering the scan event use
the same intent. Asset API must confirm the issue owner, edition, purpose,
private visibility, completed upload, clean scan, ready processing, PDF type and
SHA-256. The delivery records that source and schedules the ordinary protected
PDF projection in parallel with extraction. The PDF worker checks the asset again
before committing. It does not issue a public asset grant.

Recognition, semantic acceptance, review and measured layout still gate the
online release. A verified current source can publish online while the PDF
workflow is pending or failed. Each projection remains available until its own
successful replacement or explicit withdrawal. Member downloads generate their
personal watermark on demand. A selected subscriber notification uses the
existing generation-zero idempotency key and requires a published PDF link.

The ordinary manual PDF action requires automatic publication to be disabled.
The online editor can confirm and publish the measured revision of a verified
source independently. A manual online publication or qualified recovery can finish the online branch of an active independent request. Its PDF workflow and original notification choice remain authorized. Qualified recovery binds the same source and revision.

### Deployment and rollback

Deploy the additive migration before the compatible API and workers. Existing
epochs and outstanding deliveries without an independent-release record retain
the original sequencing. Do not backfill a record from an old automatic flag.
A pending attachment delivery resumes its captured request after a restart.
A stale, replaced or cancelled delivery cannot create a fresh authorization.

Before rolling back, stop accepting new automatic requests through the
administrative entry points. Keep the compatible API, PDF outbox worker and
extractor running until the new requests are terminal, or retain compatible
workers for those requests. Inspect pending and processing PDF outbox events,
the workflow linked by each independent-release record, and the corresponding
online intent, update and layout jobs. Resolve or explicitly cancel outstanding
requests before routing them to an older release. Retain the additive schema,
immutable publications and receipts. Reopening an edition requires a fresh
publisher action.
