# Recording capture, automatic publication and member live (Tasks 3–4, 8)

The ten C1 Admin capture routes are implemented. They are human-only,
actor/recording/capture bound and return the existing C1 envelopes. Create requires
write permission; requesting either live exposure or automatic publication also
requires publish permission. Status requires read permission. Cancel automatic
publication and close live require publish permission. Other mutations require
write permission. Only seal accepts an 8 MiB JSON body; other capture inputs are
limited to 256 KiB. Status accepts one cursor and one limit (1–1000), rejecting
unknown, duplicated or malformed query keys.

Migration 077 adds a capture aggregate, operation receipts, pending Asset commands,
a server-side scope registry and the recording edit generation. All capture and
scope changes lock the parent recording before its capture row. Capture ownership
excludes browser source and direct package attachment. A partial unique index
allows at most one active capture for each recording.

Create commits its intent before contacting Asset. Failed or response-lost calls
retry the original operation key and stored uploader identity, without retaining
an access token. Asset success binds the immutable capture ID. An expired/deleted
intent cannot be revived by a delayed create response; its durable abort command
is delivered after recovering that ID. Completed operation payloads are discarded,
while digests and receipts remain. Pending command payloads are bounded to 9 MiB. Asset-facing upload operations
are capped at 20,002; stop, abort, close-live and cancel each have a separate
32-operation reserve, so upload receipts cannot exhaust lifecycle capacity.
A new abort of a known-ready capture is rejected without an Asset command. If
Asset completion wins an accepted abort race, its authoritative ready media state
settles the durable command while live stays closed and automatic publication
stays cancelled. The original CMS receipt remains; no Asset abort receipt is
fabricated. Parent deletion or media failure cannot be revived by that reply.
Receipts replay their original acceptance time and return the current local state.

Stop records a receipt and `stopAcceptedAt` without freezing Asset intake. The
scope registry permits existing same-user scopes to resume, but rejects new scopes
after stop. Declare/sign/confirm remain available for the tail until seal, expiry
or abort. Seal requires a prior stop and `normalEnd=true`; Asset validates the full
inventory and confirmation evidence before CMS records acceptance. Queued objects
are never treated as verified or playable. Existing VOD readiness validation is
unchanged; package attachment from an accepted seal does not set recording ReadyAt.

Abort, expiry and observed permanent Asset failure close live admission/renewal,
cancel pending automatic publication and persist an owner abort command. The
fixed CMS deadline is 24 hours after its original intent creation; retries do not
extend it. A verified normal completion can establish replayUntil, capped by
endedAt + verified duration + 30 minutes, the capture deadline and an existing
shorter recording retention deadline. A completed capture reaching that deadline
is not converted into an incomplete-capture failure from live cutoff alone.
Asset unfinished-package validation retains its existing 24-hour deadline; an
explicit Asset expired/failed result still cancels pending publication. No producer
deadline is extended. Scope admission checks the
current deadline directly, independent of reconciliation wakeup.

The existing recording worker replays pending commands and polls Asset status
with the persisted uploader identity. It currently runs on its existing five
minute cadence. Human title, cover, publish, unpublish and delete operations
advance the edit generation. Unpublish closes live and cancels pending publication
in the same transaction, including draft recordings with an active capture or pending automatic publication;
delete additionally terminates capture intake. Closing live alone preserves
capture intake and its automatic publication intent; cancelling publication alone
preserves live exposure. Old receipts cannot reopen either.

Automatic publication is driven by the existing backend worker after normal seal,
matching ready capture/package and independent owner-bound VOD validation. Internal
`VerifiedReadyAt`, `PermissionCheckedAt`, attempt count and next retry time are
stored in the capture aggregate; C1 wire schemas remain unchanged. Migration 078
indexes ready pending intents. Temporary Asset/Account/audit/transaction failures
keep the intent pending with exponential retry (one minute to 64 minutes), observed
on an independent 30-second publication cadence with a startup pass. Capture and
cleanup maintenance retain their five-minute cadence. Cover inheritance clears the
obsolete dependency retry delay; eligible publication is picked up on the next
publication pass. Restart resumes the durable intent. This is a polling cadence,
not a promise of publication within 30 seconds of seal.

