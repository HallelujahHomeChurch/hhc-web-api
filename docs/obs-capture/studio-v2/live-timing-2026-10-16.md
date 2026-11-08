# Public end and validation attempt timing

Scope: server-side diagnostic evidence only. K4 `k4-2026-10-15.rc1`, K3
`k3-2026-10-14.rc1`, B1 `b1-2026-10-10.rc1`, C1 `c1-2026-10-08.2`,
inventory schema 1, 30000/1001 fps, 900-frame/30.03-second segments,
digest, idempotency, authorization, full media validation and public
`[start,endExclusive)` boundaries remain unchanged. No Windows plugin or CLI change.

## Public end

CMS stores `EndedAt` beside `StartedAt` in the existing broadcast JSON record.
This is the first server observation that the exact current public end boundary
is verified, or a public stream's revocation/failure is confirmed by Asset.
It is not the End command time, OBS Stop, seal, media last-frame clock, ready,
or publication time. Existing admitted replay semantics remain unchanged.

The write occurs in the existing locked/fenced broadcast transaction, once only.
An unverified boundary does not set it. Never-public streams have no public end.
Completed historical records without evidence stay unknown; deployment does not
backfill them with the deployment time. There is no new public API field or UI.

Read-only CMS diagnostic query (substitute only the selected recording UUID):

```sql
SELECT recording_id, payload_json->>'StartedAt' AS public_started_at,
       payload_json->>'EndedAt' AS public_ended_at,
       payload_json->'View'->>'phase' AS phase,
       payload_json->'View'->'boundaries' AS public_boundaries
FROM hhc_web.member_recording_broadcast
WHERE recording_id = :recording_id;
```

## Validation attempts

Asset adds nullable `diagnostics` JSON to the existing package/source attempt
rows. Each claim has independent evidence; a stale worker cannot update it after
lease expiry/reclaim. Old rows remain NULL (unknown). Existing cleanup/retention
and parent deletion apply, with no new historical media retention.

A snapshot is saved with the existing progress heartbeat (30 seconds), phase
changes for source processing, and before terminal completion. A diagnostic
write has a two-second timeout and does not change validation success/failure.
The attempt row's state/finished_at remains the authority for committed completion.

- `observedAt`: snapshot observation, UTC.
- `elapsedMs`: worker wall time since claim processing began; excludes scheduling.
- `phaseElapsedMs`: wall time per existing processing phase.
- `stages.*.workerElapsedMs`, `calls`: accumulated work across workers.
  `inventory`, `playlists`, `inventory_write`; immutable object HEAD/copy;
  `object_download_hash`; `probe`; `decode`. `freeze_hash` is the containing
  freeze operation. Nested/concurrent totals must not be summed into wall time.
- Download, hash, and scratch writing share one streamed pass and are reported
  together; this instrumentation adds no second read or verification pass.
- `workFinishedAt`: validation/encoding returned, not proof of committed ready.
- `failureStage`, `failureReason`: bounded internal labels only, never raw errors,
  object paths, credentials or bearer URLs. Source processing outside package
  validation may report `unknown`; its phase still attributes elapsed time.

A killed worker may leave a partial checkpoint. In-flight stage work is counted
only on return, while phase wall time is checkpointed. Diagnostic-store failure
can leave older/missing evidence; it must not invalidate otherwise valid media.

Read-only Asset query:

```sql
SELECT a.claim_id, a.state, a.created_at, a.finished_at, a.diagnostics,
       p.completed_at AS upload_completed_at, p.ready_at
FROM recording_package_attempts a
JOIN recording_packages p ON p.id = a.package_id
WHERE p.recording_id = :recording_id
ORDER BY a.created_at;
```

Browser source attempts use `recording_source_attempts` joined to
`recording_sources` on `source_id`. Compare first claim creation to upload
completion for initial queue/dispatch wait; later attempt gaps include prior
work, lease expiry and retry delay, so they are not pure queue time.

## Targeted acceptance and Windows handoff

Platform local/CI coverage must include pending versus confirmed end, immutable
end during publication, never-public/historical handling, concurrent timing,
failed-stage recording, sanitized failure, stale-claim fencing and independent
attempt history, plus the existing real-media single-read/decode regressions.
Deployment and real OBS acceptance are separate evidence states.

Windows does not need to rebuild or repeat already-passed 16-minute recovery or
2.5-hour capture. Append the released platform versions to the current prompt.
At the next already-authorized short capture (after checking the meeting window),
retain capture/recording IDs and timezone-qualified End/Stop/seal/ready/publication
observations. Mac reads the new server timing evidence; Windows should not guess
these timestamps from UI polling. Preserve historical failures and diagnostic
recordings. Do not manually publish, replay Stop/seal, reset keys or delete data.

Production versions, CI and smoke evidence will be recorded in the release handoff.
