# Bulletin geometry preflight

CMS Foundation Task 1, with user-supplied 1739/1740 as the acceptance corpus.
Validation uses Python standard library only and the production-pinned Poppler
target. Fixture generation is a controlled local step using bundled ReportLab
and Pillow; neither is added to the API runtime.

```sh
python3 -m unittest discover -s tools/bulletin_geometry -p 'test_*.py'
./scripts/verify-bulletin-pdf-geometry.sh 1739 /controlled/1739.pdf 1740 /controlled/1740.pdf
docker build --target geometry-probe -f Dockerfile.extractor -t bulletin-geometry:verify .
docker run --rm --network none -v /controlled:/input:ro bulletin-geometry:verify 1739 /input/1739.pdf
docker run --rm --network none -v "$PWD/internal/onlinebulletins/testdata:/fixtures:ro" \
  --entrypoint python bulletin-geometry:verify fixtures.py /fixtures
```

`PYTHON` and `PDFTOHTML` override local executable paths. Conversion happens in
an automatically removed temporary directory; subprocess output is not logged.
The report exposes source checksum, Poppler version, check booleans and a fixed
error code only. `geometry_pass` is not a complete template acceptance result.

Without arguments the shell script verifies both sanitized PDFs against their
goldens. Run it inside the pinned image or with matching Poppler 25.03.0; parser
upgrades intentionally invalidate goldens. XML dimensions are truncated; precise
page sizes are checked using pdfinfo. Fonts are defined once per document, not
necessarily on every page.

## Acceptance state

On 2026-10-01 both originals passed controlled Poppler 25.03.0 geometry probes.
1739 has 12 A4 pages; 1740 has 16 A4 pages. Cover is page 1, body begins on page 2,
two-column hymns are penultimate, retained summary/announcements/prayers are last.
Original checksums:

- 1739: `1d711ace0b71c686318d8b9b607b9e5aa808bfce9db5a83af018b9e4248b4fed`
- 1740: `28f7a34bb833475cf462f076351aeae933e7d842b42893819404e73e83b72acb`

The legacy 1731/1733 profiles remain for later regression, not as accepted input.
Schema/renderer/editor work remains gated on the new fixture CI result. Original
text/XML/images remain outside Git; only allowlisted synthetic tokens, legal
substitute font glyphs, and uniform synthetic image pixels are committed.

`EXCLUDED_TOP = 464` is a visually reviewed boundary for these **two fixtures**,
not a production extraction rule for future issues. Future sensitive-region
boundaries must be detected and reviewed before storage/external AI processing.

Generation preserves source text slots and font size classes, replacing source
glyphs with horizontally fitted tokens. It deliberately does not reproduce
original artwork: all image/background slots use uniform synthetic pixels.
This parser corpus is not a UI fidelity acceptance screenshot.

## Structured extraction checkpoint

The Go parser and `ApplyTemplateV1` compose cover, body, hymns and retained back
components with source-sized page slots. Filtering runs before candidate or
classification evidence is returned. Unsupported structures return no fabricated
document. Unknown images/fonts, ambiguous headings and incomplete fixed-template
reconstruction remain blocking review findings; this is **not** a ready or
source-fidelity acceptance result.

`Dockerfile.extractor`'s `extractor` target is separate from the public API image:
non-root, Go binary, three pinned Poppler tools, required shared libraries and CA
certificates only. It contains no Python runtime, shell, package manager, source
PDFs, source font binaries or source image bytes.

`pdftocairo` supplies the retained back frames missing from the text XML. A bounded
Go XML reader accepts only the reviewed scroll/dashed-box/prayer-frame path
families, normalizes Cairo's page matrix, and retains numeric bounds only. The
temporary SVG (including font/image data) is deleted with other process output;
it is never returned, stored as a template, or embedded in the reader. Missing or
ambiguous frames block review. The exclusion cutoff remains unchanged.

```sh
docker build --target extractor -f Dockerfile.extractor \
  --build-arg BULLETIN_RENDERER_ARTIFACT_SHA256="$BULLETIN_RENDERER_ARTIFACT_SHA256" \
  -t bulletin-extractor:verify .
docker run --rm --network none --read-only bulletin-extractor:verify -version
```

The artifact digest must be the exact compiled shared renderer digest. V1 is
currently provisional and has not been published/frozen. The template manifest
checksum is build-pinned; `BULLETIN_EXTRACTOR_ASSET_ORIGIN` is trusted HTTPS job
configuration, never a URL taken from the uploaded document. Every referenced
asset byte and license is verified before extraction.

The single-source command accepts `-source /private/source.pdf` plus canonical
issue JSON on stdin (`issueId`, `series`, `contentLocale`, `metadata`). It writes
bounded candidate/review JSON to stdout as **private job data**, never a log.
Errors expose fixed codes only. Run with a read-only root, bounded writable
`/tmp`, and bounded CPU/memory; grant acquisition, durable job state, exception
classification and Chromium layout validation are subsequent worker integration.
CI mounts the existing sanitized fixtures into the actual minimal runtime and
checks the Go parser there, not just the Python probe image.

To reproduce locally, call `fixtures.generate(source, issue, legal_font_path,
output_directory)` with the controlled originals and font below. Record goldens
only in the pinned image via `fixtures.py /fixtures --record-goldens`, after its
privacy scan, then review the geometry diff. CI never re-records goldens.
