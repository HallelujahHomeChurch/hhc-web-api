# Upload-once bulletin publishing implementation plan

> Use native execution with fresh independent review of the complete change. Tasks use behavioral regression tests before implementation.

**Goal:** Upload once produces member PDF, Traditional and Simplified editions without routine administrative orchestration.
**Architecture:** Extend the existing CMS owner, Event Job and versioned SDK; reuse existing UI controls and member projections.
**Tech Stack:** Go/PostgreSQL, TypeScript/React, published pnpm packages.
**Spec:** ../specs/2026-11-16-bulletin-product-flow-design.md

## Global constraints

Preserve source/permission/CAS/idempotency/cancellation checks and healthy publication fallback. No new service/provider. Normal UI contains no raw diagnostic codes, UUIDs or version-management procedures. Reuse published dependencies for final consumers. Separate local/CI/release/live acceptance.

## Review focus

- Stale response/worker after replacement must not publish old source.
- Metadata correction must preserve old downloads/links until coherent replacement and reject issue-number collision.
- Partial simplified failure must not turn healthy PDF/Traditional availability into an error.
- Retry and correction must not repeat notifications or revive explicit withdrawal.
- Browser reload must neither require manual phase selection nor lose an accepted operation.

### Task 1: CMS product operations and truthful outcomes

Files: internal/bulletins, internal/onlinebulletins, internal/postgres/repository.go, online_bulletin_* workflow modules, internal/httpapi, openapi.yaml, additive migrations if needed.
- [ ] Trace upload→automatic release→derived publication and metadata publication/read contracts completely. Record exact command/projection interfaces before consumer work.
- [ ] Write failing real-PostgreSQL tests for the acceptance/review cases, then implement minimal durable orchestration/recovery and staged corrections.
- [ ] Preserve legacy mutation behavior; new publication-authorized contract governs corrections and retry. Reuse stored successful evidence/stages.
- [ ] Run affected DB/race tests, Go suites, contract/migration checks. Independent review before integration.

### Task 2: Typed contract and gateway compatibility

Files: frontend-platform/packages/hhc-web-client/src and tests; owning gateway allowlist only if new route required.
- [ ] Match verified producer interfaces using existing client/error/idempotency conventions.
- [ ] Test exact transport payloads, errors and cancellation; run required package checks.
- [ ] Publish through normal dependency workflow before final consumer pin. No unreleased override in deliverable.

### Task 3: Outcome-focused Admin workflow

Files: src/pages/BulletinDetailPage.tsx, bulletin list/new flow, src/pages/bulletins/OnlineBulletinEditorPage.tsx, PublicationJourneySummary and progress/recovery components; typed adapters/mocks/tests.
- [ ] Replace duplicate normal status/control surfaces with one concise issue outcome and simple availability links.
- [ ] Connect single Resume/update and save-correction actions to producer; automatic same-key reconciliation survives reload. Keep deliberate manual editing/draft/unpublish secondary.
- [ ] Remove diagnostic-oriented tests and replace with user-result tests; preserve security/concurrency assertions.
- [ ] Verify desktop/mobile/keyboard flows and no technical identifiers in ordinary UI; run full tests/lint/build.

### Task 4: Integrated delivery and member acceptance

Files: owning tests/evidence and member route only where correction aliases require changes.
- [ ] Review complete cross-repository diff and resolve findings.
- [ ] PRs and all required CI; producer→SDK→consumer normal release sequence.
- [ ] Preview exact live acceptance operations before authorized production content changes.
- [ ] Verify fresh upload, replacement/correction, recovery/reload, PDF download parse, Traditional and Simplified published readers. Record actual revisions privately, sanitized outcome publicly.
- [ ] Keep worktrees until all scope and release verification complete.
