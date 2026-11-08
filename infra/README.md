# Production release

`hhc-web-api` is private to the Container Apps environment and is invoked through Dapr by `api-gateway`.

The internal ingress accepts private callers through Dapr caller identity and the app-channel token; workload authentication for sync windows is owned by Operations API.

Azure Container Apps injects the revision-scoped `APP_API_TOKEN` used to verify inbound Dapr requests. Do not persist it in Key Vault, declare it in Bicep, or reuse it as an outbound `DAPR_API_TOKEN`.

## Deployment configuration

Normal releases run only through the merged main-branch workflow. The protected
`production-actions` environment supplies `AZURE_CLIENT_ID`, `AZURE_TENANT_ID`,
`AZURE_SUBSCRIPTION_ID`, `RESOURCE_GROUP`, `ACR_NAME`, `ACR_LOGIN_SERVER`,
`IMAGE_REPOSITORY`, `EXTRACTOR_IMAGE_REPOSITORY`, `CONTAINER_APP_ENVIRONMENT_NAME`,
`RUNTIME_KEY_VAULT_NAME`, `MIGRATION_KEY_VAULT_NAME`, `LEGACY_KEY_VAULT_NAME`,
`ACCOUNT_KEY_VAULT_NAME`, `ENGAGEMENT_KEY_VAULT_NAME`, `QUEUE_STORAGE_ACCOUNT_NAME`
and `AZURE_OPENAI_ACCOUNT_NAME` as secrets. Optional provider endpoints, client
identities and existing credential-version pins also use secrets. Public feature
flags and the public renderer digest remain variables.

`scripts/deployment_config.py` validates targets and immutable images before
writes. Its `parameters TEMPLATE OUTPUT` command creates a mode0600 parameter
file outside the build context. Every template caller must pass that document;
there are no production target defaults. Do not upload it or print its contents.
The API docs publisher separately requires `STORAGE_ACCOUNT` and its existing
`API_DOCS_AZURE_CLIENT_ID` identity. No credential body belongs in source, workflow
inputs, logs or artifacts. Initial identity/vault/role provisioning is a separate
operator task requiring an exact reviewed preview; ordinary releases never grant
new permissions.

`hhc_web` keeps DML only. `hhc_web_migrate` owns the database, schema, tables, sequences, and migration history. The two credentials and managed identities use separate Key Vaults.

Keep the legacy secret and policy through the first rollback window. After cleanup, confirm the normal production `what-if` has no delete before starting a code release.

Every release uses one lean immutable API image for the migration job and runtime, and a separately verified watermark image from the same commit for the bulletin worker. The default Dockerfile target is `api`; build `--platform linux/amd64 --target watermark` for the worker. Pass `watermarkWorkerImage=<verified-worker-digest>` whenever `deployWatermarkJob=true` (or `deployWatermarkWorker=true` on the rollback path); the API image intentionally contains no Python, models or Poppler. The release rejects destructive resource/property changes and destructive migration SQL. A failed migration stops before the runtime revision changes. Runtime rollback never rolls back the forward-only schema. Before changing workloads, release captures the ready API revision and digest, the persistent watermark worker App revision/digest while that App still exists, and the watermark Event Job's previous digest once it exists. The watermark step deploys the Event Job with `watermarkJobMaxExecutions=0`, verifies paused admission, runs its `--preflight` execution, enables and verifies twenty executions, and only then deletes the App (see the README section "Bulletin watermark Event Job"). A confirmed failed deployment or failed watermark cutover runs `scripts/rollback-watermark-job.sh`: it restores the previous App (verify, exact revision copy, or recreate from this template with the previous digest), checks it becomes ready with that digest at scale-to-zero, then pauses the Job (`maxExecutions=0`). No queue message is injected for verification.

Console transport failures in release smoke and rollout checks emit an
`azure_console_failure` JSON record with the stage, original exit code,
WebSocket handshake status and numeric `retry_after_seconds` when available.
Raw CLI headers and response bodies are omitted. Missing metadata stays null;
an exit code alone does not establish throttling. These failures still stop the
release and trigger its existing rollback path. For a confirmed 429, wait the
reported Retry-After interval before considering an official workflow rerun;
the scripts do not retry failed probes or relax any readiness or route checks.

Release configuration is validated on `main` before any OIDC login. Source
verification, API image preparation, and extractor image preparation then run
independently. BuildKit pushes the extractor directly to private ACR, then its
vulnerability scan and four isolated load-test jobs pull the same immutable
digest in parallel. The load tests cover V1–V10 once across balanced groups.
Deployment waits for the scan and every other gate, and rejects image outputs
from another commit before its login. Publishing a candidate image does not
permit deployment until all its checks pass.
The production concurrency group still covers the entire workflow. Go source
verification runs contracts, core and storage/migration packages in isolated
PostgreSQL jobs, retaining race checks and uncached test execution for every
package.

