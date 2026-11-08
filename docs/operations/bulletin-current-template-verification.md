# Current online bulletin template verification

The online format starts at general Traditional Chinese issue 1739. This report
describes private-source local regression evidence, not production acceptance.
Original PDFs, passages, source images and generated reports are not committed.

## Reproduction

Run `TestHistoricalCorpusPrivateOriginal` with `HHC_PRIVATE_PDF_SOURCE`,
`HHC_PRIVATE_CANONICAL_METADATA`, `HHC_TEST_LAYOUT_RUNNER_ROOT`,
`HHC_TEST_LAYOUT_RENDERER_VERSION=v8`, and the frozen renderer digest
`646c849b47e87cc3607abccd3e92ad60a432bc55d8b1835f97c5b34ca2793c66`.
Use the pinned extractor runtime with Poppler 25.03.0, verified legal fonts and
OpenCC. No network is needed. Both Hant and derived Hans are checked.

The opt-in test requires the independently observed original page counts,
complete fixed slots, retained first-article blank band and divider, no production
header payload, hymn columns (1–3 songs per column, 3–5 total), separate back
panels with the vertical tagline outside the summary border, exact source-page
membership, source semantic conservation and an overflow-free persistence proof.

## Original-source results, 2026-10-10

| Issue | Source SHA-256 | Pages | Hant | Derived Hans | Fixed-template checks |
| --- | --- | --- | --- | --- | --- |
| 1739 | fd9f9376f2a016231fd4f72d212471c102afa1fb70468ad6369fd8cc09a65907 | 12 | PASS | PASS | No unresolved art, image, frame or blanket-template check |
| 1740 | 5b848079989db9de92e11876ef6f36f6d109bd68c1daa3c60c125aaa2c277bd4 | 16 | PASS | PASS | No unresolved art, image, frame or blanket-template check |
| 1741 | 959076eaec8af807aa27c69a710cbe843efa09ef1d8ff2a5d3694fcee55bc095 | 24 | PASS | PASS | Same; one church-text word-order check remains |

These runs deliberately do not call the external semantic classifier. They retain
25, 35 and 46 `unknown_body_kind` checks respectively. They are not source loss or
layout overflow, and they are not silently accepted. Classification assistance,
confirmed-example learning and the issue-first editor remain separate delivery
slices. The 1741 church-text difference requires the approved shared settings and
immutable snapshot contract; it is not waived by recognizing source artwork.

## Boundaries

- Reviewed artwork requires exact source identities, canonical wording and source
  bands. Missing, duplicate, moved and unfamiliar images remain review work.
- Border reconstruction uses validated summary/announcement/prayer content and
  anchors only; the private-table exclusion boundary remains mandatory.
- Extractor `weekly-v1.16` creates new candidates. It does not modify existing
  Local/published revisions, remove older conversions, auto-confirm or publish.
- CI, immutable deployment, physical-device reading and production 1739–1741
  editor/re-upload acceptance must be recorded separately. These three originals
  are regression examples, not proof of every future PDF.

## Whole-branch review correction

Independent review found that the legacy summary X cutoff could discard unknown
native margin text after the blanket visual gate was removed. The shared back
normalizer now omits only runs selected by the same exact column predicate that
successfully verified the complete invariant sidebar wording. Other margin text
is retained, with a page/component-located non-waivable source mismatch.
Regressions cover both a separate margin run and an unrelated run inside a valid
sidebar's bounding box. RED was observed for each; complete onlinebulletins race,
vet and full repository/PostgreSQL race checks pass. The fresh reviewer cleared
the P2 after the correction. All six original/locale checks were rerun; semantic,
content and layout hashes remain identical to the pre-correction native runs.
