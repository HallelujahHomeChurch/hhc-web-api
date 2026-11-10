# Withdrawing and republishing completed broadcast VOD

Completed, retained broadcast VOD can be withdrawn without destroying its media
range authority. This is distinct from closing a running broadcast, deletion,
expiry, terminal media failure, or emergency revocation. Those protections remain
irreversible. The Asset range API, object-store authority and media Worker contract
are unchanged; they already support `blocked` → `vod` with increasing revisions
and reject `revoked=true` → `false`.

## Behavior and ownership

- CMS owns the publication decision. With `RECORDING_VOD_REPUBLISH_ENABLED=true`,
  ordinary unpublish of a retained completed broadcast writes draft status,
  `PublicationWithdrawn=true` and `MemberState=blocked` in the existing audited
  transaction. It advances the authority revision without changing capture,
  package, epoch, start/end boundaries, observed timestamps or retention.
- The flag defaults **false** and controls creation of reversible withdrawals
  only. Every version containing this fix always honors existing withdrawal
  markers, even with the flag off. Turning the flag off is not a data rollback.
- Background projection refresh cannot turn a withdrawn record into live/published
  content or rearm automatic publication. Settled withdrawn history is excluded
  from routine polling; pending authority and required cover work remain selected.
- Only an explicit successful publish transaction clears withdrawal. Current
  permissions, expected version, ready/retained media and synchronized authority
  are still required. Asset acknowledgement precedes new member VOD admission.
  Replaying an old publish receipt does not republish a later draft. Existing
  notification deduplication is unchanged.
- CMS refuses new member admission immediately on withdrawal. Existing media
  credentials are denied by the Worker when its authoritative range update
  reaches `blocked`; a provider outage can delay that propagation, as with the
  existing revocation path. The durable pending update is retried. No claim of
  synchronous cross-service revocation is made.
- Deletion explicitly bypasses the reversible branch, including deletion after
  withdrawal. Incomplete, revoked or terminal media never becomes recoverable
  merely because this flag is enabled.

## Release order and rollback gate

1. Keep the flag false while deploying this candidate with the platform batch.
   No Asset, frontend, SDK or schema release is required for this wire contract.
2. Verify **every CMS writer and background reconciler** uses a marker-aware
   artifact and that older revisions/processes have stopped. Traffic shifting
   alone does not stop old background workers. Old code would drop the unknown
   marker when saving JSON and could reopen a withdrawn archive as live.
3. Only after that evidence and separate enablement authorization, set the flag
   true through the owning reviewed runtime delivery workflow. This PR does not
   alter `infra/runtime-env.json` or enable production behavior.
4. In controlled acceptance, publish a completed VOD, withdraw it, wait for range
   convergence, verify new and existing member access is denied, refresh/restart,
   then explicitly republish. Verify original boundaries/expiry, fresh playback,
   no repeated notification, and deletion still produces permanent denial.
5. After any reversible withdrawal exists, the minimum rollback artifact must
   retain marker-aware readers/writers/reconciliation. Disable creation of new
   withdrawals if needed, but never roll back to code that discards the marker.
   Preserve the same gate when combining the other CMS platform PRs.

## Records already revoked by older versions

This fix does **not** un-revoke existing records or perform a data migration.
The old boolean does not identify whether the cause was ordinary unpublish,
emergency closure, deletion or terminal failure. Asset and R2 intentionally reject
unrevocation, and a new epoch does not change the immutable identity of the old
capture/object.

A separately reviewed recovery must first establish the original audited action,
absence of deletion/security/terminal fences, current retention deadline and
survival/integrity of the original validated package. Any reconstruction must be
owned by the producer, preserve retention and original media boundaries, acquire
fresh authority under a new valid identity, remain idempotent and lose safely to
concurrent deletion/revocation. No blanket JSON/SQL flag reset, authority object
replacement, retention extension or re-upload is part of this bugfix. If evidence
or retained bytes are missing, recovery is blocked rather than guessed.

## Verification boundary

Repository tests cover durable withdrawal and explicit republish, pending denial,
repository reload, old receipt replay, original retention, default-off rollout,
background auto-publication/projection refresh, history polling, terminal states,
and deletion before/after withdrawal. Existing Asset/Worker tests own byte-level
range enforcement. Local and CI results do not establish deployed authority
convergence, device playback, historical recovery or cost savings.
