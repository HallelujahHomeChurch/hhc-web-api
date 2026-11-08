CMS database Jobs now verify their authenticated login, finite role connection limit and non-elevated role flags before migrations, content import, extraction or watermark work. This closes the gap between a declared Job secret reference and the actual database role enforcing the shared budget. API, operator-only Home V2 tooling and the rollback-only persistent watermark App keep their existing login paths. Watermark `--once` always requires the Job gate; ACA Job preflight is identified by its platform-provided `CONTAINER_APP_JOB_NAME` marker.

The guard performs one bounded read-only catalog query and fails closed on unavailable/wrong/unlimited/privileged credentials; it never changes roles, credentials or server limits. Startup must open one connection to inspect identity, so manual execution admission still belongs to the fleet rollout plan and is not claimed unbounded-safe.

Validation: role predicate cases, real disposable PostgreSQL capped/wrong/unlimited/privileged/SET ROLE cases, affected command race tests and vet. Full owner CI required; no production execution or provider calls performed.

Original discussion: https://github.com/HallelujahHomeChurch/hhc-web-api-archive-20261109/issues/456. Canonical Feature: https://github.com/HallelujahHomeChurch/hhc-planning/issues/26; planning ca186f025d021c5e2cfe849c05cafcc3f20ada57.