Go build caches include the toolchain, module graph, job role, source and embedded
SQL/JSON inputs in their identity. A source change saves a fresh cache instead of
repeatedly restoring a stale module-only key; fallback stays within the same
platform, toolchain, modules and role. Only Go's build-cache directory is archived;
`-count=1` keeps every test execution fresh.
CI API and native image caches use separate per-PR/branch GitHub cache scopes.
Native image scopes include the UTC date to refresh apt layers daily; only one
renderer group exports each shared image cache. The API refreshes its apt layers
through the date build argument. GitHub's branch cache boundaries prevent PR
caches from being promoted into main releases. Additional source snapshots and intermediate image
layers consume cache storage and may evict older entries.

Image builds use separate private ACR `:buildcache` refs in their existing image
repositories. Only main release jobs write these caches; PR images are never
promoted or used as release caches. BuildKit reuses immutable layers, while the
UTC `APT_CACHE_DATE` argument refreshes distribution package layers each day.
Every final artifact is built from the merged commit and retains its vulnerability
scan and runtime checks. The cache adds intermediate-layer storage in ACR; the
first build of each day still refreshes package layers.

## Private CMS service transport

The CMS private HTTPS ingress accepts separately scoped Account, Operations,
Engagement and LINE credentials for existing routes. The Gateway has a
separate credential for the registered CMS admin/member/statement routes and
one opt-in credential for broadcast, capture, live playback and ingest.
The live transport retains the existing Presenter key and ingest session
validation. The public API, methods, routes and Gateway auth policies stay
the same. The published-locations credential remains separate.

The admin template `private-service-secret-access.bicep` stages six distinct
32-byte credentials encoded as lowercase hex, with each reader scoped to its
secret. Submit the secure parameters through standard input and pin the reviewed
versions using the corresponding `CMS_*_PRIVATE_SECRET_VERSION` release
secrets. The ordinary and locations-alias releases preserve existing
bindings. The Account, Engagement and Operations caller switches require their
exact producer secret versions. Set `CMS_ASSET_PRIVATE_HTTPS` after validating
the existing Asset workload identity and `CMS_AUDIT_PRIVATE_HTTPS` after the
Audit producer is ready.

The runtime starts without an injected app token only when `CMS_DAPR_ENABLED`
is explicitly disabled and all direct credentials are present. The ordinary release preserves the current sidecar setting. For retirement,
dispatch the existing owner configuration workflow (`locations-alias.yml`)
with transition `retire-dapr`, confirmation `retire-hhc-web-api-dapr`, the
accepted `source_release_run` and `expected_revision`, and `apply=true`.
It uses the same image without rebuilding, migrating or updating the
extractor or worker. Set the verified Gateway and Website revisions with
`CMS_DAPR_OFF_EXPECTED_GATEWAY_REVISION` and
`CMS_DAPR_OFF_EXPECTED_WEBSITE_REVISION` after authenticated live/ingest
acceptance. The release verifies the exact caller pins, stages the HTTPS-only
candidate with any deferred outbound pins after checking the serving producers,
and waits for the previous revision to drain. A confirmed unhealthy
candidate restores Dapr before copying the previous revision.

## Isolated bulletin extraction

The extractor has a separate immutable image and event-driven Container Apps Job,
`hhc-web-bulletin-extractor`. It reuses `hhc-web-api-identity`, the runtime DB
secret and optional Azure OpenAI configuration; it has no HTTP ingress or Dapr
sidecar. Work has a 120-second parent budget covering lazy PG connection
setup, claims and the batch. The batch defaults to one document (configured maximum
10); the existing 40–120 second per-document timeout is unchanged. Each document
uses the earliest parent, lease or document deadline before reserving 10 seconds
for layout/conversion persistence, or 30 seconds for extraction grant revocation
and persistence. An already-expired processing budget skips the processor and uses
the existing fenced failure path. Final SQL remains on the document context.

The existing 150-second replica timeout and 1 CPU / 2 GiB are unchanged. Grant
revocation intentionally retains its detached 20-second context, and the worker
still waits for actual work and deferred DB.Close to return. The work budget is
not a hard process/drain limit or proof that all pgx asynchronous cleanup and remote
backends are gone. Event admission is capped at one execution; database leases
fence stale document completion. The minute schedule is removed.

