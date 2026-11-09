# Bulletin automation implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans inline, task by task; one fresh whole-branch review at completion.

**Goal:** Complete authorized upload/reupload-to-publication automation with verified Azure LLM semantic decisions and historical annotation fallback.

**Architecture:** Typed source/update decisions feed existing reconciliation, merge, native composition and transactional publication. Durable intent, checkpoints and exact-source/version fences bind every stage. No standalone agent service.

**Tech Stack:** Existing Go/database/sql/PostgreSQL/Azure Responses adapter, Event Job/queue, published TypeScript clients and React admin.

**Spec:** docs/superpowers/specs/2026-11-09-bulletin-autopilot-design.md

## Global constraints

- Use the new public canonical CMS repository; never push archived private history or private source/member data.
- Preserve authorizer/audit/lease/source/metadata/proof checks and explicit draft/unpublish intent.
- Uncertain old mappings retain history without blocking verified source; uncertain source readiness retains the healthy publication.
- Existing supported editions only; no new provider, dependency, service or infrastructure permission.
- Normal PR/required-CI/merge/release/live-smoke sequence remains mandatory.

## Review focus

- A reupload/unpublish during inference must fence the old completion, including identical bytes under a new asset.
- Equivalent names in different articles must not exchange private annotations.
- A source-identity mismatch or missing article must never become an automatic deletion/publication decision.
- An authored draft must not publish without its durable exact-source authorization; immutable evidence cannot inherit another job's lease.
- Repair/retry and derived publication must stay bounded and must not duplicate paid work, notices or mutations after cancellation.

### Task 1: Semantic mapping application

**Files:** Create internal/onlinebulletins/semantic_mapping.go and semantic_mapping_test.go; reuse mapping.go, componentBlocks and layout fragments.
**Interfaces:** ApplySemanticMappings(previous, next Document, decisions []SemanticMappingDecision) (Document, []SentenceMapping, error); SemanticMappingDecision contains fromSentenceId, kind and toSentenceIds.
- [ ] Write RED tests for one-to-one identity retention, splits/merges, historical fallback for missing/changed anchors, source/edition binding, duplicate/foreign target rejection and nonmutation.
- [ ] Run `go test ./internal/onlinebulletins -run SemanticMapping -count=1`; expect RED before implementation.
- [ ] Implement exact ID/ownership validation, code-owned stable rekeys and proof invalidation; reuse PublicationMappings for final structural validation.
- [ ] Run the new tests plus the complete onlinebulletins package; expect PASS. Commit the self-contained primitive.

### Task 2: Verified Azure update decisions

**Files:** Add internal/onlinebulletins/update_plan.go, azure_update_plan.go and tests; reuse AzureExtractor's bounded transport and existing comparison types.
**Interfaces:** typed update input binds canonical/source hash, base/local/incoming/published snapshots and source evidence. Structured output references valid existing units/IDs and source metadata only. Independent passes decide readiness; mapping uncertainty uses Task1 historical fallback.
- [ ] RED: ready/incorrect-issue/incomplete source; harmless reflow/correction/structure/manual-override cases; injected source instructions, invented IDs/text, disagreement and model/provider failure.
- [ ] Implement typed schema/prompt, bounded two-pass receipt, deterministic input/decision hashes and safe source selection. No private reader-note text enters requests.
- [ ] Run targeted and full affected tests; commit.

### Task 3: Durable automatic-update stage

**Files:** Add the next immutable migration, internal/postgres/online_bulletin_updates.go and DB tests; extend worker.go, online_bulletin_wake.go, cmd/extractor wiring and tests.
**Interfaces:** consumer-owned update store claims/records/applies an exact source-bound plan; existing layout proof is required after any content/identity change. Add stage to existing Event Job rather than an additional worker.
- [ ] RED: fresh/existing Local, auto-adoption, compatible manual edit, same-source reuse, CAS/lease/ABA/cancellation/new-upload fencing, malformed proof, timeout and duplicate delivery.
- [ ] Persist bounded attempts and plan evidence; call Tasks1/2, existing merge/layout/publication helpers and queue wakes. Extend due-work accounting for delayed retries.
- [ ] Run disposable PostgreSQL and worker tests plus affected race suite; commit.

### Task 4: Authorized PDF/online release intent

**Files:** bulletins upload/domain types, HTTP handlers/OpenAPI, postgres upload/publication/automatic release and tests.
**Interfaces:** additive optional autoPublish input captures explicit publisher-authorized intent at upload/reprocess completion. Worker publishes only the current verified asset and exact measured revision; manual draft/unpublish cancels it.
- [ ] RED: publisher/nonpublisher, explicit draft, first/update PDF, scan wait, metadata correction, partial failures, stale input, double publication and first-notification deduplication.
- [ ] Reuse existing asset publication and online transaction paths with durable intent and idempotent recovery; retain last healthy projections until replacement is ready.
- [ ] Run API/OpenAPI/domain/PG checks, complete required CMS lanes; commit.

### Task 5: Bounded repair and reuse

**Files:** existing recognition/worker/layout/simplified adapters and focused tests.
- [ ] RED: exact bytes under new asset reuse only with matching source/protocol/canonical/context; layout presentation repair preserves source words; semantic repair/provider failure remains bounded; derived source changes fence old work.
- [ ] Reuse trusted immutable evidence with fresh source authority; implement bounded typed repair/assessment within existing job budgets and version rules. Keep supported-language and notification policies.
- [ ] Run native source/compose/proof and complete affected test lanes; commit.

### Task 6: Published client and operator integration

**Repositories:** frontend-platform SDK and admin-fe, isolated branches from current origin/main.
- [ ] Add compatible client inputs/status types against the released canonical API; verify/publish through existing package workflow.
- [ ] Admin upload/reprocess captures one automatic-publication choice for authorized staff; normal handling shows processing/online/exception status and summary rather than mandatory manual comparison/confirmation.
- [ ] Preserve explicit draft, correction, history and existing manual exception tools. RED→GREEN tests cover authorization, concurrent update, unknown write outcome and recovery.
- [ ] Full frontend tests/lint/build, bounded desktop/narrow QA and one review per branch; normal CI/release.

### Task 7: Final review, release and acceptance

- [ ] One fresh whole-CMS-branch review; fix Critical/Important with RED→GREEN and whole-suite verification; record all rulings/minors.
- [ ] PR/allrequiredCI/merge/normalrelease; release compatible SDK/consumer in dependency order.
- [ ] Real current-issue automatic update, PDF/readers and Simplified acceptance with immutable release/source evidence; no direct DB publication writes.
- [ ] Update owning issues/private Feature/Project, archive private evidence, clean only owned clean temporary worktrees after successful release/smoke.
