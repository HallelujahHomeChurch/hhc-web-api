# Verified bulletin automation

The user approved the complete semantic automation flow and explicitly requested implementation. This resumes CMS source work after the earlier pause. Normal operation is upload/reupload, verified processing, and automatic publication without per-unit administrator decisions.

## Approved behavior

- The newest valid PDF is authoritative. Preserve compatible authored corrections; source conflicts resolve to the newest valid source. Reject wrong-issue/edition or incomplete source rather than replacing a healthy publication.
- Azure LLM decisions cover source identity/readiness, semantic updates, automatic comparison adoption, one-to-one/split/merge annotation correspondence, bounded content/layout repairs and human-readable change summaries. Use the existing Azure adapter/deployment; no provider or model migration.
- Exact byte equality, valid IDs, permissions, malware status, source/metadata/version binding, leases, deduplication, transactions and cancellation remain deterministic.
- Equivalent content keeps stable sentence IDs. Genuine split/merge correspondence is explicit. Unresolved historical correspondence retains history and never blocks otherwise verified source content.
- PDF/online release requires a durable authorized publication intent. Explicit draft mode and manual unpublish/cancellation override automatic work. A newer upload fences all older stages.
- Retain the existing Traditional/Simplified scope and deterministic Simplified conversion; use semantic assessment for source/derived consistency and recovery. Identifying unsupported languages is not silently enabling new reader products.
- Retain immutable versions and rollback history; hide routine version-management work from operators. Existing notifications remain deduplicated by publication policy; LLM summaries do not authorize additional sends.
- Reuse the existing Event Job/queue and bounded stages. No resident converter, additional service, private-note input to LLM, infrastructure permissions, or unrestricted model-controlled API/SQL calls.

## Approach

A structured LLM update plan references only server-owned source/draft/published objects. Code validates and applies its bounded actions, persists evidence before further spending and rechecks the original intent and exact current source before each write. Reuse stable-ID reconciliation and existing merge/layout/publication functions instead of a separate publishing engine.

One-to-one equivalence is implemented by retaining the old sentence identity before final layout. This reuses the existing reader mapping contract instead of introducing a parallel renamed-anchor protocol. Meaningfully replaced or uncertain old sentences retain historical notes without attaching them to unrelated text. All rekeys invalidate layout proof and are measured again.

Separate semantic content acceptance from optional annotation migration. Independent source judgments must agree on publication readiness; uncertain mapping alone follows the approved historical fallback. Persist inputs/policy/model/source hashes and bounded attempts; do not rely on self-reported confidence.

## Delivery and acceptance

Owner is hhc-web-api, with additive typed SDK/admin integration as needed. Existing service contracts and protected data boundaries remain. Producer and consumers have separate branches/CI/releases. User authorization covers the ordered delivery and current-issue acceptance; no repeated routine approval is needed.

Test new upload, same bytes/new asset, layout-only update, small corrections, structural changes, authored draft, metadata correction, wrong/incomplete source, old annotations, model reprocess, explicit restore, concurrent reupload/unpublish, duplicate event, provider timeout and bounded repair. Use synthetic committed fixtures; private real-source evaluation stays outside Git/Actions. Release through normal workflows and verify actual PDF/reader/derived state; future-week/device outcomes remain separate.
