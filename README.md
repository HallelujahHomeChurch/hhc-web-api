# HHC Web API

Website backend and CMS core for `www.alive.org.tw`.

The service owns CMS source records, revisions/publication workflows, protected weekly bulletin metadata, public website projections, and website product orchestration. Public website routes read published projections only; weekly bulletins are never public. Protected routes require the Azure Container Apps Dapr token, the `api-gateway` Dapr caller identity, sanitized `X-HHC-*` identity headers, and domain authorization.

There is no separate v1 `cms-api` or `bulletin-api`.
Church units, resources, meetings, occurrences, and their audit history are
owned by `operations-api`; this service retains only its entitlement client.

Migration101 is a guarded, irreversible contraction after exact legacy DSR cleanup
and acceptance of reader-independent releases. It removes only the empty watermark
restriction-withdrawal table, its two origin triggers/function and the obsolete
subject origin flag. Restriction rows, unknown dependencies or stored readers abort
the transaction. The mixed watermark action table retains historical erase receipts,
request/withdrawal metadata and indexes; erased subject fences, ordinary account
cleanup receipts and existing 365-day retention remain. Migration history is
immutable; the policy allows only the exact reviewed migration path and SHA256.
Recovery requires reviewed backups and compatible code, not a down migration.

## Asset dependency trace privacy

The Asset client's existing `otelhttp` transport uses a private tracer decorator.
It records a fixed `asset.http` span name, bounded HTTP method/status/protocol
attributes and a generic error type. URL/destination attributes, free-form error
status descriptions and sensitive event/link metadata are omitted. This also
covers errors while reading the response body. Trace IDs, parentage, span kind,
timestamps, status codes and the configured sampler remain intact.

