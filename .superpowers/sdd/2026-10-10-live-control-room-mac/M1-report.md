# M1 CMS broadcast runtime report — 2026-10-10

Repository: `hhc-web-api`; worktree `live-control-room-plan-20261010`; branch `feat/broadcast-contract-m0-20261010`; M0 base `39dd5a7`. Initial scope was local implementation and verification. The later approved PR reconciliation below integrates upstream commits locally; this agent performed no push, PR merge, deployment, production mutation, or Windows acceptance.

## Implemented

- Typed broadcast aggregate and real PostgreSQL repository, migration 087, durable actor/request-bound idempotency, commands, receipts, full control intents and private audit. Existing legitimate recording mutations retain catalog-validated Central Audit events and existing publication/notification outbox semantics. No new bus or synthetic runtime.
- Recording → broadcast → capture locking; global create guard and B1 singleton active-capture index; binding and audit roll back together. Failed private create retries reuse the same durable intent/epoch; failed pre-public captures can rebind at the next epoch. Existing C1/CLI public schemas and ownership remain unchanged.
- Immutable bound policy, publication permission guards, cross-actor staff controls with uploader-only marker ACK, revision-independent ACK, frozen segment boundaries and Stop/End race handling. Normal seal obtains its end from complete verified rendition inventories; autoStart never guesses latest sequence.
- Asset typed private capture-create, range/projection and preview calls. B1 create uses atomic deny-all producer initialization. Emergency close preserves frozen boundaries and returns success only after the authoritative range and matching projection acknowledgement; unavailable producers return 503 while the intent persists.
- Independent member authority `blocked|live|vod`: pending End retains verified live prefix; fully verified End blocks member live access until publication. Live scope issuance checks locked broadcast state. Publication records VOD authority in the same transaction; revocation is durable and irreversible. Asset/Worker enforce old credential access independently of CMS UI.
- Authority application uses a durable prepare transaction, one exact committed policy snapshot applied outside the CMS transaction, and a fresh epoch/revision/boundary/state-fenced acknowledgement transaction. A failed projection read or CMS commit cannot strand retries behind a producer-only higher revision.
- Default-off `BROADCAST_WRITERS_ENABLED` with Bicep/runtime environment mapping and the existing reviewed release workflow variable, boolean/media validation, and all four what-if/deployment parameter paths; 30-second bounded rotating reconciliation, including deleted-record revocation. Disabling writers leaves existing controls and reconciliation available.
- Announced schedule expiry exemption, seven-day abandoned unannounced/unbound cleanup, thirty-day cancelled cover-reference grace. Explicit title/description and broadcast-owned uploads; inherited defaults preserve source ownership; durable cover references fence late retains, and auto cover waits for public projection readiness.
- Direct member watch resolution for legacy VOD/C1 and announced B1; upcoming list and member broadcast-cover route; existing membership/legal/DSR fences. Staff-preview grants have distinct purpose/path, bounded five-minute scopes, issuer/epoch validation and durable audit without credentials.
- Canonical OpenAPI updated with all runtime routes and additive optional nullable watch media fields. Admin filter `all` and `selectable` match the canonical enum; member upcoming rejects undeclared filters. Frozen rc1 candidate documents and manifest are unchanged. Its canonical equality test permits only the four explicit nullable resolver additions; the parent owns an independently frozen rc2 manifest.

## TDD and regression evidence

Required initial domain tests failed before the missing types/behavior were implemented, then passed. Actual PostgreSQL regressions additionally reproduced and fixed: transactional revocation, failed pre-public rebind, missing marker becoming sequence zero, emergency end shrinking/empty range, unpublished archive live-grant bypass, canonical `filter=all` rejection, explicit auto-cover source ownership, and producer-ahead-of-CMS recovery after an accepted policy followed by a failed projection read. Tests exercise actual transactions and typed HTTP producer calls; test producer fixtures are confined to tests.

Additional SQL checks cover failed producer response replay after a fresh service instance, audit failure rolling back binding, scheduled expiry, cancelled reference grace, staff/uploader separation, marker metadata-revision tolerance, exact public origin, cancellation of auto publication, fenced custom cover retention, pending-End live continuity, publication authority intent, schedule-presence idempotency, preview issuer audit and rejection of a member-purpose preview credential.

## Verification

Final stable-source verification completed on 2026-10-10. Mid-edit verification logs are retained for diagnosis and are not completion evidence.

- `go test -race ./... -count=1 -p=1`, with actual isolated PostgreSQL: PASS (root contract suite 148.400s, actual PostgreSQL suite 32.873s, recordings suite 16.139s); log `M1-verified-race-test.log`.
- `go vet ./...`: PASS; `M1-verified-vet.log`.
- Redocly CLI 2.47.0 canonical plus both unchanged rc1 snapshots: PASS, 21 warnings; `M1-final-openapi-lint.log`.
- CI concurrency, migration-policy tests/all migrations, what-if policy, release policy, extractor rollback policy: PASS; `M1-final-policy.log` (its bootstrap attempt is superseded by the successful isolated run below).
- Bootstrap migration/runtime role test: PASS on disposable localhost PostgreSQL. The existing test hardcodes port 5432; a temporary test-only adapter replaced it with 61531. Repository scripts were unchanged; `M1-final-bootstrap-test.log`.
- Offline watermark suite: PASS, 64 tests, 21 optional integration skips; `M1-final-watermark-test.log`. These skips are not claimed as image/PDF/model integration verification.
- Local `az bicep build` of `infra/main.bicep`: PASS (no deployment); `M1-final-bicep-build.log`. Writer rollout policy validation: PASS, including actual workflow configuration cases, recorded separately in `M1-final-writer-release-policy.log`.
- `git diff --check`: PASS. `docs/obs-capture/live-control-v1` has no diff.

