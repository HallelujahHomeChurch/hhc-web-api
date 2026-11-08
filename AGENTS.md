# Repository Instructions

## Delivery Policy

- Treat `main` as the production branch. Do not commit or push directly to `main`.
- Create a focused branch for every task and submit changes through a pull request.
- CI must pass before merge. Do not bypass, ignore, or merge around a failing required check.
- Keep changes small and reviewable, and use squash merge unless the repository explicitly requires another strategy.
- For deployable applications and services, a merge to `main` is the release boundary: build an immutable artifact, deploy it through CI/CD, then verify health and the production route.
- Never deploy an unmerged local commit or replace the CI/CD path with an ad hoc production command.
- If deployment fails, preserve the last healthy revision, report the failure, and use the repository rollback path.

## Architecture and local verification

- CMS and website backend: owns source records, revisions/publication, protected bulletins, published website projections and product orchestration. Operations owns church units/resources/meetings; Asset owns bytes/file mechanics. There is no separate cms-api or bulletin-api.
- `cmd/server` serves `internal/httpapi`; domain packages include content, publication, bulletins/onlinebulletins, recordings, legal and sitesettings; `internal/postgres` persists them and named clients call other services. cmd/migrate, extractor and bulletin-watermark-worker are separate workloads, not implicit HTTP startup.
- Read [README.md](README.md), [openapi.yaml](openapi.yaml), [go.mod](go.mod), `internal/config` and [.github/workflows/ci.yml](.github/workflows/ci.yml). Export task-local configuration, prepare disposable PostgreSQL via the migration entrypoint when authorized, then `go run ./cmd/server`. Protected routes require app-token, exact Gateway Dapr caller and sanitized identity plus domain authorization.
- Use CI's grouped Go/policy/extractor/image checks and disposable HHW_TEST_DATABASE_URL. Native renderer/extractor acceptance additionally needs pinned renderer assets/images and the CI fixtures; a plain server or unit-test pass is insufficient. The required verify aggregate rejects unexpected skips.
- Keep published migrations/renderers immutable and retired DSR boundaries retired. Public projections exclude protected bulletins; private headers never replace domain authorization. Content import, watermark workers, feature activation and production migrations require their own task scope.

The commands above are source-defined entrypoints, not a record of successful execution. Report installation, local tests, Linux startup, CI and live acceptance separately.

## Multi-repository Codex tasks

Read the checked-out `hhc-planning/AGENTS.md` and
`hhc-planning/docs/cloud-environments.md` first and record its revision.
Sibling repositories do not inherit it automatically. This repository owns its
setup/tests; use the task’s smallest runtime set and preserve active writers in
isolated worktrees. Follow explicit review/merge/deployment authority. Pure Markdown documentation skips automatic workflows; source, workflow/configuration, executable
skills and mixed changes retain full verification.

If no sibling planning checkout exists, use the [single-repository fallback](https://github.com/HallelujahHomeChurch/hhc-planning/blob/73ee1af2a1ff828d72421a73626f45ff4ce04a6f/docs/cloud-environments.md#single-repository-checkout)
to read the same entry documents through authorized GitHub access at the task’s
immutable planning revision. Record that revision; report the exact inaccessible
target/error without expanding permissions or copying private text here.

## Runtime configuration delivery

`infra/runtime-env.json` is the runtime env override source for Apps and Jobs.
Manifest-only changes skip application CI and use Production Release's env-only
job with the existing serving image; no build, scan, migration or smoke is added.
Use explicit `null` for removals and existing `secretRef` for secret bindings.
Workflow, deployment-script, Bicep and mixed changes still require their relevant
verification. Keep normal image releases consuming this same source.