Only telemetry is filtered: the real URL (including signed query and userinfo),
headers, body, redirect policy, cancellation and returned errors are untouched.
Synthetic local tests cover a signed upload through a 307 redirect, transport and
body failures, cancellation and trace propagation. No production credentials or
trace payloads were inspected. This addresses the reproducible code-path risk in
[issue299](https://github.com/HallelujahHomeChurch/hhc-web-api-archive-20261109/issues/299), not a
claim that historical exposure has been measured or existing stored traces erased.

The tradeoff is reduced dependency detail: these spans no longer expose request
paths, target hosts or raw failure descriptions. Other clients, application logs,
span resources and HTTP metrics are outside this filter's scope. In particular,
returned errors keep their existing semantics and must not be assumed redacted
when a caller logs them. No exporter, resource, retention or sampling change is
introduced by this transport-only tracing policy.
Member recording list responses classify a cancelled incoming request as HTTP 499
(`request_canceled`) only when the returned error also matches context cancellation.
Independent dependency cancellation, deadlines, and genuine failures retain their
existing mappings. A cancellation remains an unsuccessful operation for reliability
accounting; 499 alone does not establish why the caller disconnected.

Lifecycle reconciliation compares incoming projections with current primary rows before requesting recording write admission. Unchanged snapshots use ordinary bounded reads; changes retain the same finite admission, atomic update and retention/deletion predicates. This avoids retryable capacity failures for redundant member catalogue refreshes without caching authority. Changed snapshots pay an extra read before their existing transaction.

## Staff translation previews

Authenticated CMS translation previews have no application cooldown or per-minute/day
quota. Staff can regenerate the same saved source and language immediately, including
after a provider failure. Responses return `retryAfterSeconds: 0`; existing cooldown
rows are ignored for previews. Source/version/permission checks, bounded provider
requests and content-free audit remain required. This does not save or publish
content. The separate news-summary generator retains its existing quota policy.

## Local Development

Personalized PDF downloads use the dedicated watermark worker's trusted-derived
Asset completion contract. The original clean immutable PDF and renderer source
hash must match; output hash/size/MIME, private grants and receipts remain required.
Generated outputs do not enter a second antivirus queue. An absent/invalid producer
contract fails closed; deploy the additive Asset contract before this consumer.
Ordinary uploads still require scanning. Each execution processes one download; the Event Job admits up to eight
executions. Account-bound ready artifacts retain their seven-day reuse window.

```sh
cp .env.example .env
set -a && source .env && set +a
go run ./cmd/server
```

## Public read deadlines and connection budget

The published JSON routes (`/api/news`, history, videos, locations, pages, home,
active statements, site layout and common legal content) have a 25-second request
context deadline. It includes pool checkout and SQL execution, preserves an earlier
caller deadline/cancellation, and ends before the server's 30-second write timeout.
Socket timeouts alone do not cancel database work. Authenticated routes, downloads,
translation and background jobs retain their own budgets; this is not a global
handler timeout. PostgreSQL tests cover blocked pool checkout and a table-locked
projection query, cancellation and successful reuse after the blocker is released.

Connection budgets are per process, not per Dapr request. The release template sets
the API pool to 10 open / 5 idle connections (also the standalone default),
with 1–12 replicas: up to 120 API connections per active revision. The API rejects
an open limit above this proposed envelope; worker limits are separate. The
watermark worker Event Job runs up to 20 executions with a 1 / 1 pool each on the
`hhc_web_jobs` login (the rollback-only persistent App keeps 2 / 1). Each extractor execution
caps its pool at 2 / 1; each migration/import CLI caps it at 1 / 1. Count simultaneous
executions and old/new revision overlap separately. Job `parallelism: 1` limits
one execution's replicas, not the number of overlapping executions. Other services
and operator/migration headroom also share the database server's capacity.

Dapr application concurrency controls requests entering through each sidecar; it
does not cap background SQL, direct HTTP calls, or the combined replica pools.
Do not size it by equating it to this pool, or treat the maximum connection count
as a safe number of concurrently executing queries. Pool/replica changes require
measured checkout waits, query/lock latency, workload overlap and recovery evidence.

## Bulletin watermark Event Job (PLAN-v2 W5)

The watermark worker runs as the Container Apps Event Job
`hhc-web-bulletin-watermark`. KEDA polls `bulletin-watermark` every 10 seconds
(`queueLengthStrategy: visibleonly`, one execution per visible message, at most
`watermarkJobMaxExecutions`=20) and starts `/hhc-web-bulletin-worker --once`:
one dequeue attempt, then exit. An empty queue, a finished job, a terminal
failure and a released retryable failure all exit 0; only process-level errors
exit 1. Queue visibility (300s) equals `replicaTimeout`, and the database lease
(270s) is shorter, so a killed execution's message reappears after its lease has
lapsed. The Job has no Dapr or ingress and uses only
`hhc-web-bulletin-worker-identity`: ACR pull, the queue scaler and client, the
Key Vault reference to `jobs-database-url`. Asset API calls use the exact
`X-HHC-Caller-App-Id` assertion over private HTTPS, without acquiring an Entra
token. CMS/extractor use `hhc-web-api`; watermark uses `hhc-web-bulletin-worker`
while retaining CMS asset ownership. The existing `workload` mode remains an
alias for `internal`; deployed audience/client metadata is retained for the
existing rollback checks but no longer used for Asset authentication.
`config.LoadWorker` does not require the Dapr `APP_API_TOKEN`.

Each execution starts a fresh process, so investigations reload the watermark
model per execution (the resident direct decoder is disabled in `--once` mode);
investigation volume is low and this is accepted.

**Connection-limit dependency:** every execution holds one `hhc_web_jobs`
connection. The role's connection limit must cover 20 watermark executions + 2
extractor + 2 content-import = 24 before this release ships; it is raised
separately (database operator change, not part of this repository).

**Preflight.** `/hhc-web-bulletin-worker --preflight` consumes no work. It
checks configuration, PostgreSQL connectivity, the exact table privileges the
worker repository methods use (`has_table_privilege` in a read-only
transaction; receipt `UPDATE` is required for `FOR SHARE`), a peek of the main
queue, and `GET /priv/assets/{random 32 hex}` which must return 404 (token
accepted and caller allowed; 401/403 fail). It logs exactly one line,
`bulletin watermark preflight`, with `ok` and one fixed class per check, and no
identifiers. The release runs it as a manual execution of the deployed Job with
only the container args overridden (see `scripts/cutover-watermark-job.sh`):

```sh
az containerapp job show -g "$RESOURCE_GROUP" -n hhc-web-bulletin-watermark -o json \
  | jq '{containers:[.properties.template.containers[0] | {name,image,command,args:["--preflight"],env,resources:{cpu:.resources.cpu,memory:.resources.memory}}]}' > preflight.yaml
az containerapp job start -g "$RESOURCE_GROUP" -n hhc-web-bulletin-watermark --yaml preflight.yaml --query name -o tsv
az containerapp job execution show -g "$RESOURCE_GROUP" -n hhc-web-bulletin-watermark --job-execution-name <execution> --query properties.status -o tsv
```

The full container template is passed because `az containerapp job start
--args` alone sends a container with only the overridden fields.

**Cutover and rollback.** The release deploys the Job
with admission paused (`deployWatermarkWorker=false deployWatermarkJob=true
watermarkJobMaxExecutions=0`), verifies it against the
`valid_watermark_job` what-if policy plus this release's image, `RELEASE`,
`--once` and `maxExecutions=0`, and runs the preflight. It then enables twenty
executions and verifies the readback before retiring the App. A failed verification or
preflight pauses Job admission (`maxExecutions=0`) and leaves the persistent App
untouched. Observation or activation errors also pause admission and request worker
recovery independently of API health. Only after activation is verified is the App
`hhc-web-bulletin-worker` deleted; its last image digest is logged first (keep
it in ACR while it can still be a rollback source). After a confirmed failed
deployment `scripts/rollback-watermark-job.sh` restores the previous worker App
(unchanged App: verified only; changed: exact revision copy; deleted: recreated
from `infra/main.bicep` with `deployWatermarkWorker=true deployWatermarkJob=false`
and the previous worker or Job image), waits until it is ready with that digest,
and then pauses the Job. A healthy `ScaledToZero` App is valid; an unhealthy or
inactive App cannot authorize pausing the Job. Failure after App retirement begins
requests this worker recovery even when the API remains healthy.
`scripts/test-rollback-watermark-job.py` rehearses it
in CI against a stubbed `az`. The App definition and the Dapr Asset-auth branch
are removed after the Job has been stable for 2 weeks.

**Manual end-to-end check.** The member download path cannot be exercised in CI
(account login is Cloudflare Turnstile protected). After each release that
changes the worker, sign in on www.alive.org.tw, download one protected
bulletin, and confirm one `bulletin watermark job phases` line with
`outcome=ready` in `ContainerAppConsoleLogs_CL` for the Job, plus a matching
ready database job and a successful execution.

## Bulletin extraction Event Job (PLAN-v2 P4)

PDF upload/clean-scan completion, manual extraction retry, layout validation and
Simplified-Chinese conversion atomically enqueue a content-free outbox wake.
The existing outbox dispatcher sends `{"version":1}` to `bulletin-extraction`.
Transport/configuration failures retain this wake beyond the ordinary outbox
attempt cap, with the existing backoff capped at 160 seconds; there is no cron
fallback. Processing failures still follow the extraction/manual retry policy.
The extractor Job polls every 10 seconds, admits at most one execution and runs
one database batch. Extraction failures stay failed until staff retries; layout
and conversion retain their existing retry rules. Nothing is published by a wake.

The database owns work state. The message stays invisible for 180 seconds while
an execution runs (120-second work budget, 150-second replica timeout). Remaining
work, delayed retries or live leases defer the same wake; no remaining work
acknowledges it. A killed process leaves the message for redelivery after its
lease expires. Malformed wakes send only a fixed marker to the poison queue.
The release sends one `--wake` execution to adopt work from the retired schedule.
See [bootstrap and rollback](infra/README.md#isolated-bulletin-extraction).

## Manual PostgreSQL jobs

`hhc-web-migrate`, `hhc-web-content-import` and `hhc-web-home-v2-migrate` each cap their own pool at one open and one idle connection. Migration cleanup runs before the CLI reports an error and exits. A detached ten-second cleanup context bounds advisory unlock; failed acquisition/unlock discards the physical session. This is a per-process cap, not a bound on concurrent executions or total bootstrap/retry/drain time.

## Runtime images

The default Dockerfile target builds a lean API/migration/import image. Only
`--platform linux/amd64 --target watermark` includes Python, Poppler and watermark
models. Both targets reuse the same static Go build; CI tests/scans each verified
image and release passes separate immutable digests to the owning workloads.
See [release and rollback contracts](infra/README.md).

## CI cost controls

PR CI skips application Go/native/image jobs only for proven human documentation
diffs; dependency/security, OpenAPI, history and policy checks still run. Unknown
events, missing history, mixed changes, symlinks, executable documents and contract
fixtures (including `docs/obs-capture/`) always take the full verification path.
The `verify` check rejects unexpected skipped or failed jobs.

Go modules and source-aware build artifacts have separate caches, so the build
cache is restored once per job. The combined race job retains the former core
cache namespace to avoid discarding compatible compiled artifacts. Go verification uses one runner with two bounded
`-race -count=1 -p=1` lanes: migrations/PostgreSQL use a separate disposable
database from core/contracts. `scripts/test-go-lanes.py` requires distinct loopback
test DSNs (`HHW_TEST_DATABASE_URL` for core/contracts and
`HHW_TEST_STORAGE_DATABASE_URL` for storage, retaining the guarded `hhc_web_test`
database name), discovers every package, rejects incomplete results, and stops both
process groups on failure or cancellation. Its regression tests run in CI and
release. Bootstrap, converter tests and vet still run once.

Extractor consumers execute authenticated prebuilt test binaries with `go tool
test2json`; they retain the pinned Go toolchain but no unused module/build caches.
Producer compilation caches and immutable image/binary verification remain.
Docker excludes Go test files without excluding
the Python/JavaScript tests executed inside image builds. Test-only Go and README
pushes do not deploy a new runtime; manual production releases remain available.

PR CI builds the pinned renderer and extractor image once, then sends that exact
image to four independently isolated PostgreSQL/native-verification shards. The
one-day CI artifact is selected by its producer's immutable artifact ID and checked
against the checkout/run, archive SHA256 and loaded image ID. Failed-consumer reruns
reuse the original producer output; after artifact expiry, rerun all jobs to rebuild.
These artifacts are never promoted to production.

When CI also rehearses release selection, each shard executes the union of both
plans in one process. Only identical `(test tier, renderer version)` cases are
deduplicated; full and smoke tests both run when requested. Head, image, runtime,
verification and binary hashes must match, and neither receipt is written unless
the complete union passes. Both existing exact-coverage aggregates remain. Actual
release runs still execute their own selected tests; no PR result is promoted.

CI targets ten minutes from run creation to final check, with fifteen minutes as
the review threshold. Application/build jobs have a fifteen-minute execution
timeout (extractor execution step ten minutes); policy and aggregate jobs have
ten/five-minute limits. These are cancellation bounds, not a workflow-wide timer:
serial dependencies and queue time can still exceed the review threshold. Compare
official job timestamps, queue, test-log timestamps and summed runner time separately;
reporting delays and parallel job sums are not measured billed savings.

Closed PR merge-ref caches are removed by a separate trusted-base workflow with
only Actions write and pull-request read permissions. It never checks out code,
validates every returned cache ref and ID before deletion, and rechecks that the
PR is still closed before each delete. Main and other PR refs are excluded.
Cleanup runs only on future PR-close events; it does not sweep historical caches.
Caches saved after cleanup by an in-flight CI run remain subject to normal GitHub
retention. A concurrent reopen between the state check and delete can still lose
a cache entry; reopening remains safe because caches are rebuildable.

## Initial Routes

- `GET /health`
- `GET /ready`
- `GET /api/member/bulletins`
- `GET /api/member/bulletins/latest`
- `GET /api/member/bulletins/online` (authorized PDF/Online union, offset/limit)
- `POST /api/member/bulletins/{issueId}/versions/{locale}/online/access`
- `GET /api/member/bulletins/{issueId}/versions/{locale}`
- `GET /api/member/bulletins/{issueId}/versions/{locale}/download`
- `GET /api/admin/bulletins`
- `GET /api/admin/bulletins/template-settings` (historical read-only values; new jobs use backend conversion rules)
- `PUT /api/admin/bulletins/template-settings` is retired (410); remove the Admin editor before releasing this change. Queued jobs, historical rows and document snapshots remain unchanged.
- `GET /api/admin/bulletins/template-settings/revisions`
- `GET /api/admin/bulletins/{issueId}`
- `POST /api/admin/bulletins`
- `POST /api/admin/bulletins/{issueId}/upload-sessions`
- `POST /api/admin/bulletins/{issueId}/assets/{assetId}/complete`
- `POST /api/admin/bulletins/{issueId}/publish`
- `POST /api/admin/bulletins/{issueId}/unpublish`
- `GET /api/news`, `/api/history`, `/api/videos`, `/api/home`
- `GET/POST /api/admin/content/{news|history|videos}`
- `GET/PUT /api/admin/content/{module}/{contentId}`
- `POST /api/admin/content/{module}/{contentId}/{publish|unpublish}`
- `GET /api/admin/content/{module}/{contentId}/revisions`
- `POST /api/admin/content/{module}/{contentId}/revisions/{revision}/restore`
- `/api/admin/campaigns*` and `/api/admin/campaign-schedules*` proxy authorized CMS operators to the private engagement service.

Admin writes require `If-Match` after creation. Publish is asynchronous and returns `202`; protected bulletin visibility changes only after the private asset is ready. News edits keep the previous public projection live until the replacement asset grant is ready.

Private bulletin template settings contain three Traditional Chinese church values,
not their fixed labels. Human bulletin read/write scopes apply. CAS saves create
immutable history and an atomic site-settings audit; they never publish or change
an existing issue. New extraction jobs freeze the saved version; identical replay
and retries (including a parser upgrade) retain the original nullable snapshot.
V9 renders the frozen configurable values; nullable legacy jobs retain their
original renderer. Consumer and immutable renderer release gates still apply.

Structured online reading starts at issue 1739. Earlier online projections are
retired from member discovery/access and cannot be extracted, converted or
published again. PDF discovery/downloads and private immutable revisions remain
unchanged and available for recovery. No archive table, deletion or rewrite of
existing seven-day offline receipts is performed.

Weekly bulletin uploads are orchestrated here, while `asset-api` owns the upload target, private object, ClamAV status, and owner-authorized byte stream. The browser never chooses an asset namespace or owner. PDF uploads are limited to 20 MiB.

News, history, and video content share lifecycle, locale, revision, and public-projection behavior while retaining typed module tables and validation. Public routes never read drafts.

News image uploads are coordinated through content-owned upload routes. Detail and home images are optional; when attached, each image must be owned by that item, pass ClamAV, finish responsive derivative processing, and receive its own public read grant before publication. The public home image falls back to the detail image. Unpublish removes the projection immediately and revokes every public grant asynchronously while retaining private assets for revisions and republishing.

Content and bulletin publication may submit an immutable notification command to `engagement-api`. HHC records only command acceptance in its outbox: notification delivery, status, and deletion are owned by Engagement and never change the source publication lifecycle.

## Recording public IDs

New upload, broadcast and missing-ID allocations use 11 characters from the
64-character URL-safe alphabet. The generator rejects substrings from the fixed
version 1 list in `internal/postgres/recording_public_id.go`, ignoring case and
`_`/`-`, and also checking `0→o`, `1→i`, `3→e`, `4→a`, `5→s`, `7→t` substitutions.
This covers a limited set of common English strings and Taiwanese Latin
abbreviations, not every language, spelling or phonetic variant. Rejection sampling
reduces the original 66-bit candidate space; it does not change the ID format.
Generation stops after 32 rejected candidates or an entropy read error. Database
collision retries remain separately bounded at eight. Existing mappings, lookups
and validation remain unchanged, including IDs that now match the list. No
existing ID is reassigned, and deploying this change does not run a backfill.

## Production release

Service-principal identity is limited to recording list/read/create/title,
exposure, covers, publish and unpublish operations. It carries no human user, role or
session identity; durable audit records retain `actorType=service` and the
principal UUID. Missing audit persistence fails closed. Other CMS and member
routes remain human-only. Legacy single-file recording routes are retired.

Recording management uses draft/published states only. Unpublish returns to
draft without deleting media or extending its 30-day deadline. Pinning/hiding
are retired (the exposure endpoint returns 410); hidden publications migrate to
draft without notifications. Member selection defaults to the newest upload.
Human-only DELETE requires `cms:recordings:write`, If-Match, and additionally
`cms:recordings:publish` for published recordings. CMS deletion, audit and its
retryable cleanup command commit together. Deleted records disappear immediately
and cannot obtain fresh playback grants; Asset API fences their jobs and late
uploads, then existing cleanup removes source Blob, HLS and previews. Existing
grants remain bounded by their expiry. Retained CMS metadata/audit are not a
personal-data erasure operation. Failed cleanup rotates through the durable queue.

This change pins the service-actor catalog proposed in audit-log PR #14;
release that producer before enabling a consumer. CLI service credentials and
HLS upload/playback require the independently gated Asset producer; these identity
checks alone do not enable service-token issuance or production HLS.

Recording publication by a service principal requires one bounded
`Idempotency-Key` and the original `If-Match`. Keyed publication returns an
immutable receipt plus the current recording and `published`/`state_changed`
outcome. Receipt, publication, audit and the deduplicated first-publication
notification commit together. Replaying a completed intent never republishes
after manual unpublish or expiry. A key is bound to actor type/ID, recording and
expected version; changed payload conflicts. Human Admin calls without a key
retain their existing response and optimistic-concurrency semantics.

Browser source uploads use human-only `/api/admin/recordings/{id}/source-uploads`
create/status/sign/complete/retry-processing routes. CMS binds a single immutable
source ID to the recording; direct CLI packages and browser sources cannot replace
one another. Asset owns the 50GB source limit, 16MiB Blob blocks,
short-lived write capabilities, source processing and cleanup. CMS never proxies
the video bytes or sends these sources into the ordinary malware-scan workflow.
Complete returns `202/finalizing`, not ready. Background reconciliation uses a
CMS-owner-bound ready-source lookup followed by independent ready-package
validation, so closing the uploader tab does not prevent later publication.
Source completion never publishes or sends notifications by itself; the existing
publication transaction remains the notification boundary. Source producer gates,
Gateway allowlists, frontend integration and live provider acceptance must all be
completed before feature activation.

Admin progressive capture create/status/declare/sign/confirm/stop/seal/abort and
cancel-auto-publication/close-live routes now persist their actor-bound intents,
receipts and retryable Asset commands. Stop fences new server-registered scopes
without freezing tail uploads. Seal requires stop plus normal-end evidence and
Asset confirmation; queued objects do not imply verified media. The 24-hour
capture deadline never extends on retry. Recording unpublish/delete close live
and cancel pending automatic publication transactionally. See
[`recording-capture-foundation.md`](docs/operations/recording-capture-foundation.md).
Completed captures with pending automatic publication use an independent
30-second reconciliation loop, including a startup pass. Durable per-intent retry
backoff still applies; the five-minute maintenance batch does not delay eligible
publication. Every attempt independently validates ready media and
rechecks the original authorizer through Account's exact recording-publication
probe. Publication shares the recording lock with cancellation and manual edits;
its terminal intent, internal service audit and first notification commit together.
Account and audit producer releases remain activation gates. Member live
list/playback routes now reuse current membership/legal checks, the subject
fence, private media origin and watermark policy. Live grant issuance holds the
recording/capture lock through the bounded Asset call and registry/trace commit.
B1 End preserves already registered account-bound scopes through archive processing and publication, without admitting new viewers to the unpublished archive. Stop blocks new viewer scopes; existing account-bound scopes can resume after
grant expiry until the fixed replay/capture/retention deadline. Completed live
expiry preserves normal VOD and pending normal publication. Publish-capable
human staff can close live or cancel automatic publication while OBS is offline.
Admin GET alone adds an optional four-field capture summary; upload inventory
remains uploader-only. Asset/Worker/Gateway release and real-device gates remain.

Recording covers are optional and independent of playback grants. Admin cover
list/upload/select/content routes support scoped service identities; member
`GET /api/member/recordings/{id}/cover` requires current entitlement and legal
acceptance, publication visibility and unexpired media, and returns private,
no-store JPEG bytes. Existing recording projections only add optional
`selectedCoverId`; recording lists do not fan out to Asset. Without an explicit
selection, the first ready automatic preview remains the default.

Cover selection requires If-Match, write scope, and publish scope when already
published. It never publishes, notifies, or extends recording expiry. Service
selection requires an Idempotency-Key; its immutable receipt is returned with
current state and `selected`/`state_changed`. CMS commits a uniquely referenced
intent before retaining Asset bytes. Selection, version, receipt and audit commit
together; the five-minute reconciliation loop releases failed/abandoned intents,
replaced references, and expired/deleted selections with retry. Abandoned intents
are fenced after ten minutes. Asset release tombstones prevent delayed retains
from resurrecting a released reference. See
[`recording-cover-operations.md`](docs/operations/recording-cover-operations.md).

GitHub Actions builds one immutable image for the migration job and runtime. The release stops if the manual Container Apps migration job fails. See [`infra/README.md`](infra/README.md).

Admin bulletin, website-content, and site-settings reads and mutations durably enqueue their catalog-validated Central Audit events in `hhc_web.outbox_event`; mutation events share the business transaction. Dispatch defaults to disabled. Enabling it requires `AUDIT_DISPATCH_ENABLED=true`, exact `AUDIT_APP_ID`, `AUDIT_TOKEN`, and `DAPR_HTTP_PORT`.

## Protected bulletin access

When `BULLETIN_AUTO_EXTRACTION_ENABLED=true`, completing a general Traditional Chinese PDF upload from issue 1739 atomically
records an online extraction request in the existing outbox. The worker waits
for a clean scan and verifies owner, PDF MIME and SHA-256 before queuing parsing;
closing the browser does not cancel it. Both first and replacement uploads use
this flow. Superseded uploads are skipped, and separate upload objects preserve
their own immutable evidence even for identical bytes. No PDF publication is
blocked, no online content is automatically published, and authored Local stays
unchanged while a replacement produces Incoming for comparison. Earlier issues,
children's PDFs and other PDF locales are unchanged.
The compatibility release defaults this switch to false. Release and verify the
consumer first, then activate production through a subsequent CI/CD release;
see [rollout and recovery](docs/operations/bulletin-automatic-upload.md).

The event-driven extractor uses the immutable V10 artifact after both frontend
consumers have released V10 support. Each new job's immutable cover-setting
snapshot supplies the three labelled vision values; later shared-setting changes
cannot alter that job, its retry or its saved document. Nullable legacy jobs
retain V8. The `weekly-v1.19` cache identity creates a new Incoming candidate for
an existing source; saved V1–V9 drafts keep their frozen renderers. Native historical labels require complete text and geometry;
the reviewed gospel-wording variant has its own `historicalGospelGoals` identity.
Outlined Letter headers require the reviewed image pair and native glyph anchors,
distinguish current three-row from historical four-row wording, and remain
explicit, located review proposals. Unknown/moved/duplicate evidence
fails visibly. This stage neither adopts Incoming nor changes authored drafts,
canonical metadata, review confirmation, publication or reader activation.
Current V8 candidates from issue 1739 omit the first article's production
metadata while retaining its source blank band and divider. Body source-page
membership and vertical origin stay unchanged; legacy candidates keep their
existing presentation until explicitly re-extracted.
The source-verified Cambria face maps to body text; other Cambria variants and
unidentified fonts still require review. Existing candidates are not rewritten.

V10 recognizes canonical title/subtitle artwork only inside the supported first
article's fixed header geometry. Up to twelve overlapping WordArt fragments must
form connected title/subtitle groups with the full-sized anchors; an isolated
image or missing anchor remains reviewable. Other images remain reviewable. Dense covers
use measured complete text with a 9-point floor, never clipping source content.

Traditional online publication now authorizes conversion and automatic Simplified
publication from that exact immutable source. Layout/review completion no longer
queues a draft. Native layout, mapping and audit gates remain enforced; failures
retain the previous publication and expose recovery. Unpublished source edits
never enter this flow. See [rollout and recovery](docs/operations/bulletin-simplified-publication.md).

The back-page service-table anchor uses a paper-relative lower bound for both
A4 and Letter. Only verified excluded-band horizontal overflow is discarded;
cross-boundary content still blocks review. Scaled back frames and complete
numbered-item sequences may propose outlined section labels; absent sidebars
also require an empty native-text region and geometrically contained slots.
These proposals require located human review; conflicting native wording stays
blocked. Long outlined titles retain readable canonical placement proposals
instead of losing both title and subtitle. None of these paths approves source
wording or makes an otherwise unsupported source eligible for publication.

When a comparison fully adopts Incoming content and source geometry, known
visual findings refresh from that extraction instead of retaining stale warnings.
Mixed authored content and unknown findings remain reviewable; adopting a draft
does not accept findings or publish it.
Repeated extraction retains an existing known section kind only when the matched
section's semantic content is unchanged. Classification-only model variation
therefore does not create new comparison work; changed or unknown sections still
retain their incoming result, and authored Local kinds remain three-way edits.
The updated cache identity lets the normal extraction action apply this rule to
previously cached candidates without rewriting saved drafts or old evidence.

Extraction and draft saves retain published sentence anchors across CJK layout
whitespace changes and unchanged ordered duplicate lyrics. A draft save repairs
unoccupied anchors against immutable published content, retaining authored text,
styles and new geometry and requiring fresh layout measurement. Exact surrounding
anchors bound independent typo corrections; real splits, merges and ambiguous
replacements still require publication mapping. Partial highlights and note ranges
on the same sentence survive whitespace-only changes by code-point alignment,
not fuzzy matching; changed wording retains the private quote for reanchoring.

V8 retains V7's source policy and pinned fonts. It fits canonical titles at
normal tracking, keeps boxed headings with their own speaker row, and places
the cover issue after its measured date. Unfit headings fail rather than lose
text, cross source pages, or shrink the base text below the reading floor.
The extractor-owned diagnostic adapter leaves immutable renderers unchanged:
editable layout failures carry bounded input-bound page/slot identities into
the existing non-waivable review list. Saved-draft failures are lease-fenced job
diagnostics, not content revisions; successful validation removes those findings.
Simplified conversion failures retain the same source locations, so repairs stay
in the matching Traditional draft rather than introducing manual Hans editing.
Both images and CI use Go 1.26.9 to include the October standard-library security fixes.

V7 omits the production credits band and closes its space on the same source
page. Only frame-proven, single-separator speaker suffixes resolve automatically;
ambiguous evidence stays reviewable. Variable source tracking is normalized before
native layout validation, never merely hidden from the issue list. Traditional
body emphasis uses the pinned serif bold face, including Simplified fallback for
rare glyphs retained by OpenCC. The original PDF remains unchanged.

Every historical and future electronic bulletin is member-only. The request maps
an exact `series + locale` pair to an Operations entitlement, checks it before any
bulletin or Asset lookup, and streams bytes through Asset API's private
owner-authorized endpoint. Unknown pairs and denied entitlements return the same
non-disclosing `404`; authorization dependencies fail closed with `503`. Every
response is `private, no-store`. Staff `cms:bulletins:*` permissions never grant
member access, and there is no public-open switch or public bulletin grant.

Online access uses that same exact edition entitlement. Its POST atomically
commits a distinct reader receipt before returning source-free structured
content, immutable publication metadata and a seven-day validation window.
Clients verify `X-HHC-Content-SHA256` against the exact response bytes before
decoding. A repeated account-bound `clientRequestId` preserves the original
window; a fresh request can revalidate a same-account/document/revision receipt.
Only an owner `not_found` with `X-HHC-Bulletin-Access: unavailable` is terminal
for offline cleanup. Dependency failures and unrelated 404s must only lock data.
Reader receipts share the existing privileged trace lookup, fixed 365-day
retention, subject fence and ordinary account cleanup, but never enter
PDF fingerprint evidence or screenshot decoding. No full-content GET exists.

Discovery accepts either one `locale` or up to three distinct comma-separated
`locales`. All requested editions must pass the existing checker before any
lookup. Pagination and total count distinct issues, with all selected language
editions returned for each page; independent language publication cannot shift
editions onto different pages. A healthy result requires no PDF-list join in
the browser. Single-locale callers keep the same contract.

Private reader state and mutation routes reuse the same entitlement before any
lookup and the existing subject write fence. State migrates transactionally
through published checkpoints; missing history fails closed without losing
private quotes. Batches allow 100 operations / 1 MiB, notes 10000 Unicode code
points and 500 anchors. Terminal mutation results are retained for 91 days;
unseen operations older than 90 days require recovery. `revision_changed`
does not consume the mutation ID. Ordinary account cleanup removes private reader state, mutation receipts and account-attributed access receipts under the shared subject fence. Existing restriction fences remain effective. Legacy DSR export/action/withdrawal endpoints are retired.

## Application logging

`LOG_LEVEL=debug|info|warn|error` defaults to `info`; invalid values stop
startup. This controls application slog and default standard-log output.
Persisted audit records remain independent of this setting.

`ACCESS_LOG_MODE=all|errors` defaults to `all` when unset, independently of `LOG_LEVEL`.
The API runtime image sets `errors`; explicit `all` restores fast success logs.
Application/provider logs and durable audit are independent. Successful per-hop
troubleshooting is reduced; sampled traces are not complete access evidence.
Healthy fast probes are omitted; errors and requests taking at least one second
remain visible. Paths use route templates (unmatched requests use `/unmatched`);
query strings, request bodies and path identifiers are omitted.

### Legal evidence (staged)

Legal drafts use the fixed `common` and `verified-member` scopes and the five
website locales. Common publication must exactly match both existing CMS public
projections in every locale. Member publication references an existing current
common version pair, stores private snapshots, and never writes public projections.
A version cannot be reused for different document bytes. Published snapshots,
documents, and trusted actor/request mutation provenance are immutable.

`GET /api/legal/common` returns only common content. Current member content uses
`GET /api/member/legal/current` after server-side membership qualification; failure
returns an error without any private fallback. Member and admin responses are
private/no-store. Only Account may resolve historical snapshot IDs; Account and
Operations may resolve a current manifest over direct private service calls.
Admin draft and publication require existing CMS page permissions and If-Match.

Migration 059 creates empty legal tables. It does not publish content, change
Account versions, enforce member acceptance, or enable analytics. Retention and
staff provenance retention still require the governance decision recorded in
`docs/data-governance.yaml` before activation.

### Preserved cleanup and restriction evidence

Ordinary account cleanup retains its existing idempotency receipts, subject fences and asset-delete outbox behavior. Legacy restriction/action/withdrawal data and historical migrations are preserved for the separate reviewed exact data cleanup. Existing restrictions remain enforced; retiring the API does not restore processing or recreate records, jobs or assets. The existing action-receipt retention worker is unchanged.

Recording metadata supports an optional plain-text `description` of at most 5000
Unicode code points. PATCH still requires the title and exact version; an omitted
description preserves it and an empty string clears it. Published edits retain
the publish-permission check. Description edits use the existing recording
metadata transaction/audit and advance human-edit generation, preventing a stale
capture intent from auto-publishing. Consumers render it as text, never HTML.

Member recording discovery keeps the existing array response when no query is
supplied. `?limit=12&cursor=...` enables uploaded-time/ID keyset batches with
`meta.nextCursor`; a null cursor marks exhaustion. Empty batches can carry a
cursor after unavailable media is skipped. Every batch retains current member
visibility and owner-ready-media checks; no cover bytes or playback grants are
returned by the list. Cursor values are opaque, bounded, and must be returned
unchanged with a valid limit.

## Church statement dismissal

`GET/PUT /api/me/statements/{statementId}/dismissal` is a trusted Gateway human
route with no CMS scope requirement. Both operations require the immutable
published version; a changed publication returns 409. Preferences are permanent
for `(account_id, statement_id, published_version)` and do not depend on a date.
Migration 082 adds the owner table. Ordinary account cleanup deletes these rows under the shared subject lock; existing fences prevent recreation.

## Live thumbnails

Live thumbnail defaults and per-capture selections are separate, revision-fenced CMS
settings. The canonical `openapi.yaml` defines the Admin JSON/image routes and
member-only image route. Writes require a human with both
`cms:recordings:write` and `cms:recordings:publish`; reads require the existing
recordings read capability. The member endpoint retains membership/legal checks.
Images are bounded private JPEG responses (`private, no-store`), never public URLs.

Migration 085 installs automatic-frame defaults for new capture intents. The first
durable intent snapshots the default revision and retains any selected fixed
image; retries reuse that snapshot. Changing defaults affects future intents only.
The initial automatic job uses the first verified, published 480p fragment.
While live, automatic selections refresh every five minutes from a verified public
480p fragment through Asset's private, idempotent refresh endpoint. The retained
current image and its revision remain readable until a replacement is ready; failed
refreshes preserve that image. Custom selections are never refreshed. Accepted End
freezes the selected image for VOD promotion and discards late automatic results.
Superseded image references retire through the existing bounded cleanup. Failed
initial extraction keeps media validation/playback intact and allows a fixed upload.

The 30-second derivative reconciler preserves live image references, promotes the
selected normalized bytes into VOD, then permits automatic publication. A newer
manual VOD selection wins. Failed automatic extraction falls back to the existing
three VOD samples. Abandoned upload/selection references are retired by bounded
cleanup; selected defaults and active capture references remain protected.

Release order: Asset migration/worker, CMS migration/API, Gateway routes, shared
Website client, Admin and Website consumers. Producer and consumer PRs are separate;
consumer lockfiles must pin an actually published shared client before release.
This feature does not change C1, inventory schema/digest or 30000/1001 fps validation.
Windows OBS requires no plugin changes; production OBS acceptance follows deployment.

## Broadcast control (B1)

CMS owns scheduled/announced broadcasts, immutable binding policy, actor-bound
commands and marker receipts. Migration `087_recording_broadcasts.sql` adds B1
state and an active-capture uniqueness guard without backfilling legacy C1.
Recording, broadcast, capture, control receipt and audit intents commit under the
same recording → broadcast → capture lock order. Marker boundaries refer to
complete segments and remain immutable after acknowledgement. Stop seals raw
capture; verified public range, media readiness and CMS publication remain
separate states.
Member recording lists and watch details report the verified public-range duration
for B1 archives; they never substitute the private raw-capture duration. Admin's
capture summary reports an enabled B1 publication intent as pending while archive
readiness defers C1 activation. Explicit cancellation and terminal execution states
remain authoritative; this read projection does not activate publication.

`BROADCAST_WRITERS_ENABLED` defaults to false. Enable new create/bind writers only
after the compatible Asset/Worker producer has released atomic B1 deny-all capture
creation, private range/projection, `memberState` authority and staff-preview
purpose support. Existing broadcasts retain reads, controls and reconciliation
when writers are disabled. The 30-second reconciliation loop publishes durable
range/access intents and retries failed producers; producer failures do not return
successful emergency-close confirmation. Terminal captures persist revoked/blocked
authority before any producer call, including retries of older pending ranges;
conflicts remain visible and cannot roll back that authority. Public live access
requires `live` authority, ready unpublished archives use `blocked`, and the existing publication
transaction records `vod` authority. Publication can briefly remain fail-closed
until producer acknowledgement. Existing audit actions cover legitimate recording
mutations; control and preview issuance have dedicated durable B1 audit records.

Announced schedules have no upload-draft expiry. Unannounced, unbound drafts are
cleaned after seven days; cancelled broadcast cover references have a thirty-day
grace period. Direct watch resolution preserves legacy VOD/C1 and announced B1
URLs, while member cover bytes require current membership and legal acceptance.

Rollback starts by disabling new writers. Keep migration 087 and compatible
readers/reconciliation while any B1 records exist; removing the additive tables or
rolling back Asset/Worker authority would lose durable privacy intents. Windows
OBS/NVENC marker, network-interruption and physical playback acceptance remains a
separate release gate. This code and local tests do not establish deployment or
physical-device acceptance.

### Event stream keys (K1)

Human-only broadcast `stream-key` GET/PUT and `stream-key-bindings` POST add an
optional per-event credential to the existing OAuth/B1 binding. Admin generates
32 random bytes; CMS persists only SHA-256 and a masked suffix in private broadcast
JSON. Keys can be configured/reset only before the first capture intent. Keyed
events reject unkeyed binding; retries retain the original capture intent and the
same global single-input limit. B1/C1 media and public boundary contracts stay
unchanged. Canonical K1 schemas are in `openapi.yaml`; the independently frozen
handoff is in [`stream-key-v1/contract.md`](docs/obs-capture/stream-key-v1/contract.md).

Release K1-aware CMS before enabling consumer UI. Pre-K1 writers can discard new
private JSON fields; once keys exist, use a K1-aware rollback/forward fix. Disabling
new writers alone does not make old reconciliation safe. Source tests do not prove
production release or Windows OBS acceptance.

### Reusable OBS source (K2)

K2 adds a singleton reusable credential and explicit Console target preparation.
See [the frozen contract](docs/obs-capture/stream-source-v1/contract.md) for
resolve/bind revision fences, durable replay and errors. Migration 091 stores
source ownership separately and derives live-input occupancy from accepted B1
seal evidence; active capture verification and per-recording uniqueness remain.
An accepted sealed capture can verify while the next event starts. Asset's
existing two-capture processing capacity is unchanged. No existing event key is
automatically migrated. After source adoption, use only a K2-aware rollback or
forward fix; old binaries cannot enforce source ownership.

## Key-only OBS ingest

The Console prepares events and delegates upload ownership. The additive K3 `/api/ingest/` surface accepts a reusable source key without Account login and issues a short-lived, event-bound transport session. Only capture transfer, status, control polling and marker ACK are allowed; Admin and member APIs retain their existing authorization. See [fixed K3 contract](docs/obs-capture/key-only-ingest-v1/contract.md). No CLI or media-validation change is required.

### Recoverable Studio stream keys

Production Studio requires `STREAM_KEY_ENCRYPTION_ID` (active version, initially
`v1`) and `STREAM_KEY_ENCRYPTION_KEYS` (JSON object of version to 32-byte hex key).
The release uses the CMS runtime vault secret `studio-stream-key-encryption-keys`;
never reuse another service's encryption key or put values in logs, Git, or tickets.
AES-256-GCM envelopes bind to the source key hash. Keep old encryption versions
until their envelopes have been migrated; removing a version makes those keys
unavailable for copying, but does not change ingest authentication.

The protected POST `/api/admin/broadcast-sources/{sourceId}/stream-key/reveal`
requires `cms:recordings:write`, records a successful audit before returning a
non-cacheable response, and never includes a secret in source list/detail data.
Legacy hash-only keys return `409 stream_key_not_retained`. Their next authenticated
K4 output open retains an envelope without rotating the key or changing revisions.
Do not reset an existing OBS key just to migrate storage.

### Deployment readiness

`/health` and `/health/live` report process liveness. `/ready` and
`/health/ready` check local initialization only, without network or database calls.
An unavailable DB or Dapr sidecar does not evict an otherwise initialized API.
Protected requests still fail closed when their authorization dependencies fail.

A separate `dependency_health` monitor checks DB acquisition/ping and, when
`DAPR_HTTP_PORT` is set, local Dapr outbound health. Checks run immediately and
every 30 seconds, sequentially with separate two-second deadlines. It uses only
`DAPR_API_TOKEN` for the loopback request, rejects redirects, closes response
bodies, and cancels/joins before database shutdown. It logs a bounded outcome,
phase, HTTP status and duration, never raw errors, URLs, credentials or bodies.
Checks emit initial state, transitions, and a heartbeat every five minutes
(healthy INFO, failed WARN); sampling remains every 30 seconds. Observe at INFO level;
heartbeats missing beyond six minutes for an expected active replica mean unknown,
not healthy. This monitor
is not a request-path gate, alert deployment, or proof of downstream availability.

Release verification runs immediately after API readiness, before background
worker deployment. `scripts/verify-rollout.sh` validates the captured candidate
image and every app/sidecar container, rejects forward candidates with restarts,
and requires 30 healthy rounds of read-only public probes at two-second intervals.
Private console invocation is omitted; the retained public probes cover the existing Gateway routes.
Sanitized request IDs, status, latency and candidate snapshots are uploaded on
success or failure; a later successful response cannot erase a failed sample.
Rollback retains the failed release result and verifies the restored image/path.

This is a post-cutover gate, not proof of uninterrupted deployment. The separate
PG maintenance, replica increase, full-transition observation and the historical
Dapr port/probe failures remain separate acceptance work.

## Private reader bookmarks

Reader state now always includes `bookmarks`, including an empty array. The
existing protected state/mutations endpoints support create, rename, delete and
explicit re-anchoring. Writes reuse account/document serialization, version CAS,
mutation fingerprints and the subject erasure fence. Names are trimmed and capped
at 80 Unicode scalars; each account/document allows 100 active bookmarks. Targets
must match a canonical source fragment's page and scalar interval. A published
revision change retains the target and marks it for explicit re-anchoring.
Migration104 is additive; bookmark rows share the reader parent's account cleanup
cascade and private-data counts. Release this producer before a published SDK
and its online-only bookmark consumer. No new public route or offline queue is
introduced.

### Live playback capacity protection

Live grant issuance retains its transaction and authorization fences while calling
Asset. To prevent that class of slow calls from consuming the entire database
pool, each runtime's shared RecordingRepository admits at most
`DB_MAX_OPEN_CONNS - 2` concurrent live grants. The normal pool size of 10 admits
8; a pool of 2 admits 1, and a pool of 1 cannot admit live grants. Runtime config
requires a positive pool limit; unlimited `sql.DB` instances used by tests do not
have a finite budget and bypass this guard.

Excess grants fail before acquiring a database connection with the existing
`503 capture_unavailable` response and `Retry-After: 5`; diagnostics identify
`live_capacity`. There is no additional queue. This preserves two pool slots
for other work at normal pool sizes, but does not reserve them against other
query classes or across separate repository instances. Legitimate rejected
playback attempts remain reliability failures. This safeguard does not replace
autoscaling or establish the cause of any historical outage. VOD package grants
already issue outside the database transaction and are not subject to this limit.

Broadcast reconciliation reads the Asset projection before acquiring recording,
broadcast and capture row locks. It rechecks capture identity, epoch, boundaries,
stop/revocation state and projection revision under the existing transaction before
applying the result; stale results wait for the next reconciliation pass. Slow
projection reads therefore do not hold the row locks needed by viewer heartbeat
and metrics requests. The mutating idle-signal reconciliation still runs inside
its existing transaction: moving it requires a separate producer-side fence and
is not covered by this change. This is a reproduced contention fix, not proof of
the historical incident's cause or of peak-load capacity.

## Recording transaction admission

Explicit `RecordingRepository` transactions share one admission budget before
`BeginTx`, including grants, source operations, captures, interactions and metrics.
A finite pool of10 admits8 such transactions, reserving2 connections for other
work. A pool of2 admits1; a pool of1 admits none. An unlimited pool retains no
finite reservation guarantee. Main constructs one repository instance for the
shared DB; creating additional instances around the same DB would create separate
budgets and is not a supported way to share this protection.

Grant and mutation admission is nonblocking. Excess work receives a local capacity error, mapped to
retryable HTTP503 with `Retry-After: 5`; legitimate user failures still count
against the SLO. This is overload containment, not a replacement for scaling or
removing slow dependencies. Transaction cleanup rolls back unfinished work before
releasing its permit, including error/cancellation/panic paths. SQL statements,
authorization/DSR fences and lock ordering are unchanged.

The local PostgreSQL reproduction holds an Asset preview call while source-lock
waiters, viewer heartbeats or their mixture arrive. Previously all10 connections
could wait, causing the real readiness DB Ping to return503 after2s. With shared
admission, the same scenarios retain readiness200 and ordinary reads complete.
Independent pools and a separate OS process against the same DB are covered;
those tests model replicas without creating cloud resources. They demonstrate an
amplification mechanism, not the initial blocking operation in the live incident.

The budget covers explicit recording `BeginTx` entry points. Direct SQL queries,
including CTE updates such as reconciliation selectors, and other repositories
remain outside it and can still consume or exhaust the shared pool. There is no
claim of universal DB availability. Stateful signal/preview network calls still
hold existing authority locks; narrowing global locks and moving network calls
require separate consistency designs. No pool size, scaler, min/max replica or
infrastructure setting changes are included.

Recording watch and member cover reads share the DSR lifecycle advisory fence,
so a slow protected read does not serialize that member's heartbeat and comment
operations. Interaction transactions retain their original user-wide mutual
exclusion through a separate advisory key, acquired after the shared lifecycle
fence and before publication rows. This preserves heartbeat credit and comment
idempotency across recordings. Restriction/erasure and other domains retain the
exclusive lifecycle key; mixed old/new replicas remain conservative. This does
not remove publication locks, external-call waits, or database pool limits.
### Bounded contention diagnostics

`CMS_DIAGNOSTICS_ENABLED` defaults to `true`; `false` disables the new recorder,
summary worker and stage spans. It does not disable existing access logs or
OpenTelemetry. Diagnostics never query PostgreSQL for statistics: `DBStats`
reads the process-local pool. No new exporter, collector or dependency is added.

Each API process emits `cms_db_pool` at most once per 15 seconds and `cms_stages`
at most once per minute through the existing console logger. There are no
per-request diagnostic examples. `LOG_LEVEL=warn` or `error` suppresses these
info summaries. Console ingestion can still fail; missing summaries mean missing
evidence, not zero traffic or zero errors. Replica/revision attribution uses the
platform's existing log envelope; `release` accepts only a 40-character hex SHA.

Pool fields are `max`, `open`, `in_use`, `idle`, and interval deltas `waits` and
`wait_ms`. `initial` identifies the first sample after recorder startup; `reset`
identifies decreasing cumulative counters and uses the new totals as the delta.
These waits are **connection-pool waits**, not PostgreSQL row-lock wait time.

The stage summary's `data` is a JSON object with six static keys. Each value is
`[count,sum_ms,max_ms,errors,cancelled,stale,le10ms,le100ms,le1000ms,gt1000ms]`.
Histogram buckets are exclusive; `cancelled` is a subset of `errors`. Counters
reset per emitted window; uint32 overflow saturates and sets `saturated=true`
rather than wrapping. `stale` counts discarded projection results separately
from their read outcome and can fall into the next window after the read.

| Stage | What the elapsed time includes |
| --- | --- |
| `projection_poll` | Asset projection GET, including downstream processing |
| `signal_reconcile` | Mutating Asset signal POST, still under existing SQL locks |
| `broadcast_source_tx` | BeginTx/pool acquisition, source advisory lock, source cleanup SQL |
| `broadcast_lock` | Recording/broadcast/capture row queries, including SQL and lock waits |
| `broadcast_callback` | Existing UpdateBroadcast callback, including any nested dependency calls |
| `broadcast_commit` | Commit; no assumption that this is only network time |

Stage times overlap and must not be added as transaction duration. Persistence
between callback and commit is not separately measured in this scope. No stage
claims exact PostgreSQL lock-only time. A background reconciliation span connects
its stages; HTTP stages retain the request trace, and Asset calls are children of
the corresponding stage span. Existing head sampling applies: not every error
will have a trace. All completed calls still update the in-memory summaries.
New summaries and stage attributes contain no user/capture/request IDs, SQL,
URLs, error messages, tokens or payloads. This is not a claim of whole-trace
redaction: the existing Asset `otelhttp` dependency spans still include URL
attributes, including path IDs and potentially signed query parameters. The new
reconciliation root changes their parentage and sampling grouping, not their
attribute filtering. [Issue299](https://github.com/HallelujahHomeChurch/hhc-web-api-archive-20261109/issues/299)
tracks that existing privacy risk; it requires a separate fix before
claiming end-to-end trace privacy. Existing diagnostics outside this recorder
retain their previous behavior and are not included in its byte budget.

Worst-case text-handler fixtures measured 322 bytes per pool record and 966 bytes
per stage record, below enforced 512/1024-byte budgets. Budgeting at those upper
bounds gives 4,423,680 raw bytes/day/replica (35,389,440 at eight replicas), excluding
platform envelopes, existing logs and sampled traces. This is not a bill estimate
or a change to retention/daily caps. One worker coalesces ticker delays instead of
catching up with bursts. Request accounting never writes to the log sink; shutdown
cancels the worker without a final flush and waits at most one second. A blocked
console write can retain that one worker until the sink returns, without blocking
request accounting or extending shutdown beyond that bound.

Sampled-span volume is separate from the console bound. Estimate it from completed
stage counts plus reconciliation invocation counts, then apply the effective
parent/head sampling decisions; do not assume independent 10% sampling for children
of the same reconciliation. Multiply measured exported bytes per representative
span by retained span count, including the existing dependency spans, and reconcile
with actual AppInsights/Log Analytics billed sizes and freshness before rollout.
The fixture proves span parentage and field privacy only for the new stages; no
production ingest-cost or cap-headroom acceptance has been performed.
### Opt-in D2 recovery control

New OBS output intents may send `recoveryVersion: common-watermark-v1` as a
capability preference. CMS persists the requested value for output-session
replay identity separately from the effective Asset capability. Only a new
direct, unscheduled AutoStart/AutoEnd capture activates it; manual/scheduled
targets still open successfully with legacy authority. Missing fields in older
capture JSON remain legacy and cannot be upgraded by replay or a control GET.

`controlVersion=d2-2026-10-23.rc1` adds recovery and public-end metadata while D1
and unversioned response schemas remain unchanged. Asset owns the persisted
deadline/common watermark; CMS does not create a recovery clock. The existing
30-second background sampling can delay the visible interruption hint to roughly
30–60 seconds. Healthy control polls do not add Asset calls; interrupted health
refresh keeps the existing two-second bound and authority transaction. This
still holds the existing SQL fence across signal I/O and is not a lock-removal
fix. A failed refresh re-reads binding authority rather than taking control down.

Public-end provenance and recovery snapshots use additive existing JSON payload
fields. Old binaries may discard unknown fields on write: after rollback,
capability absence is reported as unsupported and old end provenance as unknown,
never inferred as five-minute support. Deploy Asset and CMS producers before the
new plugin. An old CMS can reject the new open field with generic400; clients
must not silently retry a different intent. Protected network authorization and
local capture continuation remain separate; public end grants no upload rights.
### Staff preview scope renewal

`POST /admin/broadcasts/{recordingId}/preview-access` accepts optional
`playbackScopeId`. Omitting it preserves new-scope behavior. Supplying a scope
renews only an unexpired staff-preview grant for the same actor, capture and
epoch under the current authority lock. Unknown, expired, stopped, revoked or
mismatched authority returns 404 without creating a replacement scope. Consumers
must still exchange the returned credential to refresh the cookie; the media URL
and cookie path remain unchanged. Each expiry is bounded by five minutes and the
capture expiry. Changed audit metadata appends a receipt; an identical audit
snapshot does not create another receipt. The existing Asset call remains inside
the authority transaction; this change does not remove that contention risk.


## Subject-scoped member reads

Watch and member broadcast-cover lookups use an explicit `SubjectReader` on the
connection already held by the DSR fence. Ordinary broadcast watch reads no
longer borrow a second pool connection while retaining the first. The same SQL
and scans run at the existing READ COMMITTED isolation; this is not a promise of
one immutable snapshot across all statements. Member cover uses its persisted
selected-cover scope, which already replaced the preceding studio lookup.
Admin cover ownership lookup and all publication/expiry/revocation checks remain.

Only entry to this scoped subject-read transaction may wait for a permit: at most
64 queued callers per repository, for at most one second or the caller's earlier
cancellation/deadline. No SQL connection is acquired while queued. These reads
are limited to six concurrent read transactions within the existing eight-of-ten
budget, leaving two transaction permits for mutations. Small pools retain at
least one permitted read when their total budget is positive, so two mutation
permits are not guaranteed there. Grants and other mutations retain
immediate capacity rejection. Queue expiry remains a failed request/HTTP503.
The queue is not FIFO: newly arriving work can acquire a free permit. Its one
second limit covers permit waiting only, not subsequent SQL checkout, callback,
external calls or the whole HTTP request. Two reserved slots are not protected
against unrelated repositories or plain queries.

The one-second bound accommodates nine waves for fifty requests with a synthetic
100ms admitted service time using six read slots (about900ms total). The former250ms bound accepted
only24/50 in the original eight-read-slot control. A separate true-service
Confirm test found eight read slots could reject all three concurrent confirms;
the six-read sublimit preserves mutation headroom, not guaranteed write admission. This is not a measurement of historical database
latency. A single replica cannot promise50 requests at500ms per admitted request
within the candidate2s latency goal; scaling and shorter transaction work remain
necessary. Real PostgreSQL tests cover live binding, same/different subjects and
recordings, cancellation, terminal authority and DSR exclusion.

VOD package observation/projection and standalone-live refresh now run between
short subject transactions. The final read rechecks the subject and current
recording/binding, merges progress monotonically into fresh capture control,
and rejects a changed dependency. Active broadcast watch still uses one subject
transaction without Asset I/O. These code-path fixes do not establish the first
blocking operation in a historical incident.
Core member media reads and heartbeat requests classify an incoming canceled
request as `499 request_canceled` only when the request context is canceled and
the returned service error wraps `context.Canceled`. Watch, live list, heartbeat,
metrics, interactions and live cover use this classification; independent
repository failures, dependency deadlines and admin routes retain their existing
error mappings. These cancellations remain unsuccessful requests, not successful
SLO operations. The classification does not fix the reason a client disconnected.

Broadcast repository callbacks explicitly receive the `cms.broadcast_callback`
context, including nested mutation callbacks. Asset dependency spans therefore
nest under the callback stage rather than beside it. The child context retains
its incoming deadline, cancellation and provenance; this does not move network
I/O, change transaction ownership/lock order, or add driver-level SQL tracing.
Parentage is tested through real PostgreSQL, recording service and Asset HTTP
transport with a local fake dependency; production collector delivery is separate.

Broadcast health reads, periodic signal reconciliation and staff preview grants perform Asset I/O between short database transactions. The final transaction rechecks current authority before applying a result or returning a preview credential. Member cover bytes are fetched outside the subject fence and returned only after a fresh subject/recording/cover check. Emergency close first persists revocation without requiring Asset availability; the existing durable range reconciliation applies producer policy afterwards. Cover retain/release also runs outside SQL: prepared reference IDs survive lost responses, short final transactions repeat CAS/revocation/generation fences, and cleanup commits disposal before calling Asset. Auto-cover first commits its reference intent. Asset release tombstones prevent a delayed retain from reviving the same reference. Concurrent selections may retain two distinct intents, but only one CAS commits; the losing intent remains recoverable by reconciliation. No schema, OBS, pool, CPU or scaler changes are included.

## Admin source-linked bulletin correction

Source parts are bound to the current immutable extraction, PDF checksum,
canonical version and saved draft revision. The native worker prepares only
privacy-filtered, bounded sanitized PNGs in Asset's permanently private
`cms.weekly.image` namespace. Admin restoration accepts identifiers and reading
position, never caller pixels/geometry/ownership or proof. Scanning may return
`waitingForScan`; missing bytes do not prevent staff removing an image. Member
responses and publication require clean owned bytes through Asset's private,
issue/checksum-bound illustration route. Every read rechecks current Asset
authority without creating or revoking service grants. Deploy the additive Asset
route before this CMS client.

New snapshot-backed conversions use immutable V11, retaining source pages and
fresh native layout proofs; frozen V1–V10 publications remain unchanged. Ship the
reviewed shared UI/client consumers and private Asset namespace before activating
new output. The extractor image build verifies V11, so an older renderer image
cannot release this implementation. Publish a reviewed immutable renderer image
through the normal workflow before merge/release. Pin its source commit and
image in `scripts/extractor-producer.json`; after compatible readers deploy,
select that same immutable image as the production release input. The previous-issue pilot is isolated;
latest/other-issue reconversion and live publication require separate acceptance.

### Bulletin query and verification cost

Source repair catalogs read only current repair authority and matching immutable
extraction evidence in one nonlocking MVCC statement. Migration 106 adds the
`(document_id, created_at DESC, id DESC)` extraction-history index with a bounded
concurrent build, definition checks and retry handling. Published migrations and
reader mutation/DSR fences are unchanged. Apply the additive index through the
normal migration Job; do not rewrite existing content or publication pointers.

CI and release keep all eleven native renderer versions in four balanced groups;
512 MiB parser smoke, 1CPU/2GiB isolation, image identity and aggregate gates remain
required. Tracking: https://github.com/HallelujahHomeChurch/hhc-web-api-archive-20261109/issues/339.

## Release verification during a reviewed maintenance window

Acceptance is bounded to 15 minutes; it never waits throughout a maintenance window.
Normal releases keep full public smoke inside the deploy job. Runtime/worker
recovery requires confirmed negative deployment or exact-candidate evidence;
unknown observation or public transport failures block acceptance without automatic
rollback. `RELEASE_PUBLIC_EXPECTATION` defaults to
`normal`; an unexpected public503 remains a failure. For a separately reviewed
maintenance window, the nonsecret repository variable may explicitly be set to
`maintenance`. This does not enable Gateway maintenance: the smoke also verifies
all traffic-bearing Gateway revisions are configured `MAINTENANCE_MODE=on`, and
requires the exact503 JSON, `Retry-After: 300` and private/no-store headers. Home,
unauthenticated admin denial and exact ACA candidate image/readiness/active traffic
checks remain mandatory. The gate uses no Azure exec, private Dapr probe
or replica restart polling. Public ready/OIDC checks still require200.

A successful deployment under maintenance emits **pending public acceptance**.
The separate `public_acceptance` job intentionally fails with
`PUBLIC_ACCEPTANCE_PENDING` while the Gateway remains in maintenance. It does not
emit accepted outputs, publish the OpenAPI pointer or seal an accepted extractor
checkpoint. This expected final-job failure does not roll back the independently
successful deploy job and does not leave a runner polling throughout the window.

After the separately authorized reopening, select **Re-run failed jobs** on the
same original release run (`gh run rerun RUN_ID --failed`). Do not use a new manual
deployment or Re-run all jobs as an acceptance shortcut: those rebuild/redeploy.
The acceptance job requires retained successful deploy outputs, validates the
original deploy job's SHA/attempt, rechecks the exact current revision and immutable
image, watermark Event Job image/release/`--once`/maxExecutions (and that the retired worker App is absent), and extractor job
image/activation before and after all unchanged public contract/rollout checks, and confirms
Gateway maintenance is off. Superseded/missing identities or unexpected responses
fail closed. Only accepted outputs unlock the existing OpenAPI publisher and
checkpoint sealer; original extractor verification attempts stay attached to their
receipts. Clear the maintenance-expectation variable after the window so subsequent
releases use normal verification.

A genuine failure after reopening remains unaccepted and requires a reviewed
rollback decision using the existing revision/worker recovery path; it does not
silently retry to green. Runtime rollback does not reverse migrations. The existing
workflow concurrency serializes deployment and failed-job acceptance runs; a newer
revision also makes the old run's acceptance fail. Expired extractor artifacts
cannot be replaced with unrelated evidence.

[GitHub reruns](https://docs.github.com/en/actions/how-tos/manage-workflow-runs/re-run-workflows-and-jobs)
retain the original SHA/ref and original actor privileges. Local fixtures exercise
retained outputs, skipped completion gates and identity failures; the hosted
maintenance/reopening/failed-job cycle has not been exercised and is not production
acceptance. Missing retained outputs are fatal rather than guessed or reconstructed.

If public acceptance already succeeded but OpenAPI publication or checkpoint sealing
failed, do **not** rerun only the downstream failed jobs: both completion jobs require
acceptance from their current workflow attempt. Explicitly rerun the original
`public_acceptance` job and its dependents (`gh run rerun RUN_ID --job JOB_ID`),
which revalidates the deployed identities and all public checks before retrying
completion. Missing or older accepted-attempt outputs fail before publication.
GitHub documents that [rerunning a job also reruns its dependent jobs](https://docs.github.com/en/rest/actions/workflow-runs#re-run-a-job-from-a-workflow-run).

GitHub Actions publishing uses the main-only `production-actions` environment
and the `hhc-web-api-production-actions` concurrency group. Release checkpoints
use schema3 with digest-only image identities. Same-SHA retries must preserve
those digests; previous successful releases must be ancestors of the requested
source, preventing an old release from overwriting a newer accepted deployment.
Missing or incompatible checkpoints require full extractor verification.
Successful environment-only or documentation-only releases do not replace the
last image checkpoint. Their complete job metadata must prove that deployment,
acceptance and sealing were skipped; unknown or failed releases still fail closed.
Runtime override manifests are excluded from native inputs; reuse still requires
matching runtime and verification fingerprints. Recording source changes retain
full native verification through the extractor's PostgreSQL dependencies.

Environment-specific deployment targets and credential-version pins are protected
environment secrets; cross-job outputs carry digests and reconstruct exact private
references only inside trusted jobs. Public rollout artifacts contain whitelisted
results and metrics, never raw cloud configuration or request identifiers. Detailed
cloud state remains available through the private Azure control plane.

### Entitlement failure tracing

Operations entitlement calls now propagate request/trace correlation and report
sanitized failure phases without changing the three-second authorization deadline.
See [the read-only incident runbook](docs/operations/entitlement-failure-tracing.md)
for DB versus Dapr attribution, privacy boundaries and release acceptance.

Runtime templates reference platform-owned resources as `existing`: `apiIdentity`, `migrateIdentity`, `runtimeVault`, `migrationVault`, `watermarkQueue`, `watermarkPoisonQueue`, `translationRAIPolicy`.
Provision these through azure-infra before initial service deployment; routine
service releases do not create or overwrite them. Existing identity IDs and
secret references stay unchanged. Explicit permission-bootstrap flags still
control their existing role-assignment scope; this change does not grant access.


### Release verification without remote execution

The release verifies the exact immutable image and candidate revision through ACA
metadata (ready, active, Healthy, Running and 100% serving traffic where ingress is
configured), then retains the public HTTP acceptance contracts. It creates no
verifier resource and opens no Azure console. Public channel or contract failures
block acceptance; they do not by themselves prove the application is unhealthy.
Recovery requires a successfully observed failed deployment or the exact candidate
in Failed/Degraded/Unhealthy state. CMS also treats a confirmed CPU, memory or
automatic-extraction flag mismatch on that exact candidate as negative evidence. ARM query failures and incomplete identity/traffic
proof remain unverified and fail the release without automatic rollback.
Existing database/migration and resource-specific recovery boundaries remain.
This gate does not claim an in-container socket inspection or private Dapr
invocation proof. No merge/deployment or live recovery evidence follows from local
fixture tests.

## Recording share previews

`GET /api/recording-previews/{id}` returns only the title of a currently visible,
published and unexpired recording; `/cover` returns its selected JPEG cover.
These fields are intentionally public for link cards. Both responses are
no-store and noindex; external bots can still retrieve and cache them. No public
recording list, description, playback grant or private asset URL is exposed.
Member cover and playback APIs retain their existing authorization.

## Stable recording public identifiers

Migration 107 adds a case-sensitive, unique 11-character public identifier reservation
for each recording. New upload and broadcast creation assign it in the same transaction.
UUIDs and existing routes remain unchanged. Reservations deliberately have no cascading
foreign key: deleting a recording must never permit another recording to reuse its URL.
The optional `publicId` response field is absent for historical rows until backfilled.

Apply the additive schema only through the normal reviewed migration Job. The existing
migration executable also supports a separate maintenance mode against that schema:

```sh
/hhc-web-migrate --recording-public-ids=plan
/hhc-web-migrate --recording-public-ids=apply
```

`plan` reports total and missing counts without allocating IDs or running migrations.
`apply` commits at most 100 assignments, reports remaining missing rows and exits;
repeat the reviewed Job invocation until missing is zero. Both use one connection and
the existing two-minute command deadline. Each batch locks only selected recordings
with `SKIP LOCKED`; concurrent batches/creators are safe and reruns preserve prior IDs.
A zero assigned batch can mean another worker holds the remaining rows: completion
requires the missing count to reach zero after older producers are retired. Cancellation
rolls back only the current batch. Never remove the table on application rollback.

Production approval preview: one new table with two unique indexes and a format check;
no existing row is changed by the schema migration. Each approved backfill invocation
adds at most 100 mapping rows and neither edits recording content nor publishes it.
Obtain production counts with `plan` and review them before authorizing `apply`.
This change does not activate short-URL link producers or tighten legacy-row constraints.

`GET /api/video-previews/{publicId}` and `/cover` apply the same current eligibility
as UUID share previews. The JSON remains title-only. Eligible title responses on
both paths can carry `Link: </video/{publicId}>; rel="canonical"` for the site's
legacy redirect; unavailable records never return a mapping header. Public IDs are
case-sensitive locators, not authority. `GET /api/member/videos/{publicId}/watch`
uses the same trusted Gateway, membership, legal and subject/recording checks as
the UUID watch route. Watch, member lists and live/upcoming items optionally carry
`publicId`; clients must tolerate absence until backfill completes.

### Private published locations alias

The production runtime can enable `GET/HEAD /priv/gateway/published-locations`
on its existing internal listener with `CMS_GATEWAY_LOCATIONS_TOKEN`. With the
variable unset the route is absent. Use a dedicated 64-character lowercase
hex secret in the `X-HHC-CMS-Locations-Token` header. Dapr and self-declared
caller headers cannot be combined with this credential.

The alias reuses the published locations projection and its deadline and
cache policy. It does not change the public `/api/locations` route, existing
Dapr authentication, or ACA ingress. Credential configuration and Gateway
routing are separate rollout steps.

## Failure evidence

Slow/failed HTTP calls retain a bounded entry/body/cancellation record even when
trace sampling declines the request or ordinary logging is set to errors.
The API runtime pool observes pgx connect/reset phases in request context, with
process-wide pool-wait deltas labeled separately from request timings. Its
independent scheduler timer reports delayed execution and GC pause counts; those
are correlation evidence, not proof of CPU throttling. Migration pools retain
their concrete driver type.

Operations entitlement diagnostics distinguish DNS, TCP connect, TLS handshake,
request write, response headers, response body and decode; header wait includes
remote processing and is not proof of a network fault. Exporter errors produce
rate-limited cumulative SDK-error counts (not a count of lost spans).

Fault fixtures run locally/CI only. Application entry timestamps cannot measure
pre-handler ingress/socket queueing, and cancellation timestamps indicate when
the process observed cancellation rather than when the peer sent it.

Runtime database diagnostics retain every failed or >=250ms connect, session-reset
and query-lifecycle operation independently of span sampling/log verbosity. Query
lifecycle includes result consumption through row closure, not server-only SQL
time. Query text, arguments and raw errors are never recorded. Runtime connections
carry the ACA revision as PostgreSQL application_name (a deterministic bounded tag
for names longer than63 bytes), so rollout drain checks can distinguish revisions.

Trace exporter startup records distinguish a missing endpoint, failed constructor,
and initialized exporter even at stricter log levels. Bounded cumulative batch/span
records report successful versus failed OTLP calls (first completed batch, at most
once a minute, and a bounded shutdown snapshot). An acknowledgment is not proof of
backend storage; failures may have reached the collector and are not dropped-span
counts. SDK queue loss and full-trace tail retention are not measured here.
CMS server spans use a fixed request target and three fixed resource attributes;
the original URL, host and headers are restored only for application handling.

## Runtime environment updates

Edit `infra/runtime-env.json` for runtime-only settings. Each owned App or Job maps
an environment name to `{ "value": "20" }`, an existing
`{ "secretRef": "secret-name" }`, or `null` to remove it. Keep explicit `null`
entries; deleting a key does not mean restoring a template default. Do not commit
secret values or use this file for build arguments, new secret resources, scaling,
identity or ingress changes.

A PR changing only this file skips application verification. A main push changing
only this file uses the `runtime_env` job in Production Release: it preserves the
serving template/image and updates env without building, scanning, migrating,
starting Jobs or running smoke tests. Apps retain their existing single-revision
mode; Azure's deployment response determines success. The shared production lock
serializes this with image releases. Superseded desired env is skipped; a newer
unrelated documentation commit does not discard the pending config update.

Normal and mixed changes retain the existing release path. Bicep consumes the
same manifest for all six owned runtime containers, so later image releases do
not reset the overrides. Empty resource maps preserve today's defaults. Runtime
setting mistakes may therefore surface at startup or use; revert the desired
values in this file to publish a correction.

Normal forward deployments load the latest merged runtime manifest under the production deployment lock, while keeping their immutable code/image identity. Retained-release verification keeps its original evidence.

`RELEASE` is reserved for immutable artifact identity and cannot be overridden. `BULLETIN_EXTRACTION_ENABLED` and `BULLETIN_AUTO_EXTRACTION_ENABLED` must use literal `{"value":"true"}` or `{"value":"false"}` (no null or secretRef); normal deployment readiness and retained-release evidence use these effective boolean values. Other runtime settings retain value/secretRef/null support.

Runtime configuration recovery: revert the manifest values in a reviewed PR and
use the same environment-only release. For an authorized emergency, use this
repository's existing healthy-revision rollback, then reconcile the manifest
before the next release. Application images and database migrations keep their
existing recovery contracts; configuration rollback never down-migrates data.

Pure Markdown-only changes (`**/*.md`) do not trigger automatic release workflows.
Non-Markdown configuration and mixed source changes retain their existing gates.
Markdown-only PRs do not start CI or PR cache-cleanup workflows.

### CMS build cost boundaries

CI authenticates the fixed renderer pin with the existing producer release proof
before using its registry digest. Missing, expired or mismatched evidence keeps
the exact source checkout/build fallback. PR artifacts and caches do not become
production release artifacts. Renderer/extractor native security refresh dates,
scans, full-verification fallback and evidence retention are unchanged.

The API build compiles its four required executables; the watermark worker has
its own build stage and explicit Go package inputs. API command changes no longer
invalidate the worker compilation. New worker dependencies must be added to its
COPY inputs; the actual worker build fails if any are missing. This is local
implementation evidence, not a measured Actions saving. Checkpoint sealing is independent of OpenAPI publication; a publication failure
still fails the release while preserving accepted, source-bound extractor proof.
Cross-run production artifact promotion remains disabled.
Publication cleanup persists user provenance in new retirement, unpublish and
compensation payloads. Legacy compensation recovers the original actor only from
the referenced durable publication event with matching aggregate/version/type;
legacy bulletin unpublish uses its exact workflow. It never substitutes the
current resource owner. Unrecoverable sources retain the existing safe failure
and retry behavior; no backlog is replayed or marked complete. Extra payload
fields are ignored by old workers, so mixed versions remain compatible.

Failure and >=1s request summaries retain bounded per-phase count, total/max work,
status and fixed reasons independently of trace sampling. Recording lists expose
lifecycle, list query, package calls, projection and serialization work; DB and
Asset client operations accumulate even below individual slow thresholds. These
are overlapping work durations, not additive wall time. Implicit SQL checkout
and uninstrumented handler work remain unknown; global pool deltas are explicitly
all-request counters.

Paginated member recording lists synchronize lifecycle before SQL pagination,
then fetch canonical ready projections for the page in one bounded Asset batch
(at most 100 recording/package pairs). The lookahead stays outside that batch;
per-item missing/denied media can be omitted while the cursor still advances.
Whole-request, protocol and cancellation failures fail the page. SQL reads remain
per item, and the existing lifecycle HTTP call remains. Legacy non-paginated
lists, playback, watch and heartbeat keep the single ready path. No cache,
parallelization, retention, pool or deadline changes are introduced.

## Azure bulletin conversion

`BULLETIN_LLM_EXTRACTION_ENABLED` selects the source-linked LLM flow for newly
queued general Traditional Chinese bulletins. The API and extractor must enable
it together through the normal image release. CMS translation/summary generation
uses `hhc-content-ai` (Azure gpt-6-luna, 2026-09-22); bulletin recognition and its
independent source verification use the pinned `hhc-bulletin-ai` deployment
(gpt-6.1-terra, 2026-10-26). The embedding deployment remains unchanged.

The existing Event Job downloads the immutable PDF, removes excluded service
rosters from both text and pixels, then sends each whole permitted page to Azure.
LLM roles replace the old semantic parsers, learning and separate classification.
Native text is copied from whole source IDs or exact Unicode scalar ranges using trusted Poppler character positions; outlined text is transcribed and checked
against the original page. Every source image must be accounted for; retained
illustrations use sanitized owned Asset bytes and trusted source bounds. Unknown,
missing, duplicated or reordered source references fail closed. Each page uses
one recognition call and a second independent source-only recognition, with at most
one repair. Ordered article/title/speaker inventories and source scalar ownership
must agree before a code-owned semantic receipt is issued. Title/person separators
are structural reader slots; names never include them. Numeric frame bounds aid
the model without invoking the legacy semantic parser. Verified article context
and a rolling previous-page hash bind cross-page continuations and checkpoints.

A ten-minute extraction lease, 720-second replica limit and 780-second queue
visibility permit bounded execution without a resident converter. Verified pages
resume from private job checkpoints after a timeout; legacy extraction, layout
and Simplified conversion retain their existing deadlines. Completed checkpoints
remove proposal text while retaining request/token usage; failed/superseded
checkpoints expire after seven days when the next Event Job runs.

The fixed renderer measures the saved Local once. Automatic confirmation and
publication share the existing transaction, audit authorizer, proof and mapping
checks. Only an exact verified extraction -> measured Local lineage with zero
issues, no Incoming, unchanged metadata/source and a currently published PDF can
publish. A concurrent authored Local cannot qualify. The existing Simplified
release continues after Traditional publication. Layout/source/mapping failures
remain unpublished and visible in the existing staff workflow.

The runtime environment manifest activates the new path and Luna consumer.
Rollback uses the repository release/rollback workflow and the previous serving
image/configuration; gpt-4.1-mini remains deployed for rollback. Do not activate
this flag through an environment-only deployment: the Job replica deadline is
also part of the normal release. No historical backlog is automatically requeued.

Paginated member recording cards also expose optional nullable
`playbackMediaVersion`, independently of `playbackProgress`. It identifies the
currently eligible VOD, including the applied public broadcast range revision,
even before the account has saved history. Consumers may match an active local
owner against this field without guessing a range or issuing per-card reads.
Unknown/ineligible versions remain null; legacy responses can omit the field.
This read-only projection never claims an owner, rewrites live history or maps
unknown historical live coordinates onto VOD.