Local database container: `hhc-broadcast-cms-m1-20261010`, PostgreSQL 17 on localhost 61531, database `hhc_web_test`. No shared/user database or process was repurposed. The container is retained for independent review.

## Release and acceptance gaps

1. New writers remain disabled. Enable only after compatible Asset/Worker atomic creation, range/projection, memberState and preview purpose producers have released and passed integrated media smoke. The private authority extension does not change Windows public B1 command/marker inputs.
2. Local tests do not establish remote CI, runtime image/vulnerability scanning, the unrelated hash-locked bulletin renderer image integration, public deployed routes, or physical Windows OBS/NVENC acceptance. No deployment or acceptance claim is made.
3. After a CMS publication transaction, media stays fail-closed until authority reconciliation succeeds (normal cadence 30 seconds); producer outages prolong this bounded retry state. Emergency failure is surfaced as 503 and remains retryable.
4. Controls/preview have immutable local B1 audit records. Shipping new Central Audit control action names would require the Account-owned catalog extension; no existing action was falsely reused for a control. Legitimate create/edit/publication events use the existing catalog.
5. Rollback disables writers first. Retain migration 087, compatible readers and authority reconciliation while B1 records exist; dropping additive state or rolling back the authority producer would lose privacy intents.

Windows acknowledgement is a final freeze/acceptance gate, not a Mac development gate. Parent coordination and independent backend review identified three P1 issues and one lag behavior; CMS fixes and matching Asset authority work are included. Final cross-repository rc2 contract freeze is owned by the parent.

## Local commits

- `e9968d4` — complete durable CMS runtime, original migration 086 (renumbered to 087 before release), canonical OpenAPI and regressions.
- `85af855` — reviewed release configuration for default-off writers, local Bicep and release-policy checks.

The report is committed separately. Worktree and disposable DB remain available for review; no push was performed. Parent-owned `docs/obs-capture/live-control-v1/rc2/` work was preserved and excluded from these commits.

## PR #188 reconciliation with published bulletin work

Merged `origin/main` at `4707444` into the existing feature branch without rebasing or force-pushing. Preserved bulletin upload identity, durable automatic conversion, fixed-template behavior, their config/startup/OpenAPI additions, and existing release flag defaults. Resolved the two workflow/policy conflicts by retaining automatic bulletin extraction validation and its deployed-revision check alongside the default-off broadcast writer validation and four parameter paths. Added a joint valid-release-input case for both features.

Renumbered only the unreleased broadcast SQL from `086_recording_broadcasts.sql` to `087_recording_broadcasts.sql` without changing its bytes. Published upstream `086_online_bulletin_upload_source_identity.sql` and its hash-locked policy remain unchanged. Current README/infra/report references use 087. Frozen rc1/rc2 snapshots, fixtures, handoffs and manifests remain byte-identical; the parent owns a subsequent handoff version. The new sequence guard covers migrations 086 onward, preserves historical published duplicate numbers, and failed on the reproduced 086 collision before passing after the rename.

Rebuilt only this agent's disposable `hhc_web_test` in container `hhc-broadcast-cms-m1-20261010` (localhost 61531), because its old filename-based ledger already contained unreleased broadcast 086. No shared database was touched.

Reconciliation verification:

- Broadcast, online-bulletin and OpenAPI contracts, including immutable rc2 equality/hash: PASS (2.907s).
- `go vet ./...`, local Bicep compile, canonical/rc1/rc2 Redocly CLI 2.47.0 lint: PASS; 29 lint warnings; `M1-reconcile-static.log`.
- CI concurrency, migration-policy regression/all migrations, what-if, release and extractor rollback policies: PASS; `M1-reconcile-policy.log`.
- Isolated bootstrap migration/runtime roles: PASS; unchanged repository test adapted only in a temporary file to port 61531; `M1-reconcile-bootstrap.log`.
- Deterministic converter: PASS, 5 tests. Offline watermark: PASS, 64 tests with 21 optional integration skips.
- Full `HHW_TEST_DATABASE_URL=... go test -race ./... -count=1 -p=1`: PASS on the stable reconciled source; root 106.538s, PostgreSQL 33.687s, recordings 15.836s; `M1-reconcile-race-test.log`. Database ledger confirms both `086_online_bulletin_upload_source_identity.sql` and `087_recording_broadcasts.sql` applied.
- `git diff --check`: PASS; no unresolved paths or frozen candidate changes.

New broadcast writers remain false in the reviewed release defaults. Remote PR CI, image/renderer integration, merge, release and deployed smoke remain parent-owned gates; this reconciliation performed none of those external actions. The temporary worktree and isolated database remain available.
