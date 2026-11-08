# SDD ledger — plan: docs/superpowers/plans/2026-10-10-bulletin-template-producer.md

Isolated branch from API195 9e3f076; no content/source/production writes.
Pre-flight: API195 required immutable snapshot matches renderer116 V9; old null
jobs retain frozen V8 and replay input. Consumers must release before activation.
Candidate/routing RED→GREEN: snapshot copied before validation, three source
bands validated by fixed labels and bounds, not changing values. Malformed or
missing slots remain located checks. Legacy paths reject V9 rather than guess.
Native4-page V9 Hant/Hans initially failed closed because wrapper allowlist was
v[2-8]; updated only to v[2-9], then both passed within1CPU/2GiB/no network/read-only.
Full Go vet and race/Postgres suite passed; npm OpenCC dependencies installed from
existing lock without new dependency. No runtime timeout or container limit change.

Private current originals:1739 Hant+Hans12pages passed in first run.
1740 Hans first run hit60s deadline; same-limit quieter retry passed Hant20.40s,
Hans35.66s.1741 Hant23.85s/Hans39.14s passed24pages. Thus all six original/native
V9 regressions passed with0 overflow and fixed geometry/page membership assertions.
Timeout root cause is unconfirmed; retry is evidence, not a claimed diagnosis.
Reports /tmp/bulletin-template-v9-corpus-20261010.kahetH and .Q7X39E.
Remaining semantic checks are unknown_body_kind25/35/46; none silently accepted.
These are local original-source tests, not formal operator/device acceptance.

Complete V1–V9 isolation passed at unchanged limits (215.86s), with continuing
successful readiness samples and peak memory below550MiB. Initial contention-run
failures were not waived: selected diagnostic and complete quiet rerun both passed.
Log /tmp/bulletin-template-producer-isolation-final-20261010.log.
Renderer116 release38039260387 succeeded. Manifest read-back proves linux/amd64
ghcr.io/hallelujahhomechurch/hhc-bulletin-renderer@sha256:fdc897e53f76507f6ca22099acef781d5a77952450260f6fd79391097f5d27c3.
Actual CI producer ref a671cef2f45d84ae35c88087ebb96f1730bfcc33 and artifact
e38c5bb6fea134ffdf56cbc542f5c20a9a82c8e03b7a5653ebf0eabfd7bb0997 are pinned.
Repository runtime lock remains V8 until reviewed activation sequence. Single
branch review, required CI and normal release remain; activation gated on both
Admin/Web V9 consumer releases. No data/settings/content writes.

One independent whole-branch review found an out-of-band duplicate fixed label
could escape located review. RED duplicate_moved/duplicate_left tests reproduced;
GREEN counts labels across the cover header before accepting their fixed row.
Post-fix domain/extractor tests and vet passed. Rebuilt local image; all six
1739–1741 Hant/Hans original-source/native runs passed again with0 overflow
(report /tmp/bulletin-template-v9-corpus-20261010.FgyBUw).
Final Go race/Postgres suite passed before that local source-row fix; final
source-row domain/native tests passed after. Review issue fixed, no re-review.
