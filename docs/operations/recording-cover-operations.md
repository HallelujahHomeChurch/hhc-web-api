# Recording covers

Deploy the Asset cover producer (including owner-bound retain/release tombstones)
and the audit producer catalog action `cms.recording.cover.update` before this
CMS consumer; Gateway/client releases follow CMS. Apply migration 066 through the
existing migration job, never an ad hoc runtime SQL command. No production gate
or live acceptance is implied by repository tests.

Uploads are bounded raw JPEG/PNG (5 MiB); Asset owns image validation and the
generated private JPEG. Covers do not grant playback, and CMS never returns a
signed URL, bearer token or storage key. No selected pointer is required for a
first-ready automatic fallback. Browser/member errors must not expose Asset IDs
as public access capabilities.

Selection persists actor type/ID, credential ID (service credentials only), request
ID, original key/payload/version and the unique reference intent. Audit uses the
existing actor identity shape, without adding credential metadata. Successful
receipts remain historical when title/publication/cover/expiry changes; retry
never restores prior state. Selection increments the existing version but does
not modify uploaded_at/expires_at or enqueue notifications.

The existing five-minute cleanup tick drains up to 100 cover references within
its 30-second context. Individual HTTP calls have a deadline. Failed references
remain retryable, with one-minute backoff; pending intents older than ten minutes
are abandoned. Current unexpired references are excluded. Selection holds the
operation/record locks during the bounded retain call so cleanup cannot fence an
in-flight selection. Release failure or response loss is safe to retry; the Asset
reference ID is unique, owner-bound and tombstoned on release, including release
before a delayed retain. Do not manually remove reference rows to resolve a retry.

Inspect `member_recording_cover_operation.state`, `created_at`, `checked_at` and
the selected reference pointer to diagnose stalled cleanup. Unbounded/repeated
`releasing` or old `pending` rows require Asset connectivity/owner-contract checks.
No repair should extend the recording deadline, replace a current reference, or
replay publication. CMS deletion remains immediate while byte cleanup retries.

Database operation metadata is retained for retry/receipt provenance. Its legal
retention policy is pending; this feature does not claim personal-data erasure or
invent a deletion interval. Selected and unselected bytes follow Asset recording
cleanup policies. See `docs/data-governance.yaml`.
