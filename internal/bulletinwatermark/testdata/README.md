# Bulletin watermark synthetic fixtures

`generate.py` uses ReportLab, Pillow and pypdf. No private originals, personal data,
third-party photos or actual signatures are included. The raster landscape is
procedurally drawn. Generated PDFs are test inputs, not accepted weekly bulletins.

- `matrix.pdf`: seven pages, searchable text, two columns, procedural raster,
  dark background, HTTPS link, landscape, rotations 90/180/270, nonzero CropBox.
- `tagged.pdf`: minimal MCID, StructTreeRoot, ParentTree and MarkInfo retention.
  It is not a PDF/UA-certified document or screen-reader acceptance fixture.
- `encrypted.pdf`: blank user password with owner encryption; must still reject.
- `signed.pdf`: deliberately invalid signature dictionary; must reject before
  modifying it. No real signing key or valid signature is supplied.
- `corrupt.pdf`, `truncated.pdf`: malformed and missing-trailer rejection.
- `too-many-pages.pdf`: 61 pages, beyond the supported 60-page limit.

The fixture generator remains useful for PDF structure and input rejection tests.
Use the pinned helper runtime from `tools/bulletin_watermark/README.md`; its PDF
tests preserve the original objects while adding only the invisible overlay.

```sh
python3 internal/bulletinwatermark/testdata/generate.py
HHC_WATERMARK_PDF_TESTS=1 HHC_WATERMARK_PDFTOPPM=/private/bin/pdftoppm \
  /private/runtime/bin/python -m unittest discover -s tools/bulletin_watermark -p 'test_*.py'
GOTOOLCHAIN=go1.25.13 go test ./internal/bulletinwatermark -count=1
```

`generate.py --stress` can also create an exact 20 MiB / 60-page input with a
procedural 2500×2500 raster. Keep that large artifact outside Git. A generated
boundary input alone does not prove successful rendering under deployed limits.

The retired visible-code renderer's `TestRenderArtifacts`, `TestRenderStress`
and human code-transcription matrix are not current acceptance procedures.
The unused visible-evidence script has been removed. Historical private worktrees
and evidence are preserved outside this fixture directory.

For current recovery checks, create private `render_pdf`, `templates` and `detect`
jobs with fresh synthetic payloads. Reconstruct templates from the final PDF and
compare both canonical crops and independently captured browser screenshots.
Keep originals, unknown-copy and missing-true-candidate negatives; report every
inconclusive/failed result. Never commit private originals, screenshots, payloads
or real account mappings. See [acceptance](../../../docs/bulletin-watermark-acceptance.md)
for measured results and remaining reader/LINE/visual gates.
