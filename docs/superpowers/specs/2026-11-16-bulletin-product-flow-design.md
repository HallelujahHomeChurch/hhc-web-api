# Upload-once bulletin publishing

## User-approved direction

The user approved the preceding complete architecture review and explicitly requested implementation across frontend/backend, not hidden text. Normal authorized General Traditional bulletin publication is upload once, leave the page, and receive a usable PDF plus Traditional and Simplified online editions. Exceptions show one understandable reason and action. Existing explicit draft/unpublish intent is respected. This direction is already approved; implementation choices below are coordinator decisions.

## Scope and constraints

Reuse CMS, Asset, existing Event Job, immutable renderer, OpenCC and bounded semantic assessments. No new service/provider. Retain authorization, current-source/CAS fencing, idempotency, cancellation, healthy publication fallback, immutable evidence and notification deduplication. Never include private notes in model inputs or public evidence. Preserve existing supported editions and entitlement rules. Ordinary users see no UUIDs, revision numbers, raw codes, job phases or operational runbooks.

## Product behavior

- Normal upload defaults to automatic publication for publish-capable staff in the supported General Traditional scope. Draft is an explicit secondary choice. Existing issue creation needs only the minimum metadata; avoid duplicate metadata entry, use existing issue data for replacement.
- After the server accepts upload completion, work is durable and independent of an open browser. Status is one primary outcome, with PDF/Traditional/Simplified availability as concise secondary information. Existing healthy versions stay available while replacements are prepared.
- Recognized transient infrastructure/model failures get bounded automatic retries. Invalid source/content requires the specific human correction. Exhausted retry offers one Resume/update action, which the server routes to the unfinished valid stage instead of requiring the browser to coordinate jobs or republish healthy outputs.
- Metadata correction is a single publisher-authorized save/update operation. It stages new issue date/number/title/subtitle and prepares affected versions while preserving current published snapshots. It must not require unpublishing PDF editions. Commit validated metadata/content coherently; preserve old issue-number reader links via an explicit alias when the issue number changes, reject collisions, preserve original entitlement and prevent withdrawn content access. Do not loosen the legacy metadata mutation path for callers without explicit publishing authority.
- Replaced source work is historical, not a current error. Do not resurrect withdrawn authorization. New explicit update/retry captures current authorized intent while preserving previously completed validated stages where compatible.
- Member website consumes only published projections. PDF, Traditional and Simplified may become ready at different times; do not claim all ready early. A failed Simplified update cannot hide a healthy Traditional/PDF publication. No additional notification from retry or correction unless existing explicit policy calls for it.
- Ordinary UI has one primary next action. Authoring and deliberate draft/unpublish controls remain secondary. Technical diagnostics belong in a separately opened support view, not routine content pages. A pending ambiguous response is reconciled automatically with the same command key, never substituted with a new source/version.

## Architecture decisions

Extend existing CMS workflows/contracts with only the missing product operations and outcome projection. Prefer extending current route payloads if safe, otherwise add an explicit CMS command and minimal gateway allowlist entry. CMS owns stage selection, retry and correction authority. Typed SDK exposes the contract without consumer wire duplication. Reuse existing shared controls/fonts/theme; restrained layout around issue identity, file and outcome, no new design system.

Approaches considered: cosmetic hiding does not satisfy workflow correctness; rewriting services throws away working safety boundaries. Chosen approach retains the existing engine and supplies the missing end-to-end orchestration and outcome-focused UI.

## Acceptance

Regression tests first: upload/replace/metadata correction, source races, same-key response loss/reload, explicit draft/unpublish, bounded failure, unsupported source and insufficient permissions. Verify current source coherence across all outputs, notifications not duplicated, previous PDF/online availability retained during failure, old issue URLs safe. Run repository required suites and release checks. Browser acceptance at desktop/mobile and keyboard must verify the normal path has no manual conversion/review/publication or diagnostic codes. Verify member PDF parses/downloads and both actual reader documents render expected published content. Synthetic/local and live/provider/device evidence remain distinct.

## Delivery

Run bulletin-product-flow-20261116, macOS. Coordinator is this chat; no other writer may modify these new worktrees. Planning pin ca186f025d021c5e2cfe849c05cafcc3f20ada57; Feature BUL-02 observed 待驗收. CMS #28 and Admin #324 track incremental work. Branch fix/bulletin-product-flow-20261116 in managed CMS/Admin/SDK worktrees. Normal PR/CI/release workflow only. Production data mutation requires a concrete scoped preview; no private data in Git. Do not declare complete without actual acceptance, and preserve unfinished worktrees.