Before the API release, publish the reviewed shared renderer from
`frontend-platform` and configure `BULLETIN_RENDERER_IMAGE` with its exact GHCR
digest. Grant this repository's Actions token read access to that package.
The image build verifies the renderer artifact hash as well as the OCI pin.
Extraction and layout jobs load the V1/V2 manifests bundled with that same
image, never the independently deployed website's mutable manifest. The build
and isolated release gate run `bulletin-extractor -verify-assets`; incorrect
manifest pins fail before deployment. Native layout tests verify both versions
against the bundled font bytes, including Simplified Chinese conversion.
The legacy `BULLETIN_EXTRACTOR_ASSET_ORIGIN` environment value is ignored by
this runtime; infrastructure retains it for rollback to earlier images.
Configure `BULLETIN_EXTRACTOR_ASSET_AUDIENCE` for the existing Asset API workload
authentication contract. Verify the existing identity's Asset permissions, ACR
pull and Key Vault access before activation; regular release does not provision new roles.

Before the first Event release, preview `infra/bulletin-extraction-queue.bicep`:
exactly two new queues and two queue-scoped role assignments for the existing API
identity (main: Storage Queue Data Contributor; poison: Message Sender). After the
PR merges, an authorized operator deploys this reviewed module from the merged
commit, before the regular release's bootstrap guard. Never deploy a branch copy.
No account-wide storage role or new identity is required. Subsequent releases only
verify the queues and exact grants before writes. Both API and extractor receive
the queue URLs; no queue credential is exposed to the browser.

`BULLETIN_EXTRACTION_ENABLED` defaults to `false`. Disabled invocations
exit without claiming work; this flag does not suspend Event admission. Keep it false
until Azure capacity, identity/Asset access and human layout acceptance pass.
The local release load gate verifies real API readiness against PostgreSQL while
both sanitized PDFs and native HTML layout run in a nonroot, read-only,
network-isolated container limited to 1 CPU / 2 GiB without swap, and records its
cgroup peak memory. It is not a measurement of production peak usage; record
that separately before activation.

Release scans the extractor image, rejects unsafe what-if changes, deploys the
exact digest, reads back the Job configuration and repeats the API smoke check.
Before deployment, release captures the existing Job's Key Vault references,
configuration and template; inline secret values are rejected. Recovery omits
read-only `imageType` and empty provider-added `dapr`/`identitySettings` fields
that the update schema rejects; configured or unknown variants fail closed. On failure after
Job deployment starts, `rollback-bulletin-extractor.sh` restores this snapshot,
pins the previous immutable image, explicitly clears the opposite trigger, and
verifies provisioning, trigger configuration, image and activation. This restores
the old Schedule on the first cutover, or the prior Event configuration later.
The existing Job and its image are required rollback sources; preserve its ACR
digest. This cutover path does not bootstrap an absent extractor. Already-running
executions may finish within their bounded deadline; leases prevent stale completion.
Rollback never reverts database migrations. The release's full-template `--wake`
execution checks queue send access and adopts old queued work without a cron.

## B1 broadcast writer rollout

`broadcastWritersEnabled` defaults to false and maps to runtime
`BROADCAST_WRITERS_ENABLED`. The existing main-branch release workflow reads the
protected repository/environment variable `BROADCAST_WRITERS_ENABLED`, defaults
it to `false`, validates its boolean value, and includes the same value in the
reviewed what-if, migration/runtime and extractor deployment parameters. Enabling
requires the existing configured media host. Change the reviewed release variable
only after compatible Asset/Worker B1 range/projection, memberState and preview
purpose producers have released and integrated smoke passes; release through the
existing immutable workflow. Do not mutate Container Apps environment directly.

For rollback, set the release variable false and use the existing reviewed release
workflow. This blocks new create/bind writers while keeping B1 readers, controls,
authority reconciliation and migration 087. Retain compatible producer authority
and additive state while any B1 records or privacy intents exist.

## Shared Job login cap

Content-import, bulletin-extractor and watermark Event Jobs use runtime-vault
`jobs-database-url` for the separate `hhc_web_jobs` login. The watermark Job
needs the role's connection limit raised to 24 (20 watermark executions × 1 +
extractor 2 + content-import 2) before activating20 executions; that limit change is
made separately by the database operator. The HTTP API and the rollback-only
persistent watermark worker App keep the API login; migrations
keep the separate migration login (limit1). Provision/cap and verify Job-only
credentials with Job admission paused before releasing this source dependency.
No credential, Azure configuration or production data is changed here.
The historical48-connection operator preview in azure-infra PR144 predates the
watermark cutover. With the other current Job-role caps unchanged, this24-role
cap requires a total66-connection Job reserve; reconcile live role inventory
and full-server/rollout headroom before activation. This owner release does
not apply shared Terraform or alter database roles.

## API capacity profile

The API uses 0.25 vCPU / 0.5 GiB with a lazy PostgreSQL pool of 20 open / 10 idle
connections. API scaling keeps min1/max12 and uses CPU50% only, with no HTTP
rule. The CPU target is 0.125 core per replica; custom polling remains30 seconds
and configured cooldown300 seconds. Scale-down stabilization is managed by Azure;
cooldown does not reduce the min1 floor. The configured API connection ceiling
is240 per active revision; count old/new revision overlap separately. This is
the user-selected profile, not measured CPU-only scaling or peak-load acceptance.

