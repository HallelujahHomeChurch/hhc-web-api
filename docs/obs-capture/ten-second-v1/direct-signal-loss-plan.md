# Direct signal-loss completion and Console acknowledgement

User addition: 2026-10-17 Asia/Taipei. This is a required follow-up to the S10 work, not an implemented or deployed feature. Keep direct public Start/End buttons absent; scheduled retains its header controls.

## Required flow

1. Public direct live receives media normally. Use the existing single dark status surface and status dot; do not add nested pale chips, technical recovery cards or a manual End button.
2. During signal-loss grace, show the YouTube-style no-signal/reconnection presentation in the original Console, distinct from terminal End. Recovering media within grace resumes the same public event. Match current YouTube control-room wording/layout with browser evidence before implementing exact copy; do not claim an invented countdown is YouTube behavior.
3. On server-confirmed public End (normal OBS Stop or grace expiry), update that original page to ended and show an acknowledgement dialog once for that terminal event. No reload is required to discover it. The confirmation acknowledges the completed public End; it does not issue Stop/seal, publish, delete or cancel anything. Do not wait for full VOD validation/publication before acknowledging public End. A never-public cancelled preparation is not a completed public live and must retain its distinct lifecycle.
4. Confirm reloads the direct Studio flow and prepares the next event using the same key and retained details. The previous URL remains the archived recording URL. The next idle URL must remain stable across reload and multiple Console tabs.
5. Do not automatically create new events while the dialog is still open. Closing/reopening or reconnecting the UI must not dispatch End again or multiply next events. Confirm/refresh failures remain recoverable with the original identity.
6. Old transport may still be supplementing/uploading after public End. Preserve original output/capture and bytes. If it still owns the single input slot, the next Console must honestly show preparation/waiting; it cannot claim ready reception, steal the slot or force a new capture. Existing atomic source/global admission remains authoritative.

## Layout and interaction acceptance

- Keep the bounded 16:9 monitoring viewport, key selector and header Share/account placement; no new direct Start/End control.
- Use one dark status label with a state dot. Grace must be visibly distinct from ended and from validation/preview loading. Do not reintroduce the pale nested status chip or manual load-preview control.
- The terminal acknowledgement has one primary confirmation action. Restore accessible focus, announce the state change and prevent duplicate submissions. API/network failure after confirmation offers retry without minting another event.
- Deduplicate by the original event identity and terminal transition, not each poll. A new browser tab opening an archived recording goes to recording management; it must not replay an obsolete control-room dialogue.
- Confirm re-enters the normal direct-event resolver; it must not clear all page state, rotate a key, clone by a second independent create path or alter retained title/description/cover.

## Backend/contract gates before implementation

- Determine durable fresh-media arrival evidence and timestamp; validation queue/waterline lag is not signal loss. Heartbeats alone do not prove actual media arrival.
- Set a fixed grace policy (60 seconds proposed, not enabled yet), server clock/deadline and atomic timeout-vs-reconnect arbitration. Account for 10.01 and legacy 30.03-second completed fragments without false endings.
- Freeze an exact common public endExclusive boundary on timeout even when OBS cannot ACK. Specify distinction from encoder-driven scheduled End, late fragments and original-capture补傳. Preserve full media validation and original idempotency/digest rules; any wire supplement must be explicit.
- Normal OBS Stop, timeout, source interruption and terminal failure must remain distinguishable. The historical 16-minute uninterrupted-public-event recovery test cannot silently remain the expected public lifecycle once timeout is enabled.
- A natural timeout End still completes the old recording and never emits a second VOD notification. A confirmed new live may notify once as a new event.

## Browser and Windows acceptance

Use the original Console and member page: signal loss -> grace UI -> recovery before deadline; then loss beyond deadline -> exact public End -> ended UI and single acknowledgement -> Confirm -> next prepared stable URL. Verify two Console tabs, repeated confirm/reload, API failure during confirmation, delayed old supplement/Stop/seal and input contention. No interruption of OBS global output/YouTube/local recording. Preserve diagnostic recordings and historical failure evidence. Real OBS timing requires Windows; local mock UI is not end-to-end evidence.
