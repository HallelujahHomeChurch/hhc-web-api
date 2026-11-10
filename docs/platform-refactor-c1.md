# CMS C1 candidate: banner validation and Asset workload identity

Canonical planning revision: `ca186f025d021c5e2cfe849c05cafcc3f20ada57`. Engineering issue: #12. Draft only; no enablement, deployment or production acceptance.

## Banner owner validation

Scan-pending uploads may still attach to the draft. Publication waits for the existing clean/completed/owned `cms.home.banner` contract, then obtains a distinct one-minute service read grant, downloads at most 10 MiB, verifies the original SHA-256 and JPEG 1900×700 dimensions, and revokes the grant before creating any public grant. Cancellation still attempts bounded revocation; TTL bounds access if cleanup is unavailable. Full bytes are read to bind the dimension evidence to the checksum; this adds one bounded original download per home publication, not per public read. No untrusted browser dimensions or namespace-derived dimensions are accepted.

AS3 delegates product dimensions only for explicitly profiled `cms.home.banner` uploads and only behind its default-off flag. C1 retains legacy unprofiled uploads, for which Asset still checks dimensions. Enable delegation only after this validator is deployed and mixed-version fixtures pass. Old Asset + new CMS preserves both validators; profiled new Asset + new CMS preserves CMS enforcement. Before rolling CMS below C1, disable delegation and verify the AS3 artifact rechecks dimensions before public grants for already admitted profiled banners. Rolling Asset below AS3 additionally requires checking/repairing those previously delegated assets; toggling a flag alone is insufficient. Scan/decode/size safety and immutable byte storage remain Asset responsibilities.

## Default-off workload transport

`ASSET_WORKLOAD_AUTH_ENABLED` defaults false. When eventually authorized, `ASSET_WORKLOAD_AUDIENCE` is the exact Entra application audience (not a `/.default` scope) and `ASSET_WORKLOAD_CLIENT_ID` is that workload's explicit managed identity client ID. No values or identities are provisioned by this PR. The Azure SDK managed-identity credential owns token caching; each request checks expiry and uses a bounded token call. Identity failures are sanitized and fail closed without legacy fallback. Redirects/foreign hosts are rejected and inbound authorization/Dapr/workload headers are removed before the transport injects its token.

API uses `hhc-web-api` with private HTTPS required. Extractor uses `hhc-web-extractor`; watermark uses `hhc-web-bulletin-worker` and requires existing internal/workload HTTPS mode. Owner fields remain CMS; service grants/download subjects use the distinct verified workload. Existing disabled/legacy modes retain their current identity and routing.

Rollout gates: Asset AS2 reader first; a separate narrow Asset worker-authorization follow-up must admit extractor source `cms.weekly.pdf` and output `cms.weekly.image` with purpose `online_bulletin_illustration`, plus distinct service-subject grants. Do not map extractor to the broad CMS principal. Verify watermark own-subject grant/complete paths, I1 issuer/audience/object claims, principal/owner/actor provenance, 401/403/503, token renewal, and backward compatibility before enabling any caller. C2 generic derivation/owner-content remains separate and disabled until its producer contracts exist.

Local candidate tests and hosted CI are separate from deployed identity readiness, native renderer acceptance, device acceptance and realized cost. None of those external gates is claimed here.