Earlier isolated paired trials of CMS20/10 and Asset8/4 at0.5 CPU each, 120 viewer
requests/s per service plus producer/background/control work and a400ms CMS
dependency, completed with zero failed routes or driver drops. Earlier failed
trials remain part of the incident report; these trials do not prove managed
ACA timing or zero errors for arbitrary bursts. The model approximates the
native HTTP signal with application in-flight requests. No production load is
injected.

Release requires the full48-connection Job-role inventory and old-execution
drain gate, successful current-head CI, serial rollout and live revision/health
verification. Terraform source must reconcile the released settings. A config
rollback uses a reviewed source revert and the normal release workflow; an
image-only rollback does not prove resource settings were restored.

## Bulletin watermark worker identity (PLAN-v2 W3)

The watermark worker gets a dedicated `hhc-web-bulletin-worker-identity` so it can
call Asset API with an Entra workload token instead of Dapr (required for the
Container Apps Job, which has no Dapr). Release identities hold Contributor only,
so the identity and its role assignments are a privileged operator bootstrap,
run once from merged `main` after a reviewed what-if:

```sh
python3 scripts/deployment_config.py parameters infra/bulletin-worker-identity.bicep "$RUNNER_TEMP/worker-identity.json"
az deployment group what-if -g "$RESOURCE_GROUP" -f infra/bulletin-worker-identity.bicep --parameters "@$RUNNER_TEMP/worker-identity.json"
az deployment group create -g "$RESOURCE_GROUP" -f infra/bulletin-worker-identity.bicep --parameters "@$RUNNER_TEMP/worker-identity.json" --output none
```

It creates exactly one identity and four resource-scoped grants: Queue Data
Contributor on `bulletin-watermark`, Queue Data Message Sender on
`bulletin-watermark-poison`, Key Vault Secrets User on the single
`jobs-database-url` secret, and AcrPull on the configured registry. The Asset API `Asset.Invoke`
app role is assigned by the asset-api release (`approve_watermark_app_role`).

`main.bicep` only references the identity as `existing`. Repository variables
control the rollout:

| Variable | Values | Effect |
|---|---|---|
| `BULLETIN_WORKER_IDENTITY_ENABLED` | `false` (default) / `true` | Attach the identity to the rollback worker App |
| `BULLETIN_WORKER_ASSET_AUTH` | `dapr` (default) / `workload` | Rollback App Asset API authentication; `workload` requires the identity |

The Event Job (`deployWatermarkJob=true`, PLAN-v2 W5) does not read these
variables: it always uses this identity alone and workload Asset auth, and the
release passes `bulletinWorkerIdentityEnabled=true` for it. The variables only
shape the persistent App that `scripts/rollback-watermark-job.sh` recreates;
the default `dapr` keeps that rollback independent of the new identity.

### Private locations alias release

Normal production releases preserve the live alias mode and pinned reference.
The initial mode is disabled. After accepting the normal release, use the
`locations-alias.yml` workflow with `transition=enable`, the exact
`expected_revision`, the original successful `source_release_run`, and the
approved `CMS_LOCATIONS_SECRET_VERSION` environment secret pin already staged in the runtime Key Vault. Run the
default preview-only dispatch and review its paired Provider result. Dispatch
again with the same inputs and `apply` to recheck and apply. The release reads
version metadata. Only the API revision receives the reference.

This workflow reuses the authenticated immutable serving image and keeps its
RELEASE label. Build, migration, watermark, extractor, and content-import
releases run in the normal workflow. Changes to the shared Bicep require a
successful normal release before the configuration workflow. The original
release evidence must still be available. Verify private TLS and authenticated
alias smoke before any Gateway cutover.

For reverse, restore and verify Gateway Dapr first. Use `transition=disable`,
the exact alias-ready revision, and the original normal release run for its
image. The existing version pin is ignored for disable. Avoid concurrent Gateway configuration.
The alias workflow rejects active Gateway revisions with the direct binding.
It removes the alias env entry while retaining the pinned app reference.
A revision copy restores the prior revision configuration. An app-scoped
credential discrepancy requires owner review. Retain referenced versions
until every referencing revision is inactive.

The alias configuration workflow reuses the authenticated immutable serving
image and preserves its RELEASE label. Use its default preview-only mode,
review the paired Provider output, then set apply on a separate dispatch
with the same revision and inputs. Build, migrations, extractor, watermark
and content-import deployments belong to the normal release. A newer Bicep
revision needs a successful default-off normal release before this workflow
can use its image. The source release evidence must still be available.
