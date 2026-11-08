# Extractor verification selection

CI and release retain V1–V10. Shared runtime, parser, conversion, readiness,
assets, dependencies, control code, unknown paths, renames/deletions and symlinks
run the original full load acceptance. Version-specific template tests select
known downstream versions. Independent content/recording/legal/site-settings tests
still run in the complete Go lane, with native smoke for each extractor version.

Smoke composes a real bundled document and verifies its proof/content; full
retains both PDF fixtures, traditional/simplified cases, concurrent real database
readiness, 1 CPU / 2 GiB without swap and memory measurement. The shared 512 MiB
parser/asset preflight runs once, and both test binaries are built once per workflow.
Their exact hashes and image identity are bound into the selection plan.

Only fixed version/tier enums can become executable commands. All-smoke uses three balanced
runners; other plans retain at most four groups. The same Python runner executes
CI and release. Control changes also execute a synthetic release plan with no
publication credentials. Exact receipts reject missing/duplicate/stale/wrong-mode
or downgraded coverage, skipped/empty tests and image/binary mismatches.

`scripts/extractor-producer.json` pins the published producer commit/image. Both
old/new manifests must come from successful producer release runs and match the
published image. Read-only registry configuration (ENV, entrypoint, user and other
OCI config fields) is also compared by immutable digest; configuration-only
changes require full verification. The local extractor runtime fingerprint binds
its filesystem, image configuration and architecture. Missing/legacy/expired evidence requires full verification.
Current version input fingerprints conservatively cover the whole native runtime;
they exclude release-version-only metadata but include native code/dependencies.

Release uses the last successful deployed checkpoint and cumulative diff. Runtime
and per-version test-policy fingerprints must match original retained full evidence.
The checkpoint is finalized only after deployment/route smoke AND OpenAPI
publication. First release, failed publication, missing/expired origins, drift or
rollback selects full. The serialized workflow rejects stale releases and changed
artifact identities on rerun before deployment.

Set repository variable `EXTRACTOR_FORCE_FULL=true` to disable narrowing. This does
not bypass any gate. Existing runtime rollback remains separate. Plain CI success
is not proof of production, operator or document acceptance.

A failed unrelated job may be retried without rerunning successful native shards.
A prior-attempt receipt is reusable only within this exact workflow run/head/plan/
image/binary identity, after the Actions API confirms its named matrix job passed
at that attempt. Unverified, future, failed or different-plan receipts fail closed.
Each group replaces only its own same-run receipt after a successful rerun.

Vulnerability scans use the upstream GHCR database with the upstream ECR fallback.
The default Google pull-through mirror returned missing-layer HTTP 404 responses
in actual CI/release runs; Trivy does not treat that response as a transient
fallback condition. Scanner digest, VEX, severity and failure gates are unchanged.
See [upstream database configuration](https://github.com/aquasecurity/trivy/blob/main/docs/guide/configuration/db.md).

CI first routes changes before preparing native images. Regular Go additions and
modifications in the four independent main packages (`cmd/server`, `cmd/migrate`,
`cmd/content-import`, `cmd/home-v2-migrate`), with optional inert documentation,
keep Go, policy and image verification while skipping native preparation/shards.
The required aggregate accepts only an explicit `extractor_required=false` with
both native jobs skipped; missing routing output still requires successful jobs.
Unknown paths, deletions, mode changes, dependencies, shared API packages, non-manifest runtime configuration and extractor/renderer
changes retain the conservative gate. The manifest-only env path remains separate.

The dependency review used Go 1.26.10 `go list -deps -test` for `cmd/extractor` and
`internal/onlinebulletins`. The load binary imports `internal/httpapi`, which in
turn includes content, recordings, legal and site settings. Those trees therefore
remain inside native verification. Release selection uses the same independent entrypoint boundary, with smoke
only when retained full evidence matches the runtime and verification fingerprints.
Missing or mismatched evidence still requires full verification. Deployment and
evidence admission contracts are unchanged. Human-doc eligibility reuses the
shared Markdown classifier; non-document fixture/schema inputs retain full checks.
Actual API-only and extractor-change Actions timing remains to be measured after
authorized introduction; no runner-minute saving is asserted from local tests.

Checkpoint sealing depends on successful public acceptance and extractor coverage,
independently of OpenAPI publication. Publication failure still fails the release.
A failed CMS owner release is a reusable extractor baseline only when the exact
attempt completed acceptance and sealing, every other job succeeded or was
legitimately skipped, and OpenAPI publication is the sole failure. Source, artifact
digest, receipt and ancestry checks remain required; renderer producer failures
and PR artifacts never receive this exception.