Each attempt checks the original authorizer's active account and exact
`cms:recordings:publish` via Account `/priv/account/v1/permissions/verify`. No
user token is retained. A denial or changed human edit generation durably blocks
the intent. Account checking and CMS commit cannot be atomic across services;
the successful check time is recorded and each retry rechecks it. Revocation is
not promised to be instantaneous across this boundary.

Publication locks the parent recording, then rechecks intent, edit generation,
normal-end/stop proof, exact capture/package and verified readiness. It reuses the
existing publication/notification transaction without advancing human generation.
The published intent, version, one `cms.recording.capture.publish` audit and first
notification commit together; a missing audit outbox fails closed. Repeated workers
or replay cannot republish after manual unpublish. A cancel arriving after
publication returns current `published`; later expiry/deletion preserves that
completed intent evidence while closing exposure. Close-live and cancel-auto remain
independent. The `cms.recording.capture.block` audit commits with a blocked intent.

Capture audits use only the catalog's fixed service executor `hhc-web-api`, with
original `authorizerId`, exact `captureId` and `intentId`. The existing notification
command remains account-bound to the original authorizer; publication's `updated_by`
records the service executor. No human HTTP provenance is synthesized for publish.
Release the exact Account probe and Audit catalog producers before activation.

Member `GET /api/member/recordings/live` and
`POST /api/member/recordings/{id}/live/playback` use the existing gateway identity,
recording entitlement and current member legal checks. Authorization failures
retain the existing `policy_acceptance_required`, `access_required` and
`authorization_unavailable` envelopes; media failures use C1 capture errors.
Both responses are private/no-store with no-referrer. Discovery removes stopped
captures; only pre-stop registered scopes can request ending/ended playback.

Progress uses Asset's published common three-rendition waterline. Three complete
segments permit startup. Ninety seconds without advancement (including startup)
means interrupted. Advancing backlog stays recovering until verified media end
is within 120 seconds of the server-created capture timeline. Client wall clocks
are never read. Stop means ending; only normal seal plus producer ENDLIST evidence
means ended. Each grant and reconciliation enforces expiry and terminal failure.
Completed captures remain in reconciliation while live exposure is enabled.

Grant issuance takes the DSR subject lock, then the recording and capture locks,
and retains them through Asset's ten-second bounded HTTP call and transaction
commit. It validates durable scope ownership and stop policy, ensures a trace
receipt when the existing rollout is enabled, and commits scope admission before
returning credentials. A close that commits first rejects issuance; a close that
arrives during issuance waits. Already issued credentials can remain valid for
at most five minutes; exchange credentials last at most sixty seconds on Asset.
The exact live media path is `/videos/{recordingId}/captures/{captureId}/sessions/{scopeId}/master.m3u8`.
No credential or user access token is persisted. Existing VOD routes and readiness
checks remain unchanged, and draft captures do not enter VOD discovery/playback.

Any publish-capable human staff member can close live or cancel automatic
publication; upload/status/stop/seal/abort remain bound to the uploader. Local
control receipts additionally bind the acting staff account, including replay;
nonowner control responses remove object inventory and cursors. Admin GET
recording adds optional `capture: {captureId, liveEnabled, liveState, autoPublish}`;
this summary never appears in member VOD responses. Closing live cannot reopen
that capture and retains automatic publication; cancelling auto retains live.

Viewer scopes are identity/activity data, not anonymous telemetry. Migration 080
indexes subject export/erasure. The existing `website_watermark` DSR bundle adds
`recording_live_scope` records after recording receipts via `live-scopes:` cursors;
reader-v1 coverage remains the same nine identifiers. The existing Account
exporter passes generic record types through unchanged. Scope writes are always
DSR-fenced, including when trace rollout is disabled. Restriction/erase counts
include scopes; erasure deletes them under the same subject lock, and the existing
purge worker deletes them at the fixed capture deadline. Governance inventory
records that maximum 24-hour retention and leaves legal activation pending.
Live trace receipts reuse the existing recording watermark table and 365-day
policy, with the capture's immutable ID retained by its final VOD package.

Immutable Asset validation, Worker delivery, producer releases, deployment
configuration, and real browser/device acceptance remain independent gates.
No production resources were changed.
