# Direct signal completion — d1-2026-10-17.rc1

This additive lifecycle supplement completes the pending disconnect rule in S10 `s10-2026-10-17.rc1`. K4 `k4-2026-10-15.rc1`, B1 `b1-2026-10-10.rc1`, C1 `c1-2026-10-08.2`, inventory schema 1, canonical digest, membership authorization and full media validation remain unchanged. S10's optional mediaProfile is still required for 300-frame output. There is no new public request field, plugin permission, public End button or input slot.

## Authoritative signal and public End

Only an already-public, source-managed direct event (no scheduledAt, autoStart and autoEnd enabled) uses the timeout. Scheduled events and never-public preparation retain their existing rules. Normal accepted Stop follows the existing automatic End path and does not wait for the signal timeout.

Asset persists the server time of a new `.m4s` confirmation changing from declared to queued. Heartbeats, repeated receipts, repeated confirmations, playlist/init uploads and verification progress cannot renew this clock. New arrivals remain effective even while verification is backlogged. Legacy in-flight captures with no stored timestamp stay waiting until a new media confirmation; timestamps are not fabricated during migration.

The next arrival is expected one capture segment interval after the last new media confirmation: 10.01 seconds for 300 frames, 30.03 seconds for 900 frames. After that expected time, signal is interrupted. The grace deadline is another 60 seconds later. This is a server policy, not a claim that YouTube uses this exact formula. The existing CMS reconciler checks every 30 seconds, so timeout claim adds up to one polling interval under normal scheduling, plus processing/network delay. The UI does not show a misleading exact countdown.

A capture transaction serializes new confirmation and timeout claim. Until a claim commits, a new media confirmation can recover the same event. Once claimed, the cutoff is immutable: the largest contiguous common prefix confirmed in all three renditions, with endSequenceExclusive equal to the first missing common sequence. Queued bytes qualify for choosing the cutoff, but never for publishing unverified media. CMS still applies its existing range policy and waits for the exact verified projection before recording public End or showing the terminal acknowledgement. Validation failure cannot become successful End or publication.

The private CMS-only POST `/priv/recording-captures/{captureId}/broadcast-signal` requires owner provenance and `{ "closeIfIdle": true|false }`. Omitted/null/unknown input is invalid. Its state is waiting, healthy, interrupted or ended, with nullable lastMediaConfirmedAt, deadline and endSequenceExclusive. `false` reads an existing cutoff without creating a new one. Frozen cutoff recovery precedes normal inventory-End fallback even if Stop/seal arrived while a prior CMS response was lost. This private state does not expand the public BroadcastHealth enum. The endpoint is not exposed by Gateway.

## Original output and Windows work

A server-frozen public boundary is not an encoder ACK request. The existing B1 status exposes endSequenceExclusive and end_pending/processing without requiring a pending end command. The plugin must recognize this state, persist the terminal public range with the original output identity, and finalize only its independent HHC output at a common encoding boundary. It must not stop OBS global streaming, YouTube or local recording; must not issue a second public End; and must not open a replacement output automatically.

If offline, keep the original journal, outputSessionId, profile and capture. On reconnect, discover the frozen range before resuming public-event assumptions. Upload retained original-capture bytes, perform normal Stop/seal with the complete actual inventory, and let existing validation/publication finish. Do not truncate inventory to the public cutoff, fabricate segments or change digest rules. Late fragments outside the public range remain private and do not extend the already-ended public URL. If a common prefix was missing at timeout, later supplementation does not retroactively enlarge that range.

The global occupied-input reservation remains until the existing accepted Stop/seal release rule is met. Public End alone cannot steal it. A later explicit user Start may create/bind the next event only through the existing atomic direct resolver, after release. Do not loop Start automatically because a key is reusable. Historical 16-minute same-live reconnect evidence remains valid for the old policy; after a timeout claim the correct new behavior is original-capture completion and a separate explicitly started public event.

## Console

Direct mode has no public Start/End control. During grace the bounded 16:9 monitor retains a single dark status chip, state dot and no-signal label; the reconnect hint sits below the monitor. No manual load-preview button or invented countdown is added. A real preview failure remains independently visible.

An original open direct page observes server-confirmed public completion and shows one `直播已結束` acknowledgement. Confirm does not Stop, seal, publish, delete or rotate anything. It re-enters the existing direct resolver with a fresh source revision and new intent. Network retries preserve that intent; conflicts re-read authority. Multiple Console tabs still resolve the same next idle event. The old URL remains the recording URL. The dialog does not wait for complete VOD validation/publication. New deep links to a completed event go to recording management.

While the previous output still owns admission, the next page explicitly waits for its upload completion. Merely acknowledging the dialog cannot release the slot. Scheduled mode retains the single state-dependent header Start/End control between Share and account.

## Verification gates

Local tests cover timestamp persistence, duplicate confirmation, no-media/short-profile/legacy behavior, incomplete renditions, immutable late upload cutoff, explicit private close policy, owner rejection, cutoff recovery after Stop/seal, original-page acknowledgement and retry, and occupied-input presentation. Browser fixtures demonstrate UI transitions, not actual OBS or decoded media acceptance.

Windows must verify short loss/recovery, timeout, original-page acknowledgement, late original-capture supplementation, release/next explicit Start, normal Stop, and scheduled isolation. Record actual server observation times rather than claiming an exact 60-second wall-clock End. Public End may await verified bytes after cutoff claim. Preserve previous 379/388-second recovery and weak-network seek evidence; do not recast those as passing. S10 startup latency, request/worker costs and long-run throughput still require measured real OBS evidence.
