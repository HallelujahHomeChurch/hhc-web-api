# Bulletin publication controls

The existing default PDF action preserves already-authorized exact-source online
work. It never grants disabled authority. Explicit `pdf_only` and saved-draft
recovery require `BULLETIN_EXPLICIT_ONLINE_PUBLICATION_ENABLED=true`; the default
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
one transaction. It leaves an existing online publication available; withdrawal
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
